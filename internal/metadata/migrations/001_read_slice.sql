-- Runtime migration for the local read slice, distinct from docs/specs/metadata-model.sql.
CREATE SCHEMA mcpguard;
CREATE TABLE mcpguard.schema_versions(version integer PRIMARY KEY, checksum text NOT NULL);
CREATE TABLE mcpguard.authorization_heads (
  org_id uuid PRIMARY KEY, auth_epoch bigint NOT NULL DEFAULT 1 CHECK(auth_epoch>0)
);
CREATE TABLE mcpguard.principals (
  org_id uuid NOT NULL REFERENCES mcpguard.authorization_heads,
  id uuid NOT NULL, display_name text NOT NULL,
  token_hash bytea NOT NULL UNIQUE CHECK(octet_length(token_hash)=32),
  expires_at timestamptz NOT NULL, enabled boolean NOT NULL DEFAULT true,
  can_read boolean NOT NULL DEFAULT false,
  scope_id text NOT NULL CHECK(scope_id ~ '^[a-z][a-z0-9_]{0,31}$'),
  PRIMARY KEY(org_id,id)
);
CREATE FUNCTION mcpguard.bump_auth_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND (NEW.org_id,NEW.id) IS DISTINCT FROM (OLD.org_id,OLD.id) THEN
    RAISE EXCEPTION 'principal identity is immutable';
  END IF;
  UPDATE mcpguard.authorization_heads SET auth_epoch=auth_epoch+1 WHERE org_id=COALESCE(NEW.org_id,OLD.org_id);
  RETURN COALESCE(NEW,OLD);
END $$;
CREATE TRIGGER principal_epoch BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.principals
  FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_auth_epoch();
