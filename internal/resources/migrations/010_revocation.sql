-- Operator revocation: credential (file), grant, principal and resource scopes.
-- Every revocation is recorded once with the number of affected artefacts, so
-- the blast radius is auditable and cannot be inferred from mutations only.
CREATE TABLE mcpguard_ra.revocation_audit(
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
 org_id uuid NOT NULL, actor_id uuid NOT NULL,
 target_kind text NOT NULL CHECK(target_kind IN ('CREDENTIAL','GRANT','PRINCIPAL','RESOURCE')),
 target_id uuid NOT NULL, subject_id uuid, resource_id uuid,
 affected_grants integer NOT NULL CHECK(affected_grants>=0),
 affected_credentials integer NOT NULL CHECK(affected_credentials>=0),
 affected_tickets integer NOT NULL CHECK(affected_tickets>=0),
 affected_contexts integer NOT NULL CHECK(affected_contexts>=0),
 reason_ciphertext bytea,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,actor_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE INDEX revocation_audit_org ON mcpguard_ra.revocation_audit(org_id,created_at DESC,seq);
REVOKE ALL ON mcpguard_ra.revocation_audit FROM PUBLIC;
