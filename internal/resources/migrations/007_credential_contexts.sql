-- RA-09. Enrolled clients and online contexts are separate from MCP sessions.
-- No raw context token or envelope is stored in commands, tasks or audit.
CREATE TABLE mcpguard_ra.credential_clients(
 org_id uuid NOT NULL,client_id uuid NOT NULL,subject_id uuid NOT NULL,authorization_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),revision bigint NOT NULL CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,client_id),UNIQUE(org_id,authorization_id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,authorization_id) REFERENCES mcpguard_ra.delivery_authorizations
);
CREATE FUNCTION mcpguard_ra.credential_client_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'ACTIVE' OR NEW.revision<>1 OR NOT EXISTS(SELECT FROM mcpguard_ra.delivery_authorizations a WHERE (a.org_id,a.authorization_id,a.subject_id)=(NEW.org_id,NEW.authorization_id,NEW.subject_id) AND a.state='ACTIVE' AND a.expires_at>clock_timestamp()) THEN RAISE EXCEPTION 'current enrolled client authority required';END IF;
 ELSE
  IF TG_OP='DELETE' OR OLD.state<>'ACTIVE' OR NEW.state<>'REVOKED' OR NEW.revision<>OLD.revision+1 OR (to_jsonb(NEW)-ARRAY['state','revision']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision']) THEN RAISE EXCEPTION 'client identity cannot change or revive';END IF;
 END IF;RETURN NEW;
END $$;
CREATE TRIGGER credential_client_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.credential_clients FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_client_guard();
CREATE TABLE mcpguard_ra.credential_contexts(
 org_id uuid NOT NULL,context_id uuid NOT NULL,subject_id uuid NOT NULL,client_id uuid NOT NULL,
 principal_token_hash bytea NOT NULL,verifier_hash text NOT NULL UNIQUE CHECK(verifier_hash ~ '^[A-Za-z0-9_-]{43}$'),
 issued_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,absolute_expires_at timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','EXPIRED','REVOKED')),revision bigint NOT NULL CHECK(revision>0),
 PRIMARY KEY(org_id,context_id),FOREIGN KEY(org_id,client_id) REFERENCES mcpguard_ra.credential_clients,
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 CHECK(issued_at<expires_at AND expires_at<=absolute_expires_at AND absolute_expires_at<=issued_at+interval '3600 seconds')
);
CREATE UNIQUE INDEX credential_one_active_context ON mcpguard_ra.credential_contexts(org_id,client_id) WHERE state='ACTIVE';
CREATE FUNCTION mcpguard_ra.credential_context_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'ACTIVE' OR NEW.revision<>1 OR NEW.issued_at>clock_timestamp() OR NEW.expires_at<=clock_timestamp() OR NEW.expires_at>NEW.issued_at+interval '900 seconds' THEN RAISE EXCEPTION 'bounded context required';END IF;
 ELSE
  IF TG_OP='DELETE' OR OLD.state<>'ACTIVE' OR NEW.revision<>OLD.revision+1 OR (to_jsonb(NEW)-ARRAY['state','revision','expires_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','expires_at']) THEN RAISE EXCEPTION 'context cannot revive or change identity';END IF;
  IF NEW.state='ACTIVE' AND (OLD.expires_at<=clock_timestamp() OR NEW.expires_at<=OLD.expires_at OR NEW.expires_at>clock_timestamp()+interval '900 seconds') THEN RAISE EXCEPTION 'expired context cannot renew';END IF;
  IF NEW.state<>'ACTIVE' AND NEW.expires_at<>OLD.expires_at THEN RAISE EXCEPTION 'terminal context expiry is immutable';END IF;
 END IF;
 IF NEW.state='ACTIVE' AND NOT EXISTS(SELECT FROM mcpguard_ra.credential_clients c JOIN mcpguard_ra.delivery_authorizations a ON (a.org_id,a.authorization_id)=(c.org_id,c.authorization_id) JOIN mcpguard.principals p ON (p.org_id,p.id)=(c.org_id,c.subject_id) WHERE (c.org_id,c.client_id,c.subject_id)=(NEW.org_id,NEW.client_id,NEW.subject_id) AND c.state='ACTIVE' AND a.state='ACTIVE' AND a.subject_id=c.subject_id AND a.principal_token_hash=NEW.principal_token_hash AND p.token_hash=NEW.principal_token_hash AND p.enabled AND p.expires_at>=NEW.absolute_expires_at AND a.expires_at>=NEW.absolute_expires_at) THEN RAISE EXCEPTION 'current context identity required';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER credential_context_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.credential_contexts FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_context_guard();
CREATE TABLE mcpguard_ra.context_commands(
 org_id uuid NOT NULL,subject_id uuid NOT NULL,client_id uuid NOT NULL,command text NOT NULL,target text NOT NULL,request_hash text NOT NULL,payload_hash text NOT NULL,
 context_id uuid NOT NULL,PRIMARY KEY(org_id,subject_id,client_id,command,target,request_hash),
 FOREIGN KEY(org_id,context_id) REFERENCES mcpguard_ra.credential_contexts
);
CREATE TABLE mcpguard_ra.context_activation_versions(
 org_id uuid NOT NULL,context_id uuid NOT NULL,resource_id uuid NOT NULL,revision bigint NOT NULL CHECK(revision>0),
 credential_id uuid NOT NULL,lease_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','EXPIRED','REVOKED')),
 definition jsonb NOT NULL,PRIMARY KEY(org_id,context_id,resource_id,revision),
 FOREIGN KEY(org_id,context_id) REFERENCES mcpguard_ra.credential_contexts,
 FOREIGN KEY(org_id,lease_id,credential_id) REFERENCES mcpguard_ra.credential_leases(org_id,lease_id,credential_id)
);
CREATE TABLE mcpguard_ra.context_activation_heads(
 org_id uuid NOT NULL,context_id uuid NOT NULL,resource_id uuid NOT NULL,revision bigint NOT NULL,
 PRIMARY KEY(org_id,context_id,resource_id),
 FOREIGN KEY(org_id,context_id,resource_id,revision) REFERENCES mcpguard_ra.context_activation_versions
);
CREATE FUNCTION mcpguard_ra.context_activation_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF NEW.state='ACTIVE' AND NOT EXISTS(SELECT FROM mcpguard_ra.credential_contexts c JOIN mcpguard_ra.credential_leases l ON (l.org_id,l.credential_id,l.lease_id)=(NEW.org_id,NEW.credential_id,NEW.lease_id) WHERE (c.org_id,c.context_id)=(NEW.org_id,NEW.context_id) AND c.state='ACTIVE' AND c.expires_at>clock_timestamp() AND l.state='ACTIVE' AND l.expires_at>clock_timestamp() AND c.subject_id=l.subject_id AND l.resource_id=NEW.resource_id) THEN RAISE EXCEPTION 'active context and current personal lease required';END IF;RETURN NEW;
END $$;
CREATE TRIGGER context_activation_guard BEFORE INSERT ON mcpguard_ra.context_activation_versions FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.context_activation_guard();
CREATE FUNCTION mcpguard_ra.context_activation_head_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='INSERT' AND NEW.revision<>1) OR (TG_OP='UPDATE' AND (NEW.revision<>OLD.revision+1 OR (NEW.org_id,NEW.context_id,NEW.resource_id)<>(OLD.org_id,OLD.context_id,OLD.resource_id))) THEN RAISE EXCEPTION 'activation history must append';END IF;RETURN NEW;
END $$;
CREATE TRIGGER context_activation_head_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.context_activation_heads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.context_activation_head_guard();
ALTER TABLE mcpguard_ra.download_tickets ADD COLUMN client_id uuid;
ALTER TABLE mcpguard_ra.download_tickets ADD COLUMN claimed_context_id uuid;
ALTER TABLE mcpguard_ra.download_tickets ADD COLUMN queue_seq bigserial;
ALTER TABLE mcpguard_ra.download_tickets ADD CONSTRAINT download_client_fk FOREIGN KEY(org_id,client_id) REFERENCES mcpguard_ra.credential_clients;
ALTER TABLE mcpguard_ra.download_tickets ADD CONSTRAINT download_context_fk FOREIGN KEY(org_id,claimed_context_id) REFERENCES mcpguard_ra.credential_contexts;
CREATE UNIQUE INDEX download_queue_sequence ON mcpguard_ra.download_tickets(queue_seq);
CREATE INDEX download_client_queue ON mcpguard_ra.download_tickets(org_id,subject_id,client_id,queue_seq) WHERE client_id IS NOT NULL;
CREATE OR REPLACE FUNCTION mcpguard_ra.download_ticket_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'READY' OR NEW.revision<>1 OR NEW.issued_at>clock_timestamp() OR NEW.expires_at<=clock_timestamp() OR NOT EXISTS(SELECT FROM mcpguard_ra.credential_leases l WHERE (l.org_id,l.lease_id,l.credential_id,l.subject_id)=(NEW.org_id,NEW.lease_id,NEW.credential_id,NEW.subject_id) AND l.state='ACTIVE' AND l.expires_at>=NEW.expires_at) THEN RAISE EXCEPTION 'download requires current bounded lease';END IF;
  IF NEW.client_id IS NOT NULL AND NOT EXISTS(SELECT FROM mcpguard_ra.credential_clients c JOIN mcpguard_ra.credential_contexts x USING(org_id,client_id) WHERE (c.org_id,c.client_id,c.subject_id)=(NEW.org_id,NEW.client_id,NEW.subject_id) AND c.state='ACTIVE' AND x.state='ACTIVE' AND x.expires_at>clock_timestamp()) THEN RAISE EXCEPTION 'enrolled client context required';END IF;
 ELSE
  IF TG_OP='DELETE' OR OLD.state<>'READY' OR NEW.state NOT IN ('CLAIMED','EXPIRED','CANCELLED') OR NEW.revision<>OLD.revision+1 OR (to_jsonb(NEW)-ARRAY['state','revision','claimed_at','claimed_authorization_id','claimed_context_id']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','claimed_at','claimed_authorization_id','claimed_context_id']) THEN RAISE EXCEPTION 'download ticket is consumed once';END IF;
  IF NEW.state='CLAIMED' AND (OLD.expires_at<=clock_timestamp() OR NOT EXISTS(SELECT FROM mcpguard_ra.delivery_authorizations a JOIN mcpguard.principals p ON (p.org_id,p.id)=(a.org_id,a.subject_id) WHERE a.org_id=NEW.org_id AND a.authorization_id=NEW.claimed_authorization_id AND a.subject_id=NEW.subject_id AND a.audience='mcpguard-delivery' AND a.scope='credential:claim' AND a.state='ACTIVE' AND a.expires_at>clock_timestamp() AND a.principal_token_hash=p.token_hash AND p.enabled AND p.expires_at>clock_timestamp())) THEN RAISE EXCEPTION 'current private claim authorization required';END IF;
  IF NEW.state='CLAIMED' AND NEW.client_id IS NOT NULL AND NOT EXISTS(SELECT FROM mcpguard_ra.credential_contexts x JOIN mcpguard_ra.credential_clients c USING(org_id,client_id) WHERE (x.org_id,x.context_id,x.subject_id,x.client_id)=(NEW.org_id,NEW.claimed_context_id,NEW.subject_id,NEW.client_id) AND x.state='ACTIVE' AND x.expires_at>clock_timestamp() AND c.state='ACTIVE' AND c.authorization_id=NEW.claimed_authorization_id) THEN RAISE EXCEPTION 'claim context must match enrolled ticket client';END IF;
  IF NEW.client_id IS NULL AND NEW.claimed_context_id IS NOT NULL THEN RAISE EXCEPTION 'manual ticket has no client context';END IF;
 END IF;RETURN NEW;
END $$;
CREATE TABLE mcpguard_ra.download_task_receipts(
 org_id uuid NOT NULL,download_id uuid NOT NULL,client_id uuid NOT NULL,context_id uuid NOT NULL,
 outcome text NOT NULL CHECK(outcome IN ('ACTIVATED','REPLACED','DISCARDED')),replacement_id uuid,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(org_id,download_id),
 FOREIGN KEY(org_id,download_id) REFERENCES mcpguard_ra.download_tickets,
 FOREIGN KEY(org_id,replacement_id) REFERENCES mcpguard_ra.download_tickets,
 FOREIGN KEY(org_id,client_id) REFERENCES mcpguard_ra.credential_clients,
 FOREIGN KEY(org_id,context_id) REFERENCES mcpguard_ra.credential_contexts,
 CHECK((outcome='REPLACED')=(replacement_id IS NOT NULL))
);
CREATE TABLE mcpguard_ra.credential_renewals(
 org_id uuid NOT NULL,subject_id uuid NOT NULL,previous_id uuid NOT NULL,credential_id uuid NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(org_id,previous_id),
 FOREIGN KEY(org_id,previous_id) REFERENCES mcpguard_ra.credential_packages,
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages,
 CHECK(previous_id<>credential_id)
);
CREATE TABLE mcpguard_ra.context_audit(
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),org_id uuid NOT NULL,subject_id uuid NOT NULL,client_id uuid NOT NULL,
 context_id uuid,resource_id uuid,credential_id uuid,download_id uuid,
 event_type text NOT NULL CHECK(event_type IN ('CLIENT_ENROLLED','CONTEXT_CREATED','CONTEXT_RENEWED','CONTEXT_REVOKED','CONTEXT_EXPIRED','ACTIVATED','ACTIVATION_REVOKED','ACTIVATION_EXPIRED','CLIENT_DEACTIVATED','TASK_ACTIVATED','TASK_REPLACED','TASK_DISCARDED','CREDENTIAL_RENEWED')),
 server_identity text NOT NULL DEFAULT session_user,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,client_id) REFERENCES mcpguard_ra.credential_clients,
 FOREIGN KEY(org_id,context_id) REFERENCES mcpguard_ra.credential_contexts
);
CREATE TRIGGER context_commands_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.context_commands FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER context_activation_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.context_activation_versions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER context_receipts_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.download_task_receipts FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER credential_renewal_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_renewals FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER context_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.context_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
REVOKE ALL ON mcpguard_ra.credential_clients,mcpguard_ra.credential_contexts,mcpguard_ra.context_commands,mcpguard_ra.context_activation_versions,mcpguard_ra.context_activation_heads,mcpguard_ra.download_task_receipts,mcpguard_ra.credential_renewals,mcpguard_ra.context_audit FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;
