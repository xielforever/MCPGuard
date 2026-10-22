-- FOR SHARE requires an UPDATE privilege even though it performs no mutation.
-- Expose only the lock operation, not UPDATE on the authority table.
CREATE FUNCTION mcpguard.lock_auth_epoch(target_org uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE epoch bigint;
BEGIN
  SELECT h.auth_epoch INTO STRICT epoch FROM mcpguard.authorization_heads h
    WHERE h.org_id=target_org FOR SHARE;
  RETURN epoch;
END $$;
REVOKE ALL ON FUNCTION mcpguard.lock_auth_epoch(uuid) FROM PUBLIC;
