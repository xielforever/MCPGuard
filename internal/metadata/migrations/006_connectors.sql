CREATE TABLE mcpguard.connectors (
  org_id uuid NOT NULL REFERENCES mcpguard.control_heads,
  id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  kind text NOT NULL CHECK (kind IN ('postgresql','redis','jenkins','kubernetes')),
  config_ciphertext bytea NOT NULL CHECK (length(config_ciphertext) BETWEEN 28 AND 65564),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  enabled boolean NOT NULL DEFAULT false,
  test_status text NOT NULL DEFAULT 'NOT_TESTED' CHECK (test_status IN ('NOT_TESTED','PASSED','FAILED')),
  tested_at timestamptz,
  PRIMARY KEY (org_id,id),
  UNIQUE (org_id,name),
  CHECK ((test_status = 'NOT_TESTED') = (tested_at IS NULL)),
  CHECK (NOT enabled OR test_status = 'PASSED')
);

CREATE TABLE mcpguard.connector_grants (
  org_id uuid NOT NULL,
  connector_id uuid NOT NULL,
  principal_id uuid NOT NULL,
  PRIMARY KEY (org_id,connector_id,principal_id),
  FOREIGN KEY (org_id,connector_id) REFERENCES mcpguard.connectors (org_id,id),
  FOREIGN KEY (org_id,principal_id) REFERENCES mcpguard.principals (org_id,id)
);

CREATE INDEX connector_grantee ON mcpguard.connector_grants (org_id,principal_id,connector_id);

CREATE TABLE mcpguard.connector_audit (
  seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
  org_id uuid NOT NULL,
  connector_id uuid NOT NULL,
  principal_id uuid NOT NULL,
  revision bigint NOT NULL CHECK (revision > 0),
  event_type text NOT NULL CHECK (event_type IN ('CREATED','TEST_PASSED','TEST_FAILED','ENABLED','DISABLED')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY (org_id,connector_id) REFERENCES mcpguard.connectors (org_id,id),
  FOREIGN KEY (org_id,principal_id) REFERENCES mcpguard.principals (org_id,id)
);

CREATE INDEX connector_audit_history ON mcpguard.connector_audit (org_id,connector_id,seq DESC);
CREATE TRIGGER connector_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard.connector_audit
  FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();

REVOKE ALL ON mcpguard.connectors,mcpguard.connector_grants,mcpguard.connector_audit FROM PUBLIC;
