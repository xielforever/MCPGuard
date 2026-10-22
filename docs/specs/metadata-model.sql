-- MCPGuard metadata model proposal, 2026-10-22.
-- PostgreSQL 16+; empty isolated database/schema only. Not a production migration.
-- This file is not executed by the application or documentation validator.
-- UUIDs, normalized hashes and encrypted payloads are supplied by the application.
-- No extension, global role, password, native-service connection or network call is created.
BEGIN;
CREATE SCHEMA mcpguard_model;
REVOKE ALL ON SCHEMA mcpguard_model FROM PUBLIC;
SET LOCAL search_path = mcpguard_model, pg_catalog;

CREATE TABLE authorization_heads (
    org_id uuid PRIMARY KEY,
    auth_epoch bigint NOT NULL CHECK (auth_epoch > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE principals (
    org_id uuid NOT NULL REFERENCES authorization_heads(org_id),
    id uuid NOT NULL,
    issuer text NOT NULL CHECK (issuer <> ''),
    subject text NOT NULL CHECK (subject <> ''),
    enabled boolean NOT NULL,
    identity_version bigint NOT NULL CHECK (identity_version > 0),
    group_ids text[] NOT NULL DEFAULT '{}',
    role_ids text[] NOT NULL DEFAULT '{}',
    observed_at timestamptz NOT NULL,
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, issuer, subject)
);
-- definition may contain endpoint/credential references, never plaintext secrets.
CREATE TABLE catalog_revisions (
    org_id uuid NOT NULL REFERENCES authorization_heads(org_id),
    kind text NOT NULL CHECK (kind IN ('RESOURCE','TOOL','POLICY','CREDENTIAL_BINDING')),
    object_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (org_id, kind, object_id, version),
    FOREIGN KEY (org_id, created_by) REFERENCES principals(org_id, id)
);
CREATE TABLE catalog_heads (
    org_id uuid NOT NULL REFERENCES authorization_heads(org_id),
    kind text NOT NULL CHECK (kind IN ('RESOURCE','TOOL','POLICY','CREDENTIAL_BINDING')),
    object_id uuid NOT NULL,
    version bigint CHECK (version > 0),
    enabled boolean NOT NULL,
    row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (org_id, kind, object_id),
    FOREIGN KEY (org_id, kind, object_id, version)
        REFERENCES catalog_revisions(org_id, kind, object_id, version),
    CHECK (kind IN ('TOOL','POLICY') OR version IS NOT NULL),
    CHECK (NOT enabled OR version IS NOT NULL)
);
CREATE TABLE protected_payloads (
    org_id uuid NOT NULL REFERENCES authorization_heads(org_id),
    id uuid NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('PLAN','RESULT','EVIDENCE','AUDIT_DETAILS','COMMAND_RESPONSE')),
    ciphertext bytea,
    key_ref text NOT NULL CHECK (key_ref <> ''),
    key_version text NOT NULL CHECK (key_version <> ''),
    encoding_version integer NOT NULL CHECK (encoding_version > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    purged_at timestamptz,
    PRIMARY KEY (org_id, id),
    CHECK ((purged_at IS NULL AND ciphertext IS NOT NULL AND octet_length(ciphertext) > 0)
        OR (purged_at IS NOT NULL AND ciphertext IS NULL)),
    CHECK (expires_at IS NULL OR expires_at > created_at),
    CHECK (purged_at IS NULL OR purged_at >= created_at)
);
CREATE TABLE actions (
    org_id uuid NOT NULL,
    id uuid NOT NULL,
    principal_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('PENDING_APPROVAL','READY','EXECUTING','SUCCEEDED','FAILED','DENIED','CANCELLED','EXPIRED','OUTCOME_UNKNOWN')),
    row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
    interaction_mode text NOT NULL CHECK (interaction_mode IN ('BOUNDED_WAIT','CONTROL_FLOW','MRTR')),
    authorization_source text CHECK (authorization_source IN ('POLICY','HUMAN')),
    basis_epoch bigint NOT NULL CHECK (basis_epoch > 0),
    plan_hash bytea NOT NULL CHECK (octet_length(plan_hash) = 32),
    hash_key_version integer NOT NULL CHECK (hash_key_version > 0),
    plan_ref uuid NOT NULL,
    plan_summary jsonb NOT NULL CHECK (jsonb_typeof(plan_summary) = 'object'),
    approval_requirement jsonb NOT NULL CHECK (jsonb_typeof(approval_requirement) = 'object'),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    cancel_requested_at timestamptz,
    terminal_reason text,
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, id, plan_hash),
    FOREIGN KEY (org_id, principal_id) REFERENCES principals(org_id, id),
    FOREIGN KEY (org_id, plan_ref) REFERENCES protected_payloads(org_id, id),
    CHECK (expires_at > created_at),
    CHECK (state NOT IN ('READY','EXECUTING','SUCCEEDED','OUTCOME_UNKNOWN') OR authorization_source IS NOT NULL)
);
CREATE TABLE attempts (
    org_id uuid NOT NULL,
    id uuid NOT NULL,
    action_id uuid NOT NULL,
    client_scope text NOT NULL CHECK (client_scope <> ''),
    rpc_id jsonb NOT NULL,
    profile text NOT NULL CHECK (profile <> ''),
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    outcome text,
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, id, action_id),
    FOREIGN KEY (org_id, action_id) REFERENCES actions(org_id, id),
    CHECK (jsonb_typeof(rpc_id) = 'string'
        OR (jsonb_typeof(rpc_id) = 'number' AND rpc_id::text ~ '^-?[0-9]+$')),
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);
CREATE TABLE decisions (
    org_id uuid NOT NULL,
    id uuid NOT NULL,
    action_id uuid NOT NULL,
    plan_hash bytea NOT NULL CHECK (octet_length(plan_hash) = 32),
    approver_id uuid NOT NULL,
    decision text NOT NULL CHECK (decision IN ('APPROVE','REJECT')),
    source text NOT NULL CHECK (source <> ''),
    source_event_id text NOT NULL CHECK (source_event_id <> ''),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    decided_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    reason_ref uuid,
    approval_context_ref uuid,
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, source, source_event_id),
    FOREIGN KEY (org_id, action_id, plan_hash) REFERENCES actions(org_id, id, plan_hash),
    FOREIGN KEY (org_id, approver_id) REFERENCES principals(org_id, id),
    FOREIGN KEY (org_id, reason_ref) REFERENCES protected_payloads(org_id, id),
    FOREIGN KEY (org_id, approval_context_ref) REFERENCES protected_payloads(org_id, id),
    CHECK (expires_at > decided_at)
);
-- Presence is a consumed dispatch qualification, not proof of a native send.
CREATE TABLE executions (
    org_id uuid NOT NULL,
    id uuid NOT NULL,
    action_id uuid NOT NULL,
    plan_hash bytea NOT NULL CHECK (octet_length(plan_hash) = 32),
    owner_instance text NOT NULL CHECK (owner_instance <> ''),
    claim_token uuid NOT NULL,
    lease_until timestamptz NOT NULL,
    hard_deadline timestamptz NOT NULL,
    recovery_after timestamptz NOT NULL,
    dispatch_state text NOT NULL CHECK (dispatch_state IN ('NOT_DISPATCHED','INTENT_RECORDED','DISPATCHED','DISPATCH_UNKNOWN')),
    request_status text NOT NULL CHECK (request_status IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')),
    side_effect_status text NOT NULL CHECK (side_effect_status IN ('NONE','APPLIED','ROLLED_BACK','PARTIAL','UNKNOWN')),
    row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
    result_ref uuid,
    evidence_ref uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ended_at timestamptz,
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, action_id),
    UNIQUE (org_id, id, action_id),
    FOREIGN KEY (org_id, action_id, plan_hash) REFERENCES actions(org_id, id, plan_hash),
    FOREIGN KEY (org_id, result_ref) REFERENCES protected_payloads(org_id, id),
    FOREIGN KEY (org_id, evidence_ref) REFERENCES protected_payloads(org_id, id),
    CHECK (recovery_after >= hard_deadline),
    CHECK (lease_until <= recovery_after),
    CHECK (ended_at IS NULL OR ended_at >= created_at),
    CHECK (dispatch_state <> 'NOT_DISPATCHED' OR side_effect_status = 'NONE'),
    CHECK (dispatch_state <> 'DISPATCH_UNKNOWN' OR request_status = 'OUTCOME_UNKNOWN'),
    CHECK (dispatch_state <> 'INTENT_RECORDED' OR request_status = 'RUNNING'),
    CHECK (request_status <> 'SUCCEEDED' OR dispatch_state = 'DISPATCHED'),
    CHECK (request_status <> 'OUTCOME_UNKNOWN' OR dispatch_state IN ('DISPATCHED','DISPATCH_UNKNOWN')),
    CHECK (side_effect_status NOT IN ('APPLIED','ROLLED_BACK','PARTIAL') OR dispatch_state = 'DISPATCHED'),
    CHECK (request_status <> 'RUNNING' OR ended_at IS NULL),
    CHECK (dispatch_state <> 'NOT_DISPATCHED' OR request_status IN ('FAILED','CANCELLED'))
);
-- safe_summary contains redacted facts; details are encrypted references.
CREATE TABLE audit_events (
    org_id uuid NOT NULL REFERENCES authorization_heads(org_id),
    id uuid NOT NULL,
    actor_id uuid,
    action_id uuid,
    attempt_id uuid,
    execution_id uuid,
    plan_hash bytea,
    event_type text NOT NULL CHECK (event_type <> ''),
    safe_summary jsonb NOT NULL CHECK (jsonb_typeof(safe_summary) = 'object'),
    details_ref uuid,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (org_id, id),
    FOREIGN KEY (org_id, actor_id) REFERENCES principals(org_id, id),
    FOREIGN KEY (org_id, action_id, plan_hash) REFERENCES actions(org_id, id, plan_hash),
    FOREIGN KEY (org_id, attempt_id, action_id) REFERENCES attempts(org_id, id, action_id),
    FOREIGN KEY (org_id, execution_id, action_id) REFERENCES executions(org_id, id, action_id),
    FOREIGN KEY (org_id, details_ref) REFERENCES protected_payloads(org_id, id),
    CHECK (action_id IS NULL OR plan_hash IS NOT NULL),
    CHECK (attempt_id IS NULL OR action_id IS NOT NULL),
    CHECK (execution_id IS NULL OR action_id IS NOT NULL)
);
-- Only notification/status/export delivery; never native write jobs.
CREATE TABLE outbox (
    org_id uuid NOT NULL REFERENCES authorization_heads(org_id),
    id uuid NOT NULL,
    action_id uuid,
    recipient_id uuid,
    dedupe_key text NOT NULL CHECK (dedupe_key <> ''),
    event_type text NOT NULL CHECK (event_type IN ('APPROVAL_REQUESTED','ACTION_STATUS_CHANGED','AUDIT_EXPORT_READY')),
    safe_payload jsonb NOT NULL CHECK (jsonb_typeof(safe_payload) = 'object'),
    available_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until timestamptz,
    claim_token uuid,
    delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, dedupe_key),
    FOREIGN KEY (org_id, action_id) REFERENCES actions(org_id, id),
    FOREIGN KEY (org_id, recipient_id) REFERENCES principals(org_id, id),
    CHECK ((lease_until IS NULL) = (claim_token IS NULL))
);
CREATE TABLE idempotency_records (
    org_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    command_type text NOT NULL CHECK (command_type <> ''),
    target_key text NOT NULL,
    key_hash bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    hash_key_version integer NOT NULL CHECK (hash_key_version > 0),
    command_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('RUNNING','COMPLETED','FAILED')),
    response_status smallint CHECK (response_status BETWEEN 200 AND 599),
    response_ref uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (org_id, actor_id, command_type, target_key, key_hash),
    UNIQUE (org_id, command_id),
    FOREIGN KEY (org_id, actor_id) REFERENCES principals(org_id, id),
    FOREIGN KEY (org_id, response_ref) REFERENCES protected_payloads(org_id, id),
    CHECK (expires_at > created_at),
    CHECK (state = 'RUNNING' OR (response_status IS NOT NULL AND response_ref IS NOT NULL))
);

