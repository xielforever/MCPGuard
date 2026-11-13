-- Additive native upgrade. A missing outcome is sealed, never recreated.
CREATE TABLE mcpguard_provision.account_fences(
 account_id uuid PRIMARY KEY,target_id uuid NOT NULL REFERENCES mcpguard_provision.targets,
 plan_hash text NOT NULL,reason text NOT NULL CHECK(reason IN ('ABSENT','CLEANUP','CONFLICT')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION mcpguard_provision.fence_immutable() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$ BEGIN RAISE EXCEPTION 'native fence is permanent';END $$;
CREATE TRIGGER native_fence_immutable BEFORE UPDATE OR DELETE ON mcpguard_provision.account_fences FOR EACH ROW EXECUTE FUNCTION mcpguard_provision.fence_immutable();
CREATE OR REPLACE FUNCTION mcpguard_provision.create_account(target uuid,account uuid,hash text,password text,expiry timestamptz)
RETURNS oid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp SET password_encryption='scram-sha-256' AS $$
DECLARE identity name:='mg_'||replace(account::text,'-','');prior mcpguard_provision.accounts;created oid;
BEGIN
 IF target IS NULL OR account IS NULL OR hash IS NULL OR password IS NULL OR expiry IS NULL
 OR length(hash)<>43 OR password !~ '^[A-Za-z0-9_-]{43,128}$' OR expiry<=clock_timestamp() OR expiry>clock_timestamp()+interval '366 days'
 OR NOT EXISTS(SELECT FROM mcpguard_provision.targets WHERE target_id=target AND definition->>'template'='PG_OWNER_SELECT_V1' AND (definition->>'enabled')::boolean) THEN RAISE EXCEPTION 'invalid fixed provisioning plan';END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(account::text,529401));
 IF EXISTS(SELECT FROM mcpguard_provision.account_fences WHERE account_id=account) THEN RAISE EXCEPTION 'native plan is permanently sealed';END IF;
 SELECT * INTO prior FROM mcpguard_provision.accounts WHERE account_id=account;
 IF FOUND THEN
  IF prior.target_id<>target OR prior.plan_hash<>hash OR prior.expires_at<>expiry THEN RAISE EXCEPTION 'account plan conflict';END IF;
  RETURN prior.role_oid;
 END IF;
 IF EXISTS(SELECT FROM pg_roles WHERE rolname=identity) THEN RAISE EXCEPTION 'unmanaged native identity already exists';END IF;
 EXECUTE format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2 PASSWORD %L VALID UNTIL %L',identity,password,expiry);
 EXECUTE format('ALTER ROLE %I SET default_transaction_read_only=on',identity);
 EXECUTE format('ALTER ROLE %I SET row_security=on',identity);
 EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),identity);
 EXECUTE format('GRANT USAGE ON SCHEMA resource_demo TO %I',identity);
 EXECUTE format('GRANT SELECT ON resource_demo.items TO %I',identity);
 SELECT oid INTO STRICT created FROM pg_roles WHERE rolname=identity;
 INSERT INTO mcpguard_provision.accounts(account_id,target_id,plan_hash,native_identity,role_oid,expires_at) VALUES(account,target,hash,identity,created,expiry);
 RETURN created;
END $$;
CREATE FUNCTION mcpguard_provision.reconcile_account(target uuid,account uuid,hash text,expiry timestamptz)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE identity name:='mg_'||replace(account::text,'-','');prior mcpguard_provision.accounts;fence mcpguard_provision.account_fences;actual oid;login boolean;status text;
BEGIN
 IF target IS NULL OR account IS NULL OR hash IS NULL OR length(hash)<>43 OR expiry IS NULL OR NOT EXISTS(SELECT FROM mcpguard_provision.targets WHERE target_id=target) THEN RAISE EXCEPTION 'invalid reconciliation plan';END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(account::text,529401));
 SELECT * INTO fence FROM mcpguard_provision.account_fences WHERE account_id=account;
 IF FOUND AND (fence.target_id<>target OR fence.plan_hash<>hash) THEN status:='CONFLICT';
 ELSE
  SELECT * INTO prior FROM mcpguard_provision.accounts WHERE account_id=account;
  IF FOUND THEN
   IF prior.target_id<>target OR prior.plan_hash<>hash OR prior.expires_at<>expiry THEN status:='CONFLICT';
   ELSE
    SELECT oid,rolcanlogin INTO actual,login FROM pg_roles WHERE rolname=prior.native_identity;
    IF actual IS NULL THEN status:='DISABLED';
    ELSIF actual<>prior.role_oid THEN status:='CONFLICT';
    ELSIF fence.account_id IS NOT NULL OR NOT login THEN status:='DISABLED';
    ELSE status:='OWNED';END IF;
    IF status='CONFLICT' THEN INSERT INTO mcpguard_provision.account_fences VALUES(account,target,hash,'CONFLICT',clock_timestamp()) ON CONFLICT DO NOTHING;END IF;
   END IF;
  ELSE
   SELECT oid INTO actual FROM pg_roles WHERE rolname=identity;
   IF actual IS NULL THEN status:='ABSENT_SEALED';ELSE status:='CONFLICT';END IF;
   INSERT INTO mcpguard_provision.account_fences VALUES(account,target,hash,CASE WHEN actual IS NULL THEN 'ABSENT' ELSE 'CONFLICT' END,clock_timestamp()) ON CONFLICT DO NOTHING;
  END IF;
 END IF;
 RETURN jsonb_build_object('status',status,'targetId',target::text,'accountId',account::text,'planHash',hash,'roleOid',prior.role_oid::text);
