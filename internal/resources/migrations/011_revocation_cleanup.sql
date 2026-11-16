-- RA-14: revoked native accounts must keep an explicit cleanup state instead of
-- being reported as cleaned. Rows stay PENDING/FAILED until a retry proves the
-- native side effect was removed.
CREATE TABLE mcpguard_ra.revocation_cleanup(
 org_id uuid NOT NULL,account_id uuid NOT NULL,resource_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('PENDING','CLEANED','FAILED')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 last_error text,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,account_id)
);
CREATE INDEX revocation_cleanup_pending ON mcpguard_ra.revocation_cleanup(org_id,state,updated_at);
REVOKE ALL ON mcpguard_ra.revocation_cleanup FROM PUBLIC;
