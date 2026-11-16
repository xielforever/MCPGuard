-- RA-15: metadata restore fence. The generation lives in each business PG, so
-- restoring an older metadata backup (which restores its own copy) still leaves
-- the native generation ahead. Authorization checks compare both and refuse
-- until an operator re-verifies the restored point.
CREATE TABLE mcpguard_provision.fence_state(
 org_id uuid PRIMARY KEY,
 generation bigint NOT NULL CHECK(generation>0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION mcpguard_provision.advance_fence(org uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE g bigint;
BEGIN
 IF org IS NULL THEN RAISE EXCEPTION 'org required';END IF;
 INSERT INTO mcpguard_provision.fence_state(org_id,generation) VALUES(org,1)
  ON CONFLICT(org_id) DO UPDATE SET generation=mcpguard_provision.fence_state.generation+1,updated_at=clock_timestamp()
  RETURNING generation INTO g;
 RETURN g;
END $$;
CREATE FUNCTION mcpguard_provision.fence_generation(org uuid) RETURNS bigint
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
 SELECT coalesce((SELECT generation FROM mcpguard_provision.fence_state WHERE org_id=org),0)
$$;
REVOKE ALL ON mcpguard_provision.fence_state FROM PUBLIC;
REVOKE ALL ON FUNCTION mcpguard_provision.advance_fence(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION mcpguard_provision.fence_generation(uuid) FROM PUBLIC;