CREATE INDEX catalog_revisions_creator_idx ON catalog_revisions(org_id, created_by);
CREATE INDEX payload_expiry_idx ON protected_payloads(org_id, expires_at, id) WHERE purged_at IS NULL;
CREATE INDEX actions_actor_time_idx ON actions(org_id, principal_id, created_at DESC, id DESC);
CREATE INDEX actions_payload_idx ON actions(org_id, plan_ref);
CREATE INDEX actions_pending_expiry_idx ON actions(org_id, expires_at, id) WHERE state = 'PENDING_APPROVAL';
CREATE INDEX actions_ready_time_idx ON actions(org_id, created_at, id) WHERE state = 'READY';
CREATE INDEX attempts_action_time_idx ON attempts(org_id, action_id, started_at, id);
CREATE INDEX decisions_action_time_idx ON decisions(org_id, action_id, decided_at, id);
CREATE INDEX decisions_actor_idx ON decisions(org_id, approver_id);
CREATE INDEX decisions_reason_idx ON decisions(org_id, reason_ref) WHERE reason_ref IS NOT NULL;
CREATE INDEX decisions_context_idx ON decisions(org_id, approval_context_ref) WHERE approval_context_ref IS NOT NULL;
CREATE INDEX executions_lease_idx ON executions(lease_until, org_id, id) WHERE request_status = 'RUNNING';
CREATE INDEX executions_result_idx ON executions(org_id, result_ref) WHERE result_ref IS NOT NULL;
CREATE INDEX executions_evidence_idx ON executions(org_id, evidence_ref) WHERE evidence_ref IS NOT NULL;
CREATE INDEX audit_action_time_idx ON audit_events(org_id, action_id, at, id);
CREATE INDEX audit_actor_time_idx ON audit_events(org_id, actor_id, at, id);
CREATE INDEX audit_attempt_idx ON audit_events(org_id, attempt_id, action_id) WHERE attempt_id IS NOT NULL;
CREATE INDEX audit_execution_idx ON audit_events(org_id, execution_id, action_id) WHERE execution_id IS NOT NULL;
CREATE INDEX audit_details_idx ON audit_events(org_id, details_ref) WHERE details_ref IS NOT NULL;
CREATE INDEX outbox_due_idx ON outbox(available_at, org_id, id) WHERE delivered_at IS NULL;
CREATE INDEX outbox_action_idx ON outbox(org_id, action_id) WHERE action_id IS NOT NULL;
CREATE INDEX outbox_recipient_idx ON outbox(org_id, recipient_id) WHERE recipient_id IS NOT NULL;
CREATE INDEX idempotency_expiry_idx ON idempotency_records(org_id, expires_at) WHERE state <> 'RUNNING';
CREATE INDEX idempotency_response_idx ON idempotency_records(org_id, response_ref) WHERE response_ref IS NOT NULL;

