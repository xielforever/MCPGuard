-- RA-06: leases fence metadata completion; native reconciliation never obtains
-- another CREATE qualification. Original migrations and plan bytes stay sealed.
CREATE TABLE mcpguard_ra.provision_recovery (
 org_id uuid NOT NULL,job_id uuid NOT NULL,generation bigint NOT NULL CHECK(generation>=0),
 owner_id uuid,lease_until timestamptz NOT NULL DEFAULT 'epoch',
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 operation text NOT NULL CHECK(operation IN ('DISPATCH','RECONCILE','CLEANUP','FINALIZE')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 6),
 cleanup_attempts integer NOT NULL DEFAULT 0 CHECK(cleanup_attempts BETWEEN 0 AND 6),
 cleanup_state text NOT NULL DEFAULT 'NOT_REQUIRED' CHECK(cleanup_state IN ('NOT_REQUIRED','PENDING','SUCCEEDED','MANUAL_REQUIRED')),
 last_outcome text CHECK(last_outcome ~ '^[A-Z][A-Z0-9_]{0,63}$'),native_receipt jsonb,
 PRIMARY KEY(org_id,job_id),FOREIGN KEY(org_id,job_id) REFERENCES mcpguard_ra.provisioning_jobs
);
INSERT INTO mcpguard_ra.provision_recovery(org_id,job_id,generation,operation,cleanup_state)
 SELECT org_id,job_id,0,CASE WHEN state='CLEANUP_PENDING' THEN 'CLEANUP' ELSE 'RECONCILE' END,
 CASE WHEN state='CLEANUP_PENDING' THEN 'PENDING' ELSE 'NOT_REQUIRED' END FROM mcpguard_ra.provisioning_jobs;
CREATE INDEX provision_recovery_due ON mcpguard_ra.provision_recovery(next_attempt_at,lease_until,org_id,job_id);
CREATE TABLE mcpguard_ra.provision_attempt_events (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),org_id uuid NOT NULL,job_id uuid NOT NULL,
 generation bigint NOT NULL,event_type text NOT NULL,operation text NOT NULL,
 worker_identity text NOT NULL DEFAULT session_user,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(org_id,job_id,generation,event_type),
 FOREIGN KEY(org_id,job_id) REFERENCES mcpguard_ra.provisioning_jobs
);
CREATE TRIGGER provision_attempt_event_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.provision_attempt_events FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard_ra.assert_provision_lease(org uuid,job uuid) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF NOT EXISTS(SELECT FROM mcpguard_ra.provision_recovery l WHERE l.org_id=org AND l.job_id=job
 AND l.owner_id::text=current_setting('mcpguard.provision_owner',true)
 AND l.generation::text=current_setting('mcpguard.provision_generation',true)
 AND l.lease_until>clock_timestamp()) THEN RAISE EXCEPTION 'provisioning lease is not current';END IF;
END $$;
CREATE FUNCTION mcpguard_ra.provision_recovery_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'recovery history cannot be removed';END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.generation<>1 OR NEW.owner_id IS NULL OR NEW.operation<>'DISPATCH' OR NEW.attempts<>0 OR NEW.cleanup_attempts<>0 THEN RAISE EXCEPTION 'invalid initial dispatch lease';END IF;
 ELSE
  IF (NEW.org_id,NEW.job_id) IS DISTINCT FROM (OLD.org_id,OLD.job_id) THEN RAISE EXCEPTION 'immutable recovery identity';END IF;
  IF NEW.generation=OLD.generation+1 THEN
   IF OLD.lease_until>clock_timestamp() OR OLD.next_attempt_at>clock_timestamp() OR NEW.owner_id IS NULL OR NEW.operation NOT IN ('RECONCILE','CLEANUP','FINALIZE')
   OR (NEW.operation='FINALIZE' AND OLD.attempts<6 AND OLD.cleanup_attempts<6)
   OR NEW.attempts<>OLD.attempts+(CASE WHEN NEW.operation='RECONCILE' THEN 1 ELSE 0 END)
   OR NEW.cleanup_attempts<>OLD.cleanup_attempts+(CASE WHEN NEW.operation='CLEANUP' THEN 1 ELSE 0 END)
   OR NEW.cleanup_state<>OLD.cleanup_state OR NEW.native_receipt IS NOT NULL THEN RAISE EXCEPTION 'invalid recovery claim';END IF;
  ELSIF NEW.generation=OLD.generation THEN
   PERFORM mcpguard_ra.assert_provision_lease(OLD.org_id,OLD.job_id);
   IF NEW.operation<>OLD.operation OR NEW.attempts<>OLD.attempts OR NEW.cleanup_attempts<>OLD.cleanup_attempts
   OR (NEW.owner_id IS NOT NULL AND NEW.owner_id IS DISTINCT FROM OLD.owner_id)
   OR (NEW.owner_id IS NOT NULL AND NEW.lease_until<>OLD.lease_until) THEN RAISE EXCEPTION 'immutable lease qualification';END IF;
  ELSE RAISE EXCEPTION 'invalid recovery generation';END IF;
 END IF;
 IF NEW.owner_id IS NOT NULL AND (NEW.lease_until<=clock_timestamp() OR NEW.lease_until>clock_timestamp()+interval '31 seconds') THEN RAISE EXCEPTION 'invalid bounded lease';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER provision_recovery_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.provision_recovery FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.provision_recovery_guard();
