-- RA-05: separate management qualification, encrypted secrets and verified
-- native account receipts. Original catalog and access migrations stay sealed.
CREATE TABLE mcpguard_ra.provision_targets (
 org_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,version bigint NOT NULL,
 enabled boolean NOT NULL,definition jsonb NOT NULL,
 PRIMARY KEY(org_id,resource_id,resource_version,profile_id,profile_version),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions
);
CREATE TABLE mcpguard_ra.existing_account_registrations (
 org_id uuid NOT NULL,subject_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,version bigint NOT NULL,
 enabled boolean NOT NULL,definition jsonb NOT NULL,
 PRIMARY KEY(org_id,subject_id,resource_id,resource_version,profile_id,profile_version),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions
);
CREATE TABLE mcpguard_ra.account_claims (
 org_id uuid NOT NULL,account_id uuid NOT NULL,subject_id uuid NOT NULL,resource_id uuid NOT NULL,
 resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 target_fingerprint text NOT NULL,native_identity text NOT NULL,management_mode text NOT NULL,
 PRIMARY KEY(org_id,account_id),UNIQUE(target_fingerprint,native_identity),
 UNIQUE(org_id,account_id,subject_id,resource_id,resource_version,profile_id,profile_version),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions,
 CHECK(management_mode IN ('PROVISIONED','PREEXISTING'))
);
CREATE TABLE mcpguard_ra.provisioning_jobs (
 org_id uuid NOT NULL,job_id uuid NOT NULL,grant_id uuid NOT NULL,grant_version bigint NOT NULL,
 account_id uuid NOT NULL,plan_hash text NOT NULL,plan_ciphertext bytea NOT NULL,
 state text NOT NULL CHECK(state IN ('EXECUTING','VERIFIED','OUTCOME_UNKNOWN','FAILED','CLEANUP_PENDING')),
 failure_code text,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,job_id),UNIQUE(org_id,grant_id),
 FOREIGN KEY(org_id,grant_id,grant_version) REFERENCES mcpguard_ra.access_grant_versions,
 FOREIGN KEY(org_id,account_id) REFERENCES mcpguard_ra.account_claims
);
CREATE TABLE mcpguard_ra.native_secrets (
 org_id uuid NOT NULL,account_id uuid NOT NULL,secret_ref text NOT NULL,version bigint NOT NULL,
 ciphertext bytea NOT NULL,PRIMARY KEY(org_id,account_id,version),UNIQUE(secret_ref,version),
 FOREIGN KEY(org_id,account_id) REFERENCES mcpguard_ra.account_claims
);
CREATE TABLE mcpguard_ra.native_observations (
 org_id uuid NOT NULL,observation_id uuid NOT NULL,account_id uuid NOT NULL,job_id uuid NOT NULL,
 role_oid bigint NOT NULL,permission_hash text NOT NULL,definition jsonb NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,observation_id),UNIQUE(org_id,job_id),
 FOREIGN KEY(org_id,account_id) REFERENCES mcpguard_ra.account_claims,
 FOREIGN KEY(org_id,job_id) REFERENCES mcpguard_ra.provisioning_jobs
);
CREATE TABLE mcpguard_ra.native_accounts (
 org_id uuid NOT NULL,account_id uuid NOT NULL,subject_id uuid NOT NULL,resource_id uuid NOT NULL,
 resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 native_identity text NOT NULL,management_mode text NOT NULL,definition jsonb NOT NULL,
 PRIMARY KEY(org_id,account_id),
 FOREIGN KEY(org_id,account_id,subject_id,resource_id,resource_version,profile_id,profile_version) REFERENCES mcpguard_ra.account_claims(org_id,account_id,subject_id,resource_id,resource_version,profile_id,profile_version)
);
CREATE TABLE mcpguard_ra.provision_audit (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),org_id uuid NOT NULL,
 job_id uuid NOT NULL,event_type text NOT NULL,worker_identity text NOT NULL DEFAULT session_user,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,job_id) REFERENCES mcpguard_ra.provisioning_jobs
);
CREATE INDEX provisioning_job_state ON mcpguard_ra.provisioning_jobs(state,created_at);
CREATE INDEX native_secret_account ON mcpguard_ra.native_secrets(org_id,account_id);
CREATE INDEX native_observation_account ON mcpguard_ra.native_observations(org_id,account_id);
CREATE INDEX provision_audit_job ON mcpguard_ra.provision_audit(org_id,job_id,seq);
CREATE TRIGGER account_claim_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.account_claims FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER native_secret_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.native_secrets FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER native_observation_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.native_observations FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER native_account_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.native_accounts FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER provision_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.provision_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard_ra.provision_config_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['version','enabled','definition']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['version','enabled','definition'])
 OR NEW.version<=OLD.version THEN RAISE EXCEPTION 'new provisioning configuration version required';END IF;RETURN NEW;