CREATE FUNCTION reject_row_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    RAISE EXCEPTION 'immutable % row: % is forbidden', TG_TABLE_NAME, TG_OP USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER catalog_revision_immutable BEFORE UPDATE OR DELETE ON catalog_revisions
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();
CREATE TRIGGER decision_immutable BEFORE UPDATE OR DELETE ON decisions
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();
CREATE TRIGGER audit_append_only BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();
CREATE TRIGGER action_keep_identity BEFORE DELETE ON actions
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();
CREATE TRIGGER execution_keep_qualification BEFORE DELETE ON executions
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();
CREATE TRIGGER payload_keep_tombstone BEFORE DELETE ON protected_payloads
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();
CREATE TRIGGER catalog_head_keep_identity BEFORE DELETE ON catalog_heads
    FOR EACH ROW EXECUTE FUNCTION reject_row_change();

CREATE FUNCTION guard_action_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state NOT IN ('PENDING_APPROVAL','READY','DENIED')
           OR (NEW.state = 'READY' AND NEW.authorization_source IS DISTINCT FROM 'POLICY')
           OR (NEW.state = 'PENDING_APPROVAL' AND NEW.authorization_source IS NOT NULL) THEN
            RAISE EXCEPTION 'invalid initial action state/source' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF ROW(NEW.org_id,NEW.id,NEW.principal_id,NEW.interaction_mode,NEW.basis_epoch,NEW.plan_hash,
           NEW.hash_key_version,NEW.plan_ref,NEW.plan_summary,NEW.approval_requirement,NEW.expires_at,NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.org_id,OLD.id,OLD.principal_id,OLD.interaction_mode,OLD.basis_epoch,OLD.plan_hash,
           OLD.hash_key_version,OLD.plan_ref,OLD.plan_summary,OLD.approval_requirement,OLD.expires_at,OLD.created_at)
       OR NEW.row_version <> OLD.row_version + 1 THEN
        RAISE EXCEPTION 'action plan is immutable or revision not incremented' USING ERRCODE = '23514';
    END IF;
    IF OLD.state IN ('SUCCEEDED','FAILED','DENIED','CANCELLED','EXPIRED') THEN
        RAISE EXCEPTION 'terminal action is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.authorization_source IS NOT NULL AND NEW.authorization_source IS DISTINCT FROM OLD.authorization_source THEN
        RAISE EXCEPTION 'authorization source cannot be replaced' USING ERRCODE = '23514';
    END IF;
    IF OLD.authorization_source IS NULL AND NEW.authorization_source IS NOT NULL
       AND NOT (OLD.state = 'PENDING_APPROVAL' AND NEW.state = 'READY' AND NEW.authorization_source = 'HUMAN') THEN
        RAISE EXCEPTION 'authorization source must match the approval transition' USING ERRCODE = '23514';
    END IF;
    IF NEW.state <> OLD.state AND NOT (
        (OLD.state = 'PENDING_APPROVAL' AND NEW.state IN ('READY','DENIED','CANCELLED','EXPIRED'))
        OR (OLD.state = 'READY' AND NEW.state IN ('EXECUTING','DENIED','CANCELLED','EXPIRED'))
        OR (OLD.state = 'EXECUTING' AND NEW.state IN ('SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN'))
        OR (OLD.state = 'OUTCOME_UNKNOWN' AND NEW.state IN ('SUCCEEDED','FAILED','CANCELLED'))
    ) THEN
        RAISE EXCEPTION 'invalid action transition' USING ERRCODE = '23514';
    END IF;
    IF OLD.state = 'PENDING_APPROVAL' AND NEW.state = 'READY'
       AND NEW.authorization_source IS DISTINCT FROM 'HUMAN' THEN
        RAISE EXCEPTION 'approval path requires human authorization source' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER action_guard BEFORE INSERT OR UPDATE ON actions
    FOR EACH ROW EXECUTE FUNCTION guard_action_change();