CREATE TABLE mcpguard.actions (
  org_id uuid NOT NULL, id uuid NOT NULL, principal_id uuid NOT NULL,
  tool_name text NOT NULL CHECK(tool_name='pg.get_demo_item'),
  tool_version text NOT NULL CHECK(tool_version='1'),
  scope_id text NOT NULL, auth_epoch bigint NOT NULL,
  plan_hash text NOT NULL CHECK(length(plan_hash)=43),
  plan_ciphertext bytea NOT NULL, key_version text NOT NULL CHECK(key_version='local-v1'),
  authorization_source text NOT NULL CHECK(authorization_source='POLICY'),
  state text NOT NULL CHECK(state IN ('EXECUTING','SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY(org_id,id), UNIQUE(org_id,id,principal_id), FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE INDEX actions_owner ON mcpguard.actions(org_id,principal_id,created_at DESC,id);
CREATE TABLE mcpguard.attempts (
  org_id uuid NOT NULL, id uuid NOT NULL, action_id uuid NOT NULL,
  profile text NOT NULL CHECK(profile='2025-11-25'),
  state text NOT NULL CHECK(state IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')),
  PRIMARY KEY(org_id,id), UNIQUE(org_id,id,action_id),
  FOREIGN KEY(org_id,action_id) REFERENCES mcpguard.actions(org_id,id)
);
CREATE TABLE mcpguard.executions (
  org_id uuid NOT NULL, id uuid NOT NULL, action_id uuid NOT NULL, attempt_id uuid NOT NULL,
  owner_instance uuid NOT NULL,
  hard_deadline timestamptz NOT NULL,
  dispatch_state text NOT NULL CHECK(dispatch_state IN ('NOT_DISPATCHED','INTENT_RECORDED','DISPATCHED','DISPATCH_UNKNOWN')),
  request_status text NOT NULL CHECK(request_status IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')),
  side_effect_status text NOT NULL DEFAULT 'NONE' CHECK(side_effect_status='NONE'),
  result_ciphertext bytea,
  PRIMARY KEY(org_id,id), UNIQUE(org_id,action_id), UNIQUE(org_id,id,action_id,attempt_id),
  FOREIGN KEY(org_id,attempt_id,action_id) REFERENCES mcpguard.attempts(org_id,id,action_id),
  CHECK ((request_status='RUNNING') = (result_ciphertext IS NULL)),
  CHECK ((request_status='RUNNING') = (dispatch_state='INTENT_RECORDED')),
  CHECK (dispatch_state<>'DISPATCH_UNKNOWN' OR request_status='OUTCOME_UNKNOWN'),
  CHECK (request_status<>'SUCCEEDED' OR dispatch_state='DISPATCHED')
);
CREATE INDEX executions_pending ON mcpguard.executions(hard_deadline) WHERE request_status='RUNNING';
CREATE TABLE mcpguard.audit_events (
  seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  id uuid NOT NULL UNIQUE, org_id uuid NOT NULL, principal_id uuid NOT NULL,
  action_id uuid, attempt_id uuid, execution_id uuid,
  event_type text NOT NULL CHECK(event_type IN ('EXECUTION_INTENT','ACCESS_DENIED','INPUT_REJECTED','SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
  FOREIGN KEY(org_id,action_id,principal_id) REFERENCES mcpguard.actions(org_id,id,principal_id),
  FOREIGN KEY(org_id,execution_id,action_id,attempt_id) REFERENCES mcpguard.executions(org_id,id,action_id,attempt_id),
  CHECK ((event_type IN ('ACCESS_DENIED','INPUT_REJECTED') AND action_id IS NULL AND attempt_id IS NULL AND execution_id IS NULL)
      OR (event_type NOT IN ('ACCESS_DENIED','INPUT_REJECTED') AND action_id IS NOT NULL AND attempt_id IS NOT NULL AND execution_id IS NOT NULL))
);
CREATE INDEX audit_owner ON mcpguard.audit_events(org_id,principal_id,seq DESC);
CREATE FUNCTION mcpguard.immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'record is immutable'; END $$;
CREATE TRIGGER immutable_audit BEFORE UPDATE OR DELETE ON mcpguard.audit_events FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard.action_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'permanent action'; END IF;
  IF (to_jsonb(NEW)-'state') IS DISTINCT FROM (to_jsonb(OLD)-'state') OR OLD.state<>'EXECUTING'
     OR NEW.state='EXECUTING' THEN RAISE EXCEPTION 'immutable plan or terminal action'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER action_guard BEFORE UPDATE OR DELETE ON mcpguard.actions FOR EACH ROW EXECUTE FUNCTION mcpguard.action_guard();
CREATE FUNCTION mcpguard.execution_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'permanent execution qualification'; END IF;
  IF (to_jsonb(NEW)-ARRAY['dispatch_state','request_status','result_ciphertext']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['dispatch_state','request_status','result_ciphertext']) OR OLD.request_status<>'RUNNING'
     OR (OLD.dispatch_state='DISPATCHED' AND NEW.dispatch_state<>'DISPATCHED')
     OR (OLD.dispatch_state='DISPATCH_UNKNOWN' AND NEW.dispatch_state<>'DISPATCH_UNKNOWN')
     THEN RAISE EXCEPTION 'immutable qualification or terminal execution'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER execution_guard BEFORE UPDATE OR DELETE ON mcpguard.executions FOR EACH ROW EXECUTE FUNCTION mcpguard.execution_guard();
CREATE FUNCTION mcpguard.attempt_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'permanent attempt'; END IF;
  IF (to_jsonb(NEW)-'state') IS DISTINCT FROM (to_jsonb(OLD)-'state') OR OLD.state<>'RUNNING' OR NEW.state='RUNNING'
    THEN RAISE EXCEPTION 'immutable or terminal attempt'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER attempt_guard BEFORE UPDATE OR DELETE ON mcpguard.attempts FOR EACH ROW EXECUTE FUNCTION mcpguard.attempt_guard();
CREATE FUNCTION mcpguard.consistent_execution() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a mcpguard.actions; e mcpguard.executions; t mcpguard.attempts; aid uuid;
BEGIN
  IF TG_TABLE_NAME='actions' THEN aid=NEW.id; ELSE aid=NEW.action_id; END IF;
  SELECT * INTO a FROM mcpguard.actions WHERE org_id=NEW.org_id AND id=aid;
  SELECT * INTO e FROM mcpguard.executions WHERE org_id=NEW.org_id AND action_id=aid;
  SELECT * INTO t FROM mcpguard.attempts WHERE org_id=e.org_id AND id=e.attempt_id;
  IF a.id IS NULL OR e.id IS NULL OR t.id IS NULL OR a.state<>(CASE WHEN e.request_status='RUNNING' THEN 'EXECUTING' ELSE e.request_status END) OR t.state<>e.request_status
     THEN RAISE EXCEPTION 'inconsistent action/attempt/execution'; END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER consistent_execution AFTER INSERT OR UPDATE ON mcpguard.executions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mcpguard.consistent_execution();
CREATE CONSTRAINT TRIGGER consistent_action AFTER INSERT OR UPDATE ON mcpguard.actions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mcpguard.consistent_execution();
CREATE CONSTRAINT TRIGGER consistent_attempt AFTER INSERT OR UPDATE ON mcpguard.attempts
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mcpguard.consistent_execution();
