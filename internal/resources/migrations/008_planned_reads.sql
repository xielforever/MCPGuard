-- RA-11: planned production reads. One immutable plan binds the current
-- principal, the resource/binding snapshot, the ACTIVE credential revision and
-- the requested item before anything is dispatched. No new business route is
-- exposed until the gateway attaches it explicitly.
CREATE TABLE mcpguard_ra.read_plans (
 org_id uuid NOT NULL,principal_id uuid NOT NULL,plan_id uuid NOT NULL,
 resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 binding_id uuid NOT NULL,binding_version bigint NOT NULL,
 native_identity text NOT NULL,secret_version text NOT NULL,
 credential_id uuid NOT NULL,credential_revision bigint NOT NULL,
 item_id text NOT NULL,auth_epoch bigint NOT NULL,plan_hash text NOT NULL,
 state text NOT NULL CHECK(state IN ('RUNNING','SUCCEEDED','FAILED','OUTCOME_UNKNOWN')),
 result_ciphertext bytea,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,plan_id),
 FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions(org_id,resource_id,version),
 FOREIGN KEY(org_id,binding_id,binding_version) REFERENCES mcpguard_ra.identity_bindings(org_id,binding_id,binding_version),
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages(org_id,credential_id)
);
CREATE TABLE mcpguard_ra.read_audit (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
 org_id uuid NOT NULL,principal_id uuid NOT NULL,plan_id uuid NOT NULL,
 event_type text NOT NULL CHECK(event_type IN ('READ_INTENT','SUCCEEDED','FAILED','OUTCOME_UNKNOWN','DENIED')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,plan_id) REFERENCES mcpguard_ra.read_plans(org_id,plan_id)
);
CREATE INDEX read_plan_owner ON mcpguard_ra.read_plans(org_id,principal_id,created_at,plan_id);
CREATE TRIGGER read_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.read_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