CREATE FUNCTION guard_payload_purge() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    IF ROW(NEW.org_id,NEW.id,NEW.purpose,NEW.key_ref,NEW.key_version,NEW.encoding_version,NEW.created_at,NEW.expires_at)
       IS DISTINCT FROM
       ROW(OLD.org_id,OLD.id,OLD.purpose,OLD.key_ref,OLD.key_version,OLD.encoding_version,OLD.created_at,OLD.expires_at)
       OR OLD.purged_at IS NOT NULL OR NEW.purged_at IS NULL OR NEW.ciphertext IS NOT NULL THEN
        RAISE EXCEPTION 'payload may only transition once to a retained purge tombstone' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER payload_update_guard BEFORE UPDATE ON protected_payloads
    FOR EACH ROW EXECUTE FUNCTION guard_payload_purge();

CREATE FUNCTION guard_decision_actor() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
DECLARE requester uuid;
BEGIN
    SELECT a.principal_id INTO requester FROM mcpguard_model.actions a
        WHERE a.org_id = NEW.org_id AND a.id = NEW.action_id;
    IF requester = NEW.approver_id THEN
        RAISE EXCEPTION 'self approval/rejection is not an enterprise other-person decision' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER decision_actor_guard BEFORE INSERT ON decisions
    FOR EACH ROW EXECUTE FUNCTION guard_decision_actor();

