-- RA-15: metadata-side record of the recovery point. A native generation ahead
-- of this value means the metadata (or its backup) is older than the native
-- side, so issuance/dispatch must refuse until restore-verify runs.
CREATE TABLE mcpguard_ra.recovery_fence(
 org_id uuid PRIMARY KEY,
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 verified_at timestamptz,
 verified_by uuid,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,verified_by) REFERENCES mcpguard.principals(org_id,id)
);
REVOKE ALL ON mcpguard_ra.recovery_fence FROM PUBLIC;