ALTER TABLE mcpguard_ra.provisioning_jobs DROP CONSTRAINT provisioning_jobs_state_check;
ALTER TABLE mcpguard_ra.provisioning_jobs ADD CHECK(state IN ('EXECUTING','VERIFIED','OUTCOME_UNKNOWN','FAILED','CLEANUP_PENDING','CLEANED','MANUAL_REQUIRED'));
CREATE OR REPLACE FUNCTION mcpguard_ra.provision_job_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN IF NEW.state<>'EXECUTING' THEN RAISE EXCEPTION 'invalid provisioning intent';END IF;RETURN NEW;END IF;
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['state','failure_code']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','failure_code']) THEN RAISE EXCEPTION 'immutable provisioning qualification';END IF;
 PERFORM mcpguard_ra.assert_provision_lease(OLD.org_id,OLD.job_id);
 IF OLD.state IN ('VERIFIED','FAILED','CLEANED','MANUAL_REQUIRED') THEN RAISE EXCEPTION 'terminal provisioning result';END IF;
 IF NEW.state='EXECUTING' OR (OLD.state='CLEANUP_PENDING' AND NEW.state NOT IN ('CLEANUP_PENDING','CLEANED','MANUAL_REQUIRED')) THEN RAISE EXCEPTION 'new dispatch is forbidden';END IF;
 IF NEW.state='CLEANED' AND NOT EXISTS(SELECT FROM mcpguard_ra.provision_recovery l WHERE l.org_id=OLD.org_id AND l.job_id=OLD.job_id AND l.operation='CLEANUP' AND l.native_receipt->>'status' IN ('CLEANED','ABSENT_SEALED') AND l.native_receipt->>'planHash'=OLD.plan_hash AND l.native_receipt->>'accountId'=OLD.account_id::text) THEN RAISE EXCEPTION 'cleanup requires native proof';END IF;
 IF OLD.state='OUTCOME_UNKNOWN' AND NEW.state='VERIFIED' AND NOT EXISTS(SELECT FROM mcpguard_ra.provision_recovery l WHERE l.org_id=OLD.org_id AND l.job_id=OLD.job_id AND l.operation='RECONCILE' AND l.native_receipt->>'status'='OWNED' AND l.native_receipt->>'planHash'=OLD.plan_hash) THEN RAISE EXCEPTION 'unknown result requires matching native proof';END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION mcpguard_ra.grant_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE activation_job uuid;
BEGIN
 IF TG_OP='INSERT' THEN IF NEW.state<>'PROVISIONING' OR NEW.revision<>1 OR NEW.version<>1 THEN RAISE EXCEPTION 'unverified grant cannot activate';END IF;RETURN NEW;END IF;
 IF TG_OP='DELETE' OR NEW.revision<>OLD.revision+1 OR NEW.auth_epoch<=OLD.auth_epoch OR (NEW.org_id,NEW.grant_id) IS DISTINCT FROM (OLD.org_id,OLD.grant_id) THEN RAISE EXCEPTION 'invalid grant transition';END IF;
 IF NEW.state='ACTIVE' THEN
  IF OLD.state NOT IN ('PROVISIONING','PROVISIONING_UNKNOWN') OR OLD.version<>1 OR NEW.version<>2 THEN RAISE EXCEPTION 'terminal grant cannot revive';END IF;
  SELECT v.activation_job_id INTO activation_job FROM mcpguard_ra.access_grant_versions v JOIN mcpguard_ra.provisioning_jobs j ON (j.org_id,j.job_id)=(v.org_id,v.activation_job_id) WHERE v.org_id=NEW.org_id AND v.grant_id=NEW.grant_id AND v.version=2 AND j.state='VERIFIED' AND v.expires_at>clock_timestamp();
  IF activation_job IS NULL THEN RAISE EXCEPTION 'unverified grant cannot activate';END IF;
  PERFORM mcpguard_ra.assert_provision_lease(NEW.org_id,activation_job);
  IF OLD.state='PROVISIONING_UNKNOWN' AND NOT EXISTS(SELECT FROM mcpguard_ra.provision_recovery l WHERE l.org_id=NEW.org_id AND l.job_id=activation_job AND l.operation='RECONCILE' AND l.native_receipt->>'status'='OWNED') THEN RAISE EXCEPTION 'unknown grant requires reconciliation';END IF;
 ELSE
  IF NEW.version<>OLD.version OR OLD.state NOT IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN') OR NEW.state NOT IN ('REVOKED','EXPIRED','PROVISIONING_UNKNOWN','PROVISIONING_FAILED') OR (OLD.state<>'PROVISIONING' AND NEW.state NOT IN ('REVOKED','EXPIRED','PROVISIONING_FAILED')) THEN RAISE EXCEPTION 'terminal grant cannot revive';END IF;
 END IF;RETURN NEW;
END $$;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;
REVOKE ALL ON mcpguard_ra.provision_recovery,mcpguard_ra.provision_attempt_events FROM PUBLIC;