END $$;
CREATE TRIGGER provision_target_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.provision_targets FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.provision_config_guard();
CREATE TRIGGER existing_registration_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.existing_account_registrations FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.provision_config_guard();
CREATE TABLE mcpguard_ra.provision_config_history (
 seq bigserial PRIMARY KEY,kind text NOT NULL,org_id uuid NOT NULL,version bigint NOT NULL,
 definition jsonb NOT NULL,recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER provision_config_history_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.provision_config_history FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard_ra.record_provision_config() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 INSERT INTO mcpguard_ra.provision_config_history(kind,org_id,version,definition) VALUES(TG_TABLE_NAME,NEW.org_id,NEW.version,NEW.definition);RETURN NEW;
END $$;
CREATE TRIGGER provision_target_history AFTER INSERT OR UPDATE ON mcpguard_ra.provision_targets FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.record_provision_config();
CREATE TRIGGER existing_registration_history AFTER INSERT OR UPDATE ON mcpguard_ra.existing_account_registrations FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.record_provision_config();
CREATE TRIGGER provision_target_epoch AFTER INSERT OR UPDATE ON mcpguard_ra.provision_targets FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TRIGGER existing_registration_epoch AFTER INSERT OR UPDATE ON mcpguard_ra.existing_account_registrations FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE FUNCTION mcpguard_ra.provision_job_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN IF NEW.state<>'EXECUTING' THEN RAISE EXCEPTION 'invalid provisioning intent';END IF;RETURN NEW;END IF;
 IF TG_OP='DELETE' OR OLD.state<>'EXECUTING' OR NEW.state='EXECUTING'
 OR (to_jsonb(NEW)-ARRAY['state','failure_code']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','failure_code']) THEN RAISE EXCEPTION 'immutable provisioning qualification';END IF;RETURN NEW;
END $$;
CREATE TRIGGER provision_job_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.provisioning_jobs FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.provision_job_guard();

-- One stable grant identity per request; activation appends version 2 instead
-- of rewriting the approved version 1.
CREATE TABLE mcpguard_ra.access_grant_ids (
 org_id uuid NOT NULL,grant_id uuid NOT NULL,request_id uuid NOT NULL,
 PRIMARY KEY(org_id,grant_id),UNIQUE(org_id,request_id),UNIQUE(org_id,grant_id,request_id),
 FOREIGN KEY(org_id,request_id) REFERENCES mcpguard_ra.access_requests
);
INSERT INTO mcpguard_ra.access_grant_ids SELECT org_id,grant_id,request_id FROM mcpguard_ra.access_grant_versions;
ALTER TABLE mcpguard_ra.access_grant_versions DROP CONSTRAINT access_grant_versions_org_id_request_id_key;
ALTER TABLE mcpguard_ra.access_grant_versions ADD FOREIGN KEY(org_id,grant_id,request_id) REFERENCES mcpguard_ra.access_grant_ids(org_id,grant_id,request_id);
ALTER TABLE mcpguard_ra.access_grant_versions ADD COLUMN account_id uuid,ADD COLUMN binding_id uuid,ADD COLUMN binding_version bigint,ADD COLUMN observation_id uuid,ADD COLUMN activation_job_id uuid;
ALTER TABLE mcpguard_ra.access_grant_versions ADD FOREIGN KEY(org_id,account_id) REFERENCES mcpguard_ra.native_accounts;
ALTER TABLE mcpguard_ra.access_grant_versions ADD FOREIGN KEY(org_id,binding_id,binding_version) REFERENCES mcpguard_ra.identity_bindings(org_id,binding_id,binding_version);
ALTER TABLE mcpguard_ra.access_grant_versions ADD FOREIGN KEY(org_id,observation_id) REFERENCES mcpguard_ra.native_observations;
ALTER TABLE mcpguard_ra.access_grant_versions ADD FOREIGN KEY(org_id,activation_job_id) REFERENCES mcpguard_ra.provisioning_jobs;
CREATE TRIGGER grant_identity_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.access_grant_ids FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE OR REPLACE FUNCTION mcpguard_ra.grant_version_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF NEW.version=1 THEN
  IF NEW.account_id IS NOT NULL OR NEW.binding_id IS NOT NULL OR NOT EXISTS(SELECT FROM mcpguard_ra.access_decisions d JOIN mcpguard_ra.access_requests r ON (r.org_id,r.request_id)=(d.org_id,d.request_id) WHERE d.org_id=NEW.org_id AND d.decision_id=NEW.decision_id AND d.request_id=NEW.request_id AND d.decision='APPROVE' AND d.approved_expiry=NEW.expires_at AND r.state='PENDING_APPROVAL') THEN RAISE EXCEPTION 'grant requires matching approved snapshot';END IF;
 ELSIF NEW.version=2 THEN
  IF NOT EXISTS(SELECT FROM mcpguard_ra.access_grant_versions prior_version
   JOIN mcpguard_ra.provisioning_jobs j ON (j.org_id,j.grant_id)=(prior_version.org_id,prior_version.grant_id)
   JOIN mcpguard_ra.native_observations o ON (o.org_id,o.job_id,o.account_id)=(j.org_id,j.job_id,j.account_id)
   JOIN mcpguard_ra.native_accounts a ON (a.org_id,a.account_id)=(j.org_id,j.account_id)
   JOIN mcpguard_ra.identity_bindings b ON (b.org_id,b.binding_id,b.binding_version)=(NEW.org_id,NEW.binding_id,NEW.binding_version)
   WHERE prior_version.org_id=NEW.org_id AND prior_version.grant_id=NEW.grant_id AND prior_version.version=1 AND j.state='VERIFIED' AND j.job_id=NEW.activation_job_id AND o.observation_id=NEW.observation_id AND a.account_id=NEW.account_id
   AND (prior_version.request_id,prior_version.decision_id,prior_version.subject_id,prior_version.resource_id,prior_version.resource_version,prior_version.profile_id,prior_version.profile_version,prior_version.expires_at)=(NEW.request_id,NEW.decision_id,NEW.subject_id,NEW.resource_id,NEW.resource_version,NEW.profile_id,NEW.profile_version,NEW.expires_at)
   AND (a.subject_id,a.resource_id,a.resource_version,a.profile_id,a.profile_version,a.native_identity)=(NEW.subject_id,NEW.resource_id,NEW.resource_version,NEW.profile_id,NEW.profile_version,b.definition->>'nativeIdentity') AND b.enabled AND b.definition->>'accountId'=a.account_id::text)
  THEN RAISE EXCEPTION 'activation requires verified native account';END IF;
 ELSE RAISE EXCEPTION 'unsupported grant version';END IF;RETURN NEW;
END $$;
ALTER TABLE mcpguard_ra.access_grant_heads DROP CONSTRAINT access_grant_heads_state_check;
ALTER TABLE mcpguard_ra.access_grant_heads ADD CHECK(state IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN','PROVISIONING_FAILED','REVOKED','EXPIRED'));
CREATE OR REPLACE FUNCTION mcpguard_ra.grant_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN IF NEW.state<>'PROVISIONING' OR NEW.revision<>1 OR NEW.version<>1 THEN RAISE EXCEPTION 'unverified grant cannot activate';END IF;RETURN NEW;END IF;
 IF TG_OP='DELETE' OR NEW.revision<>OLD.revision+1 OR NEW.auth_epoch<=OLD.auth_epoch OR (NEW.org_id,NEW.grant_id) IS DISTINCT FROM (OLD.org_id,OLD.grant_id) THEN RAISE EXCEPTION 'invalid grant transition';END IF;
 IF NEW.state='ACTIVE' THEN
  IF OLD.state<>'PROVISIONING' OR OLD.version<>1 OR NEW.version<>2 OR NOT EXISTS(SELECT FROM mcpguard_ra.access_grant_versions v JOIN mcpguard_ra.provisioning_jobs j ON (j.org_id,j.job_id)=(v.org_id,v.activation_job_id) WHERE v.org_id=NEW.org_id AND v.grant_id=NEW.grant_id AND v.version=2 AND j.state='VERIFIED' AND v.expires_at>clock_timestamp()) THEN RAISE EXCEPTION 'unverified grant cannot activate';END IF;
 ELSE
  IF NEW.version<>OLD.version OR OLD.state NOT IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN') OR NEW.state NOT IN ('REVOKED','EXPIRED','PROVISIONING_UNKNOWN','PROVISIONING_FAILED') OR (OLD.state<>'PROVISIONING' AND NEW.state NOT IN ('REVOKED','EXPIRED')) THEN RAISE EXCEPTION 'terminal grant cannot revive';END IF;
 END IF;RETURN NEW;
END $$;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard_ra FROM PUBLIC;
