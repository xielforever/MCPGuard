-- RA-04 only: approved requests remain PROVISIONING. No native account or
-- ACTIVE grant can be manufactured by this migration or its runtime role.
ALTER TABLE mcpguard_ra.resource_access ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK(revision>0);
CREATE FUNCTION mcpguard_ra.access_revision_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'disable resource access instead of deleting its revision';END IF;
 IF (NEW.org_id,NEW.principal_id,NEW.resource_id) IS DISTINCT FROM (OLD.org_id,OLD.principal_id,OLD.resource_id) THEN RAISE EXCEPTION 'immutable access identity';END IF;
 NEW.revision:=OLD.revision+CASE WHEN (NEW.can_discover,NEW.can_request) IS DISTINCT FROM (OLD.can_discover,OLD.can_request) THEN 1 ELSE 0 END;
 RETURN NEW;
END $$;
CREATE TRIGGER access_revision BEFORE UPDATE OR DELETE ON mcpguard_ra.resource_access FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.access_revision_guard();
CREATE TABLE mcpguard_ra.subject_revisions (
 org_id uuid NOT NULL,principal_id uuid NOT NULL,revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 PRIMARY KEY(org_id,principal_id),FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id) ON DELETE CASCADE
);
INSERT INTO mcpguard_ra.subject_revisions(org_id,principal_id) SELECT org_id,id FROM mcpguard.principals;
CREATE FUNCTION mcpguard_ra.subject_revision() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN INSERT INTO mcpguard_ra.subject_revisions(org_id,principal_id) VALUES(NEW.org_id,NEW.id);
 ELSIF NEW.enabled IS DISTINCT FROM OLD.enabled THEN UPDATE mcpguard_ra.subject_revisions SET revision=revision+1 WHERE org_id=NEW.org_id AND principal_id=NEW.id;END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ra_subject_revision AFTER INSERT OR UPDATE ON mcpguard.principals FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.subject_revision();