END $$;
CREATE FUNCTION mcpguard_provision.cleanup_account(target uuid,account uuid,hash text,expiry timestamptz)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE prior mcpguard_provision.accounts;fence mcpguard_provision.account_fences;actual oid;removed boolean;
BEGIN
 IF target IS NULL OR account IS NULL OR hash IS NULL OR length(hash)<>43 OR expiry IS NULL THEN RAISE EXCEPTION 'invalid cleanup plan';END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(account::text,529401));
 SELECT * INTO prior FROM mcpguard_provision.accounts WHERE account_id=account;
 IF NOT FOUND THEN RETURN mcpguard_provision.reconcile_account(target,account,hash,expiry);END IF;
 IF prior.target_id<>target OR prior.plan_hash<>hash OR prior.expires_at<>expiry THEN RETURN jsonb_build_object('status','CONFLICT','targetId',target::text,'accountId',account::text,'planHash',hash);END IF;
 SELECT * INTO fence FROM mcpguard_provision.account_fences WHERE account_id=account;
 IF FOUND AND (fence.target_id<>target OR fence.plan_hash<>hash) THEN RETURN jsonb_build_object('status','CONFLICT','targetId',target::text,'accountId',account::text,'planHash',hash);END IF;
 SELECT oid INTO actual FROM pg_roles WHERE rolname=prior.native_identity;
 IF actual IS NOT NULL AND actual<>prior.role_oid THEN
  INSERT INTO mcpguard_provision.account_fences VALUES(account,target,hash,'CONFLICT',clock_timestamp()) ON CONFLICT DO NOTHING;
  RETURN jsonb_build_object('status','CONFLICT','targetId',target::text,'accountId',account::text,'planHash',hash);
 END IF;
 INSERT INTO mcpguard_provision.account_fences VALUES(account,target,hash,'CLEANUP',clock_timestamp()) ON CONFLICT DO NOTHING;
 IF actual IS NULL THEN RETURN jsonb_build_object('status','ABSENT_SEALED','targetId',target::text,'accountId',account::text,'planHash',hash,'roleOid',prior.role_oid::text);END IF;
 EXECUTE format('ALTER ROLE %I NOLOGIN',prior.native_identity);
 EXECUTE format('REVOKE SELECT ON resource_demo.items FROM %I',prior.native_identity);
 EXECUTE format('REVOKE USAGE ON SCHEMA resource_demo FROM %I',prior.native_identity);
 EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I',current_database(),prior.native_identity);
 SELECT NOT rolcanlogin AND NOT has_table_privilege(rolname,'resource_demo.items','SELECT') AND NOT has_any_column_privilege(rolname,'resource_demo.items','SELECT') AND NOT has_schema_privilege(rolname,'resource_demo','USAGE') INTO removed FROM pg_roles WHERE oid=actual;
 IF NOT removed THEN RAISE EXCEPTION 'owned template cleanup incomplete';END IF;
 RETURN jsonb_build_object('status','CLEANED','targetId',target::text,'accountId',account::text,'planHash',hash,'roleOid',prior.role_oid::text);
END $$;
REVOKE ALL ON mcpguard_provision.account_fences FROM PUBLIC;
REVOKE ALL ON FUNCTION mcpguard_provision.fence_immutable(),mcpguard_provision.reconcile_account(uuid,uuid,text,timestamptz),mcpguard_provision.cleanup_account(uuid,uuid,text,timestamptz) FROM PUBLIC;
