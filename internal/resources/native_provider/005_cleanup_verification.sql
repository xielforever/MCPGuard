-- The first cleanup call commits NOLOGIN. Verify account ownership, privileges
-- and live sessions in a subsequent transaction before returning CLEANED.
CREATE FUNCTION mcpguard_provision.verify_account_cleanup(target uuid,account uuid,hash text,expiry timestamptz)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE prior mcpguard_provision.accounts; proof jsonb; actual oid; login boolean; sessions integer; result text;
BEGIN
 proof := mcpguard_provision.reconcile_account(target,account,hash,expiry);
 IF proof->>'status'='ABSENT_SEALED' THEN
  RETURN proof || jsonb_build_object('activeSessions',0);
 END IF;
 IF proof->>'status'<>'DISABLED' THEN RETURN proof; END IF;
 SELECT * INTO STRICT prior FROM mcpguard_provision.accounts WHERE account_id=account;
 SELECT oid,rolcanlogin INTO actual,login FROM pg_roles WHERE rolname=prior.native_identity;
 IF actual IS NOT NULL AND (actual<>prior.role_oid OR login) THEN
  RETURN proof || jsonb_build_object('status','CONFLICT');
 END IF;
 IF actual IS NOT NULL THEN
  EXECUTE format('REVOKE UPDATE ON resource_demo.items FROM %I',prior.native_identity);
  IF has_schema_privilege(actual,'resource_demo','USAGE,CREATE')
     OR has_table_privilege(actual,'resource_demo.items','SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
     OR has_any_column_privilege(actual,'resource_demo.items','SELECT,INSERT,UPDATE,REFERENCES') THEN
   RETURN proof || jsonb_build_object('status','DISABLED');
  END IF;
 END IF;
 SELECT count(*) INTO sessions FROM pg_stat_activity WHERE usesysid=prior.role_oid AND backend_type='client backend';
 result := CASE WHEN sessions>0 THEN 'SESSIONS_REMAIN' WHEN actual IS NULL THEN 'ABSENT_SEALED' ELSE 'CLEANED' END;
 RETURN proof || jsonb_build_object('status',result,'activeSessions',sessions);
END $$;
REVOKE ALL ON FUNCTION mcpguard_provision.verify_account_cleanup(uuid,uuid,text,timestamptz) FROM PUBLIC;