CREATE FUNCTION guard_execution_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.dispatch_state <> 'INTENT_RECORDED' OR NEW.request_status <> 'RUNNING'
           OR NEW.side_effect_status <> 'NONE' THEN
            RAISE EXCEPTION 'execution starts only with a consumed intent' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF ROW(NEW.org_id,NEW.id,NEW.action_id,NEW.plan_hash,NEW.owner_instance,NEW.claim_token,
           NEW.hard_deadline,NEW.recovery_after,NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.org_id,OLD.id,OLD.action_id,OLD.plan_hash,OLD.owner_instance,OLD.claim_token,
           OLD.hard_deadline,OLD.recovery_after,OLD.created_at)
       OR NEW.row_version <> OLD.row_version + 1 THEN
        RAISE EXCEPTION 'execution identity/owner/deadline is immutable or revision not incremented' USING ERRCODE = '23514';
    END IF;
    IF OLD.request_status IN ('SUCCEEDED','FAILED','CANCELLED') AND OLD.side_effect_status <> 'UNKNOWN' THEN
        RAISE EXCEPTION 'known terminal execution is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.request_status IN ('SUCCEEDED','FAILED','CANCELLED') AND NEW.request_status <> OLD.request_status THEN
        RAISE EXCEPTION 'side-effect reconciliation cannot rewrite a known request outcome' USING ERRCODE = '23514';
    END IF;
    IF OLD.request_status = 'OUTCOME_UNKNOWN' AND NEW.request_status = 'RUNNING' THEN
        RAISE EXCEPTION 'unknown outcome never becomes runnable' USING ERRCODE = '23514';
    END IF;
    IF (OLD.dispatch_state = 'DISPATCHED' AND NEW.dispatch_state <> 'DISPATCHED')
       OR (OLD.dispatch_state = 'DISPATCH_UNKNOWN' AND NEW.dispatch_state NOT IN ('DISPATCH_UNKNOWN','DISPATCHED','NOT_DISPATCHED'))
       OR (OLD.dispatch_state = 'NOT_DISPATCHED' AND NEW.dispatch_state <> 'NOT_DISPATCHED') THEN
        RAISE EXCEPTION 'dispatch evidence cannot regress or reopen intent' USING ERRCODE = '23514';
    END IF;
    IF ((OLD.dispatch_state = 'DISPATCH_UNKNOWN' AND NEW.dispatch_state <> 'DISPATCH_UNKNOWN')
        OR (OLD.request_status = 'OUTCOME_UNKNOWN' AND NEW.request_status <> 'OUTCOME_UNKNOWN')
        OR (OLD.side_effect_status = 'UNKNOWN' AND NEW.side_effect_status <> 'UNKNOWN'))
       AND (NEW.evidence_ref IS NULL OR NEW.evidence_ref IS NOT DISTINCT FROM OLD.evidence_ref) THEN
        RAISE EXCEPTION 'unknown facts require a fresh non-null evidence reference to converge' USING ERRCODE = '23514';
    END IF;
    -- Application must verify authoritative proof of no send and isolation of the old sender.
    -- A new evidence row or an ordinary operator note is not, by itself, that proof.
    IF OLD.dispatch_state = 'DISPATCH_UNKNOWN' AND NEW.dispatch_state = 'NOT_DISPATCHED'
       AND (NEW.request_status NOT IN ('FAILED','CANCELLED') OR NEW.side_effect_status <> 'NONE') THEN
        RAISE EXCEPTION 'proven no-send recovery is terminal and never restores qualification' USING ERRCODE = '23514';
    END IF;
    IF OLD.request_status <> 'RUNNING' AND NEW.lease_until IS DISTINCT FROM OLD.lease_until THEN
        RAISE EXCEPTION 'non-running execution cannot renew a lease' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER execution_guard BEFORE INSERT OR UPDATE ON executions
    FOR EACH ROW EXECUTE FUNCTION guard_execution_change();

