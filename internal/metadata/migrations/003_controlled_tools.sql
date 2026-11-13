-- The original read ledger remains sealed. Controlled actions use a separate
-- ledger so pending approval never weakens its deferred execution constraints.
CREATE TABLE mcpguard.control_heads (
  org_id uuid PRIMARY KEY REFERENCES mcpguard.authorization_heads,
  revision bigint NOT NULL DEFAULT 1 CHECK(revision>0)
);
INSERT INTO mcpguard.control_heads(org_id) SELECT org_id FROM mcpguard.authorization_heads;
CREATE FUNCTION mcpguard.lock_auth_epoch_exclusive(target_org uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE epoch bigint;
BEGIN
 SELECT h.auth_epoch INTO STRICT epoch FROM mcpguard.authorization_heads h WHERE h.org_id=target_org FOR UPDATE;
 RETURN epoch;
END $$;
REVOKE ALL ON FUNCTION mcpguard.lock_auth_epoch_exclusive(uuid) FROM PUBLIC;
ALTER FUNCTION mcpguard.bump_auth_epoch() SECURITY DEFINER;
ALTER FUNCTION mcpguard.bump_auth_epoch() SET search_path=pg_catalog;
CREATE TABLE mcpguard.control_members (
  org_id uuid NOT NULL, principal_id uuid NOT NULL, is_admin boolean NOT NULL DEFAULT false,
  PRIMARY KEY(org_id,principal_id), FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard.control_resources (
  org_id uuid NOT NULL REFERENCES mcpguard.control_heads, name text NOT NULL CHECK(name IN ('demo.pg','demo.redis','demo.directory','demo.inventory')),
  id uuid NOT NULL, enabled boolean NOT NULL DEFAULT false,
  PRIMARY KEY(org_id,name), UNIQUE(org_id,id)
);
CREATE TABLE mcpguard.tool_grants (
  org_id uuid NOT NULL, principal_id uuid NOT NULL, tool_name text NOT NULL,
  allowed boolean NOT NULL DEFAULT false, PRIMARY KEY(org_id,principal_id,tool_name),
  FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard.reviewer_grants (
  org_id uuid NOT NULL, principal_id uuid NOT NULL, resource_name text NOT NULL,
  allowed boolean NOT NULL DEFAULT false, PRIMARY KEY(org_id,principal_id,resource_name),
  FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
  FOREIGN KEY(org_id,resource_name) REFERENCES mcpguard.control_resources(org_id,name)
);
CREATE TABLE mcpguard.control_rules (
  org_id uuid NOT NULL REFERENCES mcpguard.control_heads, id uuid NOT NULL, tool_name text NOT NULL,
  effect text NOT NULL CHECK(effect IN ('DENY','REQUIRE_APPROVAL','ALLOW')),
  pattern text NOT NULL DEFAULT '' CHECK(length(pattern)<=512), enabled boolean NOT NULL DEFAULT true,
  PRIMARY KEY(org_id,id)
);
CREATE FUNCTION mcpguard.bump_control_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE mcpguard.control_heads SET revision=revision+1 WHERE org_id=COALESCE(NEW.org_id,OLD.org_id);
  RETURN COALESCE(NEW,OLD);
END $$;
CREATE TRIGGER control_members_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.control_members FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER control_resources_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.control_resources FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER tool_grants_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.tool_grants FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER reviewer_grants_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.reviewer_grants FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER control_rules_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.control_rules FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TABLE mcpguard.managed_actions (
  org_id uuid NOT NULL, id uuid NOT NULL, principal_id uuid NOT NULL, attempt_id uuid NOT NULL,
  tool_name text NOT NULL, resource_name text NOT NULL, scope_id text NOT NULL,
  config_revision bigint NOT NULL, plan_hash text NOT NULL CHECK(length(plan_hash)=43), plan_ciphertext bytea NOT NULL,
  binding_hash text NOT NULL, requires_approval boolean NOT NULL,
  state text NOT NULL CHECK(state IN ('PENDING_APPROVAL','READY','DENIED','EXPIRED','CANCELLED','EXECUTING','SUCCEEDED','FAILED','OUTCOME_UNKNOWN')),
  revision bigint NOT NULL DEFAULT 1, cancel_requested boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), expires_at timestamptz NOT NULL,
  PRIMARY KEY(org_id,id), UNIQUE(org_id,id,principal_id),
  FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
  FOREIGN KEY(org_id,resource_name) REFERENCES mcpguard.control_resources(org_id,name)
);
CREATE INDEX managed_owner ON mcpguard.managed_actions(org_id,principal_id,created_at DESC,id);
CREATE TABLE mcpguard.managed_decisions (
  org_id uuid NOT NULL, action_id uuid NOT NULL, reviewer_id uuid NOT NULL, vote text NOT NULL CHECK(vote IN ('APPROVE','REJECT')),
  plan_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY(org_id,action_id), FOREIGN KEY(org_id,action_id) REFERENCES mcpguard.managed_actions(org_id,id),
  FOREIGN KEY(org_id,reviewer_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard.managed_executions (
  org_id uuid NOT NULL, id uuid NOT NULL, action_id uuid NOT NULL, owner_instance uuid NOT NULL,
  hard_deadline timestamptz NOT NULL, request_status text NOT NULL CHECK(request_status IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')),
  dispatch_state text NOT NULL CHECK(dispatch_state IN ('INTENT_RECORDED','NOT_DISPATCHED','DISPATCHED','DISPATCH_UNKNOWN')),
  side_effect_status text NOT NULL CHECK(side_effect_status IN ('NONE','APPLIED','UNKNOWN')),
  result_ciphertext bytea, PRIMARY KEY(org_id,id), UNIQUE(org_id,action_id),
  FOREIGN KEY(org_id,action_id) REFERENCES mcpguard.managed_actions(org_id,id),
  CHECK ((request_status='RUNNING')=(result_ciphertext IS NULL)),
  CHECK ((request_status='RUNNING')=(dispatch_state='INTENT_RECORDED')),
  CHECK (side_effect_status<>'APPLIED' OR (request_status='SUCCEEDED' AND dispatch_state='DISPATCHED')),
  CHECK (side_effect_status<>'UNKNOWN' OR request_status='OUTCOME_UNKNOWN'),
  CHECK (request_status<>'SUCCEEDED' OR dispatch_state='DISPATCHED')
);
CREATE INDEX managed_running ON mcpguard.managed_executions(hard_deadline) WHERE request_status='RUNNING';
CREATE TABLE mcpguard.managed_audit (
  seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
  id uuid NOT NULL UNIQUE, org_id uuid NOT NULL, principal_id uuid NOT NULL, action_id uuid, attempt_id uuid, execution_id uuid,
  event_type text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
  FOREIGN KEY(org_id,action_id) REFERENCES mcpguard.managed_actions(org_id,id)
);
CREATE INDEX managed_audit_owner ON mcpguard.managed_audit(org_id,principal_id,seq);
CREATE TRIGGER managed_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard.managed_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER managed_decision_immutable BEFORE UPDATE OR DELETE ON mcpguard.managed_decisions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard.managed_decision_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a mcpguard.managed_actions;
BEGIN
 SELECT * INTO STRICT a FROM mcpguard.managed_actions WHERE org_id=NEW.org_id AND id=NEW.action_id;
 IF a.state<>'PENDING_APPROVAL' OR a.principal_id=NEW.reviewer_id OR a.plan_hash<>NEW.plan_hash OR a.expires_at<=clock_timestamp()
 THEN RAISE EXCEPTION 'invalid approval binding'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_decision_guard BEFORE INSERT ON mcpguard.managed_decisions FOR EACH ROW EXECUTE FUNCTION mcpguard.managed_decision_guard();
CREATE FUNCTION mcpguard.managed_action_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'permanent action'; END IF;
  IF (to_jsonb(NEW)-ARRAY['state','revision','cancel_requested']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','cancel_requested'])
     OR NEW.revision<>OLD.revision+1 OR NOT (
       (OLD.state='PENDING_APPROVAL' AND NEW.state IN ('READY','DENIED','EXPIRED','CANCELLED')) OR
       (OLD.state='READY' AND NEW.state IN ('EXECUTING','EXPIRED','CANCELLED')) OR
       (OLD.state='EXECUTING' AND NEW.state IN ('SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')) OR
       (OLD.state='EXECUTING' AND NEW.state='EXECUTING' AND NOT OLD.cancel_requested AND NEW.cancel_requested))
    THEN RAISE EXCEPTION 'immutable plan or invalid state transition'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER managed_action_guard BEFORE UPDATE OR DELETE ON mcpguard.managed_actions FOR EACH ROW EXECUTE FUNCTION mcpguard.managed_action_guard();
CREATE FUNCTION mcpguard.managed_execution_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'permanent execution qualification'; END IF;
  IF (to_jsonb(NEW)-ARRAY['request_status','dispatch_state','side_effect_status','result_ciphertext']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['request_status','dispatch_state','side_effect_status','result_ciphertext'])
     OR OLD.request_status<>'RUNNING' OR NEW.request_status='RUNNING' THEN RAISE EXCEPTION 'immutable execution'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER managed_execution_guard BEFORE UPDATE OR DELETE ON mcpguard.managed_executions FOR EACH ROW EXECUTE FUNCTION mcpguard.managed_execution_guard();
CREATE FUNCTION mcpguard.consistent_managed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a mcpguard.managed_actions; e mcpguard.managed_executions; d mcpguard.managed_decisions; aid uuid;
BEGIN
  IF TG_TABLE_NAME='managed_actions' THEN aid=NEW.id; ELSE aid=NEW.action_id; END IF;
  SELECT * INTO a FROM mcpguard.managed_actions WHERE org_id=NEW.org_id AND id=aid;
  SELECT * INTO e FROM mcpguard.managed_executions WHERE org_id=NEW.org_id AND action_id=aid;
  SELECT * INTO d FROM mcpguard.managed_decisions WHERE org_id=NEW.org_id AND action_id=aid;
  IF (a.state IN ('EXECUTING','SUCCEEDED','FAILED','OUTCOME_UNKNOWN') AND e.id IS NULL)
     OR (e.id IS NOT NULL AND a.state<>CASE WHEN e.request_status='RUNNING' THEN 'EXECUTING' ELSE e.request_status END)
     OR (a.requires_approval AND a.state IN ('READY','EXECUTING','SUCCEEDED','FAILED','OUTCOME_UNKNOWN') AND
        (d.action_id IS NULL OR d.vote<>'APPROVE' OR d.reviewer_id=a.principal_id OR d.plan_hash<>a.plan_hash))
    THEN RAISE EXCEPTION 'inconsistent managed approval/execution'; END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER managed_consistency AFTER INSERT OR UPDATE ON mcpguard.managed_actions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mcpguard.consistent_managed();
CREATE CONSTRAINT TRIGGER managed_execution_consistency AFTER INSERT OR UPDATE ON mcpguard.managed_executions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mcpguard.consistent_managed();
CREATE TABLE mcpguard.control_operations (
  org_id uuid NOT NULL, actor_id uuid NOT NULL, route text NOT NULL, key text NOT NULL, payload_hash text NOT NULL,
  response jsonb NOT NULL, PRIMARY KEY(org_id,actor_id,route,key)
);
CREATE TRIGGER control_operations_immutable BEFORE UPDATE OR DELETE ON mcpguard.control_operations FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TABLE mcpguard.notification_outbox (
  org_id uuid NOT NULL, id uuid NOT NULL, action_id uuid NOT NULL, state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','SENT','FAILED','SUPERSEDED')),
  attempts integer NOT NULL DEFAULT 0, next_at timestamptz NOT NULL DEFAULT clock_timestamp(), lease_until timestamptz,
  PRIMARY KEY(org_id,id), UNIQUE(org_id,action_id), FOREIGN KEY(org_id,action_id) REFERENCES mcpguard.managed_actions(org_id,id)
);
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard FROM PUBLIC;
