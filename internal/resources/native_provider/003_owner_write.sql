-- RA-12: a separate write identity per person and resource. Read identities
-- keep their SELECT-only guarantees; this template creates a role that may
-- UPDATE only its own rows through the row policy below. No definer function
-- and no other table privilege is granted.
CREATE OR REPLACE FUNCTION mcpguard_provision.create_write_account(target uuid,account uuid,hash text,password text,expiry timestamptz)
RETURNS oid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp SET password_encryption='scram-sha-256' AS $$
DECLARE identity name:='mgu_'||replace(account::text,'-','');prior mcpguard_provision.accounts;created oid;
BEGIN
 IF target IS NULL OR account IS NULL OR hash IS NULL OR password IS NULL OR expiry IS NULL
 OR length(hash)<>43 OR password !~ '^[A-Za-z0-9_-]{43,128}$' OR expiry<=clock_timestamp() OR expiry>clock_timestamp()+interval '366 days'
 OR NOT EXISTS(SELECT FROM mcpguard_provision.targets WHERE target_id=target AND definition->>'template'='PG_OWNER_UPDATE_V1' AND (definition->>'enabled')::boolean) THEN RAISE EXCEPTION 'invalid fixed write provisioning plan';END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(account::text,529402));
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
 EXECUTE format('GRANT SELECT,UPDATE ON resource_demo.items TO %I',identity);
 SELECT oid INTO STRICT created FROM pg_roles WHERE rolname=identity;
 INSERT INTO mcpguard_provision.accounts(account_id,target_id,plan_hash,native_identity,role_oid,expires_at) VALUES(account,target,hash,identity,created,expiry);
 RETURN created;
END $$;
DO $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_policy WHERE polrelid='resource_demo.items'::regclass AND polname='owner_update') THEN
  CREATE POLICY owner_update ON resource_demo.items FOR UPDATE USING (owner_name = CURRENT_USER) WITH CHECK (owner_name = CURRENT_USER);
 END IF;
END $$;
-- New functions default to PUBLIC EXECUTE; native identities must keep zero
-- definer-function privileges, so revoke before the manager grant is applied.
REVOKE ALL ON FUNCTION mcpguard_provision.create_write_account(uuid,uuid,text,text,timestamptz) FROM PUBLIC;
