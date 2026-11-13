-- Additive RA-02/03 catalog only. Apply explicitly with mcpguard-catalog migrate.
-- The sealed mcpguard 1..4 migrations and legacy ledgers are not modified.
CREATE SCHEMA mcpguard_ra;
REVOKE ALL ON SCHEMA mcpguard_ra FROM PUBLIC;
CREATE TABLE mcpguard_ra.schema_versions(version integer PRIMARY KEY, checksum text NOT NULL);
CREATE TABLE mcpguard_ra.resource_versions (
 org_id uuid NOT NULL REFERENCES mcpguard.authorization_heads(org_id),
 resource_id uuid NOT NULL, version bigint NOT NULL CHECK(version>0),
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 PRIMARY KEY(org_id,resource_id,version),
 CHECK(definition->>'orgId'=org_id::text AND definition->>'resourceId'=resource_id::text AND definition->>'version'=version::text)
);
CREATE TABLE mcpguard_ra.resource_heads (
 org_id uuid NOT NULL, resource_id uuid NOT NULL, version bigint NOT NULL, revision bigint NOT NULL CHECK(revision>0), enabled boolean NOT NULL,
 PRIMARY KEY(org_id,resource_id),
 FOREIGN KEY(org_id,resource_id,version) REFERENCES mcpguard_ra.resource_versions
);
CREATE TABLE mcpguard_ra.profile_versions (
 org_id uuid NOT NULL,resource_id uuid NOT NULL,profile_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 PRIMARY KEY(org_id,resource_id,profile_id,version),
 FOREIGN KEY(org_id,resource_id) REFERENCES mcpguard_ra.resource_heads,
 CHECK(definition->>'orgId'=org_id::text AND definition->>'resourceId'=resource_id::text AND definition->>'profileId'=profile_id::text AND definition->>'version'=version::text)
);
CREATE TABLE mcpguard_ra.tool_bindings (
 org_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,tool_name text NOT NULL,tool_version bigint NOT NULL,operation_id text NOT NULL,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 PRIMARY KEY(org_id,resource_id,resource_version,profile_id,profile_version,tool_name,tool_version),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions,
 CHECK(definition->>'toolName'=tool_name AND definition->>'operationId'=operation_id)
);
CREATE TABLE mcpguard_ra.resource_access (
 org_id uuid NOT NULL,principal_id uuid NOT NULL,resource_id uuid NOT NULL,
 can_discover boolean NOT NULL DEFAULT false,can_request boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,principal_id,resource_id),
 FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id) REFERENCES mcpguard_ra.resource_heads
);
CREATE TABLE mcpguard_ra.identity_bindings (
 org_id uuid NOT NULL,principal_id uuid NOT NULL,resource_id uuid NOT NULL,
 binding_id uuid NOT NULL,binding_version bigint NOT NULL CHECK(binding_version>0),
 resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 enabled boolean NOT NULL DEFAULT false,definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 PRIMARY KEY(org_id,principal_id,resource_id,binding_version),
 UNIQUE(org_id,binding_id,binding_version),
 FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions,
 CHECK(definition->>'principalId'=principal_id::text AND definition->>'resourceId'=resource_id::text AND definition->>'bindingId'=binding_id::text AND definition->>'bindingVersion'=binding_version::text)
);
CREATE INDEX identity_binding_latest ON mcpguard_ra.identity_bindings(org_id,principal_id,resource_id,binding_version DESC);
CREATE TABLE mcpguard_ra.fixture_reads (
 org_id uuid NOT NULL,principal_id uuid NOT NULL,id uuid NOT NULL,resource_id uuid NOT NULL,
 resource_version bigint NOT NULL,binding_id uuid NOT NULL,binding_version bigint NOT NULL,
 native_identity text NOT NULL,secret_version text NOT NULL,auth_epoch bigint NOT NULL,
 plan_hash text NOT NULL,state text NOT NULL CHECK(state IN ('RUNNING','SUCCEEDED','FAILED','OUTCOME_UNKNOWN')),
 result_ciphertext bytea,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,id),FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,binding_id,binding_version) REFERENCES mcpguard_ra.identity_bindings(org_id,binding_id,binding_version)
);
CREATE TABLE mcpguard_ra.fixture_audit (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
 org_id uuid NOT NULL,principal_id uuid NOT NULL,execution_id uuid NOT NULL,event_type text NOT NULL
 CHECK(event_type IN ('READ_INTENT','SUCCEEDED','FAILED','OUTCOME_UNKNOWN')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,execution_id) REFERENCES mcpguard_ra.fixture_reads(org_id,id)
);
CREATE INDEX fixture_read_owner ON mcpguard_ra.fixture_reads(org_id,principal_id,created_at,id);
CREATE TRIGGER resource_version_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.resource_versions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER profile_version_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.profile_versions FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER tool_binding_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.tool_bindings FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER fixture_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.fixture_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard_ra.binding_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (NOT OLD.enabled AND NEW.enabled) OR (to_jsonb(NEW)-'enabled') IS DISTINCT FROM (to_jsonb(OLD)-'enabled') THEN RAISE EXCEPTION 'immutable identity binding';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER binding_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.identity_bindings FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.binding_guard();
CREATE FUNCTION mcpguard_ra.read_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.state<>'RUNNING' OR NEW.state='RUNNING'
 OR (to_jsonb(NEW)-ARRAY['state','result_ciphertext']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','result_ciphertext'])
 THEN RAISE EXCEPTION 'immutable read qualification';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fixture_read_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.fixture_reads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.read_guard();
CREATE FUNCTION mcpguard_ra.bump_epoch() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 UPDATE mcpguard.authorization_heads SET auth_epoch=auth_epoch+1 WHERE org_id=coalesce(NEW.org_id,OLD.org_id);
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END $$;
-- AFTER triggers run only for a row actually inserted/updated/deleted. In
-- particular, idempotent INSERT ... ON CONFLICT DO NOTHING must not bump epoch.
CREATE TRIGGER resource_head_epoch AFTER INSERT OR UPDATE OR DELETE ON mcpguard_ra.resource_heads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TRIGGER resource_access_epoch AFTER INSERT OR UPDATE OR DELETE ON mcpguard_ra.resource_access FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TRIGGER identity_binding_epoch AFTER INSERT OR UPDATE OR DELETE ON mcpguard_ra.identity_bindings FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE FUNCTION mcpguard_ra.head_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.revision<>OLD.revision+1 OR NEW.version<OLD.version THEN RAISE EXCEPTION 'invalid catalog head transition';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER resource_head_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.resource_heads FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.head_guard();
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard_ra FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;