CREATE FUNCTION guard_catalog_head_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    IF ROW(NEW.org_id,NEW.kind,NEW.object_id) IS DISTINCT FROM ROW(OLD.org_id,OLD.kind,OLD.object_id)
       OR NEW.row_version <> OLD.row_version + 1 THEN
        RAISE EXCEPTION 'catalog identity is immutable or revision not incremented' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER catalog_head_guard BEFORE UPDATE ON catalog_heads
    FOR EACH ROW EXECUTE FUNCTION guard_catalog_head_change();

CREATE FUNCTION guard_idempotency_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.state = 'RUNNING' OR OLD.expires_at > clock_timestamp() THEN
            RAISE EXCEPTION 'unfinished/unexpired command deduplication cannot be deleted' USING ERRCODE = '23514';
        END IF;
        RETURN OLD;
    END IF;
    IF ROW(NEW.org_id,NEW.actor_id,NEW.command_type,NEW.target_key,NEW.key_hash,NEW.request_hash,
           NEW.hash_key_version,NEW.command_id,NEW.created_at,NEW.expires_at)
       IS DISTINCT FROM
       ROW(OLD.org_id,OLD.actor_id,OLD.command_type,OLD.target_key,OLD.key_hash,OLD.request_hash,
           OLD.hash_key_version,OLD.command_id,OLD.created_at,OLD.expires_at)
       OR OLD.state <> 'RUNNING' THEN
        RAISE EXCEPTION 'command identity and completed response are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER idempotency_guard BEFORE UPDATE OR DELETE ON idempotency_records
    FOR EACH ROW EXECUTE FUNCTION guard_idempotency_change();

