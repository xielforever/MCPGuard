-- RA-12: resource-bound controlled writes. The plan is frozen before it is
-- deferred to another authorized person; only an explicit resume by the
-- requester consumes the single dispatch eligibility. Resource authorization
-- never substitutes for the separate write approval.
CREATE TABLE mcpguard_ra.write_requests (
 org_id uuid NOT NULL,request_id uuid NOT NULL,subject_id uuid NOT NULL,
 resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 binding_id uuid NOT NULL,binding_version bigint NOT NULL,
 credential_id uuid NOT NULL,credential_revision bigint NOT NULL,
 native_identity text NOT NULL,secret_version text NOT NULL,
 item_id text NOT NULL,expected_version bigint NOT NULL,
 auth_epoch bigint NOT NULL,plan_hash text NOT NULL,plan_ciphertext bytea NOT NULL,
 state text NOT NULL CHECK(state IN ('DEFERRED','APPROVED','REJECTED','CANCELLED','EXECUTING','SUCCEEDED','FAILED','OUTCOME_UNKNOWN')),
 decided_by uuid,decided_at timestamptz,decision_reason text,
 request_key text NOT NULL,result_ciphertext bytea,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,request_id),
 UNIQUE(org_id,subject_id,request_key),
 CHECK(decided_by IS NULL OR decided_by <> subject_id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,decided_by) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions(org_id,resource_id,version),
 FOREIGN KEY(org_id,binding_id,binding_version) REFERENCES mcpguard_ra.identity_bindings(org_id,binding_id,binding_version),
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages(org_id,credential_id)
);
CREATE TABLE mcpguard_ra.write_audit (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
 org_id uuid NOT NULL,subject_id uuid NOT NULL,request_id uuid NOT NULL,
 event_type text NOT NULL CHECK(event_type IN ('PLANNED','APPROVED','REJECTED','CANCELLED','DISPATCH_INTENT','SUCCEEDED','FAILED','OUTCOME_UNKNOWN','DENIED')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,request_id) REFERENCES mcpguard_ra.write_requests(org_id,request_id)
);
CREATE INDEX write_request_owner ON mcpguard_ra.write_requests(org_id,subject_id,created_at,request_id);
CREATE INDEX write_request_queue ON mcpguard_ra.write_requests(org_id,state,created_at,request_id);
CREATE TRIGGER write_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.write_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