CREATE TABLE mcpguard_ra.access_policy_versions (
 org_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,version bigint NOT NULL CHECK(version>0),definition jsonb NOT NULL,
 PRIMARY KEY(org_id,resource_id,resource_version,profile_id,profile_version,version),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions
);
CREATE TABLE mcpguard_ra.access_policy_heads (
 org_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,version bigint NOT NULL,revision bigint NOT NULL CHECK(revision>0),enabled boolean NOT NULL,
 PRIMARY KEY(org_id,resource_id,resource_version,profile_id,profile_version),
 FOREIGN KEY(org_id,resource_id,resource_version,profile_id,profile_version,version) REFERENCES mcpguard_ra.access_policy_versions
);
CREATE TABLE mcpguard_ra.access_reviewers (
 org_id uuid NOT NULL,resource_id uuid NOT NULL,principal_id uuid NOT NULL,can_approve boolean NOT NULL,
 PRIMARY KEY(org_id,resource_id,principal_id),FOREIGN KEY(org_id,resource_id) REFERENCES mcpguard_ra.resource_heads,
 FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE INDEX access_reviewer_subject ON mcpguard_ra.access_reviewers(org_id,principal_id,resource_id);
CREATE TRIGGER policy_version_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.access_policy_versions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER policy_head_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.access_policy_heads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.head_guard();
CREATE TRIGGER policy_head_epoch AFTER INSERT OR UPDATE OR DELETE ON mcpguard_ra.access_policy_heads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TRIGGER access_reviewer_epoch AFTER INSERT OR UPDATE OR DELETE ON mcpguard_ra.access_reviewers FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TABLE mcpguard_ra.access_requests (
 org_id uuid NOT NULL,request_id uuid NOT NULL,subject_id uuid NOT NULL,
 resource_id uuid NOT NULL,resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 request_hash text NOT NULL CHECK(length(request_hash)=43),snapshot_revision bigint NOT NULL DEFAULT 1 CHECK(snapshot_revision=1),
 requested_expiry timestamptz NOT NULL,request_deadline timestamptz NOT NULL,qualification jsonb NOT NULL,
 request_ciphertext bytea NOT NULL,state text NOT NULL CHECK(state IN ('PENDING_APPROVAL','APPROVED','REJECTED','CANCELLED','EXPIRED','STALE')),
 revision bigint NOT NULL CHECK(revision>0),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,request_id),
 UNIQUE(org_id,request_id,request_hash,snapshot_revision),
 UNIQUE(org_id,request_id,subject_id,resource_id,resource_version,profile_id,profile_version),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions,
 CHECK(request_deadline<=requested_expiry)
);
CREATE INDEX access_request_owner ON mcpguard_ra.access_requests(org_id,subject_id,resource_id,request_id);
CREATE INDEX access_request_queue ON mcpguard_ra.access_requests(org_id,resource_id,request_id) WHERE state='PENDING_APPROVAL';
CREATE TABLE mcpguard_ra.access_decisions (
 org_id uuid NOT NULL,decision_id uuid NOT NULL,request_id uuid NOT NULL,request_hash text NOT NULL,request_revision bigint NOT NULL,
 subject_id uuid NOT NULL,decider_id uuid NOT NULL,decision text NOT NULL CHECK(decision IN ('APPROVE','REJECT')),
 source text NOT NULL CHECK(source='HUMAN'),approved_expiry timestamptz,decision_ciphertext bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(org_id,decision_id),
 UNIQUE(org_id,request_id),UNIQUE(org_id,decision_id,request_id,decision),
 FOREIGN KEY(org_id,request_id,request_hash,request_revision) REFERENCES mcpguard_ra.access_requests(org_id,request_id,request_hash,snapshot_revision),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,decider_id) REFERENCES mcpguard.principals(org_id,id),
 CHECK(decider_id<>subject_id),CHECK((decision='APPROVE')=(approved_expiry IS NOT NULL))
);
CREATE TABLE mcpguard_ra.access_grant_versions (
 org_id uuid NOT NULL,grant_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),request_id uuid NOT NULL,
 decision_id uuid NOT NULL,decision text NOT NULL DEFAULT 'APPROVE' CHECK(decision='APPROVE'),subject_id uuid NOT NULL,
 resource_id uuid NOT NULL,resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,grant_id,version),UNIQUE(org_id,request_id),
 FOREIGN KEY(org_id,decision_id,request_id,decision) REFERENCES mcpguard_ra.access_decisions(org_id,decision_id,request_id,decision),
 FOREIGN KEY(org_id,request_id,subject_id,resource_id,resource_version,profile_id,profile_version) REFERENCES mcpguard_ra.access_requests(org_id,request_id,subject_id,resource_id,resource_version,profile_id,profile_version)
);
CREATE INDEX access_grant_owner ON mcpguard_ra.access_grant_versions(org_id,subject_id,grant_id);
CREATE FUNCTION mcpguard_ra.grant_version_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF NEW.version<>1 OR NOT EXISTS(SELECT FROM mcpguard_ra.access_decisions d JOIN mcpguard_ra.access_requests r ON (r.org_id,r.request_id)=(d.org_id,d.request_id) WHERE d.org_id=NEW.org_id AND d.decision_id=NEW.decision_id AND d.request_id=NEW.request_id AND d.decision='APPROVE' AND d.approved_expiry=NEW.expires_at AND r.state='PENDING_APPROVAL') THEN RAISE EXCEPTION 'grant requires matching approved snapshot';END IF;RETURN NEW;
END $$;
CREATE TRIGGER access_grant_version_guard BEFORE INSERT ON mcpguard_ra.access_grant_versions FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.grant_version_guard();
CREATE TABLE mcpguard_ra.access_grant_heads (
 org_id uuid NOT NULL,grant_id uuid NOT NULL,version bigint NOT NULL,
 state text NOT NULL CHECK(state IN ('PROVISIONING','REVOKED','EXPIRED')),
 revision bigint NOT NULL CHECK(revision>0),auth_epoch bigint NOT NULL CHECK(auth_epoch>0),
 PRIMARY KEY(org_id,grant_id),FOREIGN KEY(org_id,grant_id,version) REFERENCES mcpguard_ra.access_grant_versions
);
CREATE TABLE mcpguard_ra.access_commands (
 org_id uuid NOT NULL,actor_id uuid NOT NULL,command text NOT NULL,target text NOT NULL,key text NOT NULL,
 payload_hash text NOT NULL,response_ciphertext bytea NOT NULL,
 PRIMARY KEY(org_id,actor_id,command,target,key),FOREIGN KEY(org_id,actor_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard_ra.access_audit (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),org_id uuid NOT NULL,actor_id uuid NOT NULL,
 subject_id uuid NOT NULL,request_id uuid NOT NULL,grant_id uuid,event_type text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,actor_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,request_id) REFERENCES mcpguard_ra.access_requests,
 FOREIGN KEY(org_id,grant_id) REFERENCES mcpguard_ra.access_grant_heads
);
CREATE INDEX access_audit_request ON mcpguard_ra.access_audit(org_id,request_id,seq);
CREATE TRIGGER access_decision_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.access_decisions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER access_grant_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.access_grant_versions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER access_command_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.access_commands FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER access_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.access_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard_ra.request_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'PENDING_APPROVAL' OR NEW.revision<>1 THEN RAISE EXCEPTION 'invalid initial request';END IF;RETURN NEW;
 END IF;
 IF TG_OP='DELETE' OR OLD.state<>'PENDING_APPROVAL' OR NEW.state='PENDING_APPROVAL' OR NEW.revision<>OLD.revision+1
 OR (to_jsonb(NEW)-ARRAY['state','revision']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision']) THEN RAISE EXCEPTION 'immutable access request';END IF;
 IF NEW.state='APPROVED' AND NOT EXISTS(SELECT FROM mcpguard_ra.access_decisions d JOIN mcpguard_ra.access_grant_versions g ON (g.org_id,g.request_id,g.decision_id)=(d.org_id,d.request_id,d.decision_id) WHERE d.org_id=NEW.org_id AND d.request_id=NEW.request_id AND d.decision='APPROVE') THEN RAISE EXCEPTION 'approval requires decision and grant';END IF;
 IF NEW.state='REJECTED' AND NOT EXISTS(SELECT FROM mcpguard_ra.access_decisions WHERE org_id=NEW.org_id AND request_id=NEW.request_id AND decision='REJECT') THEN RAISE EXCEPTION 'rejection requires decision';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER access_request_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.access_requests FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.request_guard();
CREATE FUNCTION mcpguard_ra.decision_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE r mcpguard_ra.access_requests;
BEGIN
 SELECT * INTO STRICT r FROM mcpguard_ra.access_requests WHERE org_id=NEW.org_id AND request_id=NEW.request_id;
 IF r.subject_id<>NEW.subject_id OR r.state<>'PENDING_APPROVAL' OR r.revision<>NEW.request_revision OR r.request_deadline<=clock_timestamp()
 OR (NEW.decision='APPROVE' AND (NEW.approved_expiry>r.requested_expiry OR NEW.approved_expiry<=clock_timestamp())) THEN RAISE EXCEPTION 'invalid access decision';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER access_decision_guard BEFORE INSERT ON mcpguard_ra.access_decisions FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.decision_guard();
CREATE FUNCTION mcpguard_ra.grant_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'PROVISIONING' OR NEW.revision<>1 THEN RAISE EXCEPTION 'unverified grant cannot activate';END IF;RETURN NEW;
 END IF;
 IF TG_OP='DELETE' OR OLD.state<>'PROVISIONING' OR NEW.state NOT IN ('REVOKED','EXPIRED') OR NEW.revision<>OLD.revision+1 OR NEW.auth_epoch<=OLD.auth_epoch
 OR (NEW.org_id,NEW.grant_id,NEW.version) IS DISTINCT FROM (OLD.org_id,OLD.grant_id,OLD.version) THEN RAISE EXCEPTION 'invalid grant transition';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER access_grant_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.access_grant_heads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.grant_guard();
CREATE FUNCTION mcpguard_ra.advance_access_epoch(target_org uuid) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE result bigint;
BEGIN UPDATE mcpguard.authorization_heads SET auth_epoch=auth_epoch+1 WHERE org_id=target_org RETURNING auth_epoch INTO STRICT result;RETURN result;END $$;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard_ra FROM PUBLIC;