CREATE FUNCTION guard_outbox_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
BEGIN
    IF ROW(NEW.org_id,NEW.id,NEW.action_id,NEW.recipient_id,NEW.dedupe_key,NEW.event_type,NEW.safe_payload,NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.org_id,OLD.id,OLD.action_id,OLD.recipient_id,OLD.dedupe_key,OLD.event_type,OLD.safe_payload,OLD.created_at)
       OR NEW.attempts < OLD.attempts OR OLD.delivered_at IS NOT NULL THEN
        RAISE EXCEPTION 'outbox identity/payload and delivered record are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER outbox_update_guard BEFORE UPDATE ON outbox
    FOR EACH ROW EXECUTE FUNCTION guard_outbox_change();

-- Deferred because the claim transaction updates Action before inserting Execution.
CREATE FUNCTION check_action_execution_pair() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, mcpguard_model AS $$
DECLARE
    scope_org uuid;
    scope_action uuid;
    action_state text;
    execution_status text;
BEGIN
    IF TG_TABLE_NAME = 'actions' THEN
        scope_org := NEW.org_id;
        scope_action := NEW.id;
    ELSE
        scope_org := NEW.org_id;
        scope_action := NEW.action_id;
    END IF;
    SELECT a.state INTO action_state FROM mcpguard_model.actions a
        WHERE a.org_id = scope_org AND a.id = scope_action;
    SELECT e.request_status INTO execution_status FROM mcpguard_model.executions e
        WHERE e.org_id = scope_org AND e.action_id = scope_action;
    IF action_state IN ('PENDING_APPROVAL','READY','DENIED','EXPIRED') AND execution_status IS NOT NULL THEN
        RAISE EXCEPTION 'unclaimed/denied action cannot have execution' USING ERRCODE = '23514';
    ELSIF action_state IN ('EXECUTING','SUCCEEDED','OUTCOME_UNKNOWN') AND execution_status IS NULL THEN
        RAISE EXCEPTION 'action requires consumed execution qualification' USING ERRCODE = '23514';
    ELSIF action_state = 'EXECUTING' AND execution_status <> 'RUNNING' THEN
        RAISE EXCEPTION 'executing action and request status disagree' USING ERRCODE = '23514';
    ELSIF action_state IN ('SUCCEEDED','FAILED','CANCELLED','OUTCOME_UNKNOWN')
          AND execution_status IS NOT NULL AND execution_status <> action_state THEN
        RAISE EXCEPTION 'terminal action and execution status disagree' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER action_execution_consistency
    AFTER INSERT OR UPDATE ON actions DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_action_execution_pair();
CREATE CONSTRAINT TRIGGER execution_action_consistency
    AFTER INSERT OR UPDATE ON executions DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_action_execution_pair();

-- No global roles or blanket runtime GRANTs. Provision per-purpose roles separately.
-- Runtime must not own this schema or have ALTER/TRUNCATE/trigger-disable privileges.
-- Application append-only controls are not tamper proof against database administrators.
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard_model FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_model FROM PUBLIC;
COMMIT;
