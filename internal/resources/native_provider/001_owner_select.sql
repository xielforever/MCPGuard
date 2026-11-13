CREATE SCHEMA mcpguard_provision;
REVOKE ALL ON SCHEMA mcpguard_provision FROM PUBLIC;
CREATE TABLE mcpguard_provision.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL);
CREATE TABLE mcpguard_provision.targets(target_id uuid PRIMARY KEY,definition jsonb NOT NULL);
CREATE TABLE mcpguard_provision.accounts(
 account_id uuid PRIMARY KEY,target_id uuid NOT NULL REFERENCES mcpguard_provision.targets,
 plan_hash text NOT NULL,native_identity name NOT NULL UNIQUE,role_oid oid NOT NULL,expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION mcpguard_provision.create_account(target uuid,account uuid,hash text,password text,expiry timestamptz)
RETURNS oid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp SET password_encryption='scram-sha-256' AS $$
DECLARE identity name:='mg_'||replace(account::text,'-','');prior mcpguard_provision.accounts;created oid;
BEGIN
 IF target IS NULL OR account IS NULL OR hash IS NULL OR password IS NULL OR expiry IS NULL
 OR length(hash)<>43 OR password !~ '^[A-Za-z0-9_-]{43,128}$' OR expiry<=clock_timestamp() OR expiry>clock_timestamp()+interval '366 days'
 OR NOT EXISTS(SELECT FROM mcpguard_provision.targets WHERE target_id=target AND definition->>'template'='PG_OWNER_SELECT_V1' AND (definition->>'enabled')::boolean) THEN RAISE EXCEPTION 'invalid fixed provisioning plan';END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(account::text,529401));
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
CREATE FUNCTION mcpguard_provision.disable_account(account uuid,hash text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE owned mcpguard_provision.accounts;
BEGIN
 SELECT * INTO STRICT owned FROM mcpguard_provision.accounts WHERE account_id=account AND plan_hash=hash;
 IF NOT EXISTS(SELECT FROM pg_roles WHERE oid=owned.role_oid AND rolname=owned.native_identity) THEN RETURN false;END IF;
 EXECUTE format('ALTER ROLE %I NOLOGIN',owned.native_identity);RETURN true;
END $$;
CREATE FUNCTION mcpguard_provision.target_matches(target uuid,expected jsonb) RETURNS boolean
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
 SELECT EXISTS(SELECT FROM mcpguard_provision.targets WHERE target_id=target AND definition=expected);
$$;
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard_provision FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_provision FROM PUBLIC;
