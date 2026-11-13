-- RA-07: server-only reference envelopes, explicit lease policy and online
-- terminal state. No download ticket, context or new business route is added.
CREATE TABLE mcpguard_ra.credential_key_registry(
 org_id uuid NOT NULL,issuer text NOT NULL,key_id text NOT NULL,fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(org_id,issuer,key_id),UNIQUE(org_id,issuer,key_id,fingerprint)
);
CREATE TABLE mcpguard_ra.credential_key_usage(
 fingerprint text PRIMARY KEY,use_count bigint NOT NULL DEFAULT 0 CHECK(use_count BETWEEN 0 AND 4294967296)
);
CREATE TABLE mcpguard_ra.credential_issuers(
 org_id uuid PRIMARY KEY,issuer text NOT NULL,issuer_epoch uuid NOT NULL,key_id text NOT NULL,key_fingerprint text NOT NULL,
 version bigint NOT NULL CHECK(version>0),enabled boolean NOT NULL,definition jsonb NOT NULL,
 FOREIGN KEY(org_id) REFERENCES mcpguard.authorization_heads,
 FOREIGN KEY(org_id,issuer,key_id,key_fingerprint) REFERENCES mcpguard_ra.credential_key_registry(org_id,issuer,key_id,fingerprint)
);
CREATE TABLE mcpguard_ra.credential_policies(
 org_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 version bigint NOT NULL CHECK(version>0),max_ttl_seconds integer NOT NULL CHECK(max_ttl_seconds BETWEEN 1 AND 86400),enabled boolean NOT NULL,definition jsonb NOT NULL,
 PRIMARY KEY(org_id,resource_id,resource_version,profile_id,profile_version),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions
);
CREATE TABLE mcpguard_ra.credential_config_history(
 seq bigserial PRIMARY KEY,org_id uuid NOT NULL,kind text NOT NULL,version bigint NOT NULL,definition jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION mcpguard_ra.credential_config_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['version','enabled','definition','max_ttl_seconds','issuer','issuer_epoch','key_id','key_fingerprint']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['version','enabled','definition','max_ttl_seconds','issuer','issuer_epoch','key_id','key_fingerprint']) OR NEW.version<=OLD.version THEN RAISE EXCEPTION 'new credential configuration version required';END IF;RETURN NEW;
END $$;
CREATE FUNCTION mcpguard_ra.record_credential_config() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN INSERT INTO mcpguard_ra.credential_config_history(org_id,kind,version,definition) VALUES(NEW.org_id,TG_TABLE_NAME,NEW.version,NEW.definition);RETURN NEW;END $$;
CREATE TRIGGER credential_issuer_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_issuers FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_config_guard();
CREATE TRIGGER credential_policy_guard BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_policies FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_config_guard();
CREATE TRIGGER credential_issuer_history AFTER INSERT OR UPDATE ON mcpguard_ra.credential_issuers FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.record_credential_config();
CREATE TRIGGER credential_policy_history AFTER INSERT OR UPDATE ON mcpguard_ra.credential_policies FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.record_credential_config();
CREATE TRIGGER credential_issuer_epoch AFTER INSERT OR UPDATE ON mcpguard_ra.credential_issuers FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TRIGGER credential_policy_epoch AFTER INSERT OR UPDATE ON mcpguard_ra.credential_policies FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.bump_epoch();
CREATE TRIGGER credential_config_history_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_config_history FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER credential_key_registry_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_key_registry FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE FUNCTION mcpguard_ra.credential_usage_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='INSERT' AND NEW.use_count<>0) OR (TG_OP='UPDATE' AND (NEW.fingerprint<>OLD.fingerprint OR NEW.use_count<>OLD.use_count+1)) THEN RAISE EXCEPTION 'immutable key usage qualification';END IF;RETURN NEW;
END $$;
CREATE TRIGGER credential_usage_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.credential_key_usage FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_usage_guard();
-- Reservations survive rollback of later issuance. Burning a nonce does not
-- create a package or grant; a used reservation is never recycled.
CREATE TABLE mcpguard_ra.credential_nonce_reservations(
 fingerprint text NOT NULL REFERENCES mcpguard_ra.credential_key_usage,
 nonce bytea NOT NULL CHECK(octet_length(nonce)=12),org_id uuid NOT NULL,credential_id uuid NOT NULL,
 reserved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(fingerprint,nonce),UNIQUE(org_id,credential_id),
 UNIQUE(org_id,credential_id,fingerprint,nonce),FOREIGN KEY(org_id) REFERENCES mcpguard.authorization_heads
);
CREATE TRIGGER credential_nonce_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_nonce_reservations FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TABLE mcpguard_ra.credential_leases(
 org_id uuid NOT NULL,lease_id uuid NOT NULL,credential_id uuid NOT NULL,subject_id uuid NOT NULL,
 grant_id uuid NOT NULL,grant_version bigint NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,binding_id uuid NOT NULL,binding_version bigint NOT NULL,
 issuer_version bigint NOT NULL,policy_version bigint NOT NULL,issued_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED','EXPIRED')),revision bigint NOT NULL CHECK(revision>0),reason_ciphertext bytea,
 PRIMARY KEY(org_id,lease_id),UNIQUE(org_id,credential_id),UNIQUE(org_id,lease_id,credential_id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,grant_id,grant_version) REFERENCES mcpguard_ra.access_grant_versions,
 FOREIGN KEY(org_id,binding_id,binding_version) REFERENCES mcpguard_ra.identity_bindings(org_id,binding_id,binding_version),
 CHECK(issued_at<expires_at)
);
CREATE TABLE mcpguard_ra.credential_packages(
 org_id uuid NOT NULL,credential_id uuid NOT NULL,lease_id uuid NOT NULL,issuer text NOT NULL,issuer_epoch uuid NOT NULL,
 key_id text NOT NULL,key_fingerprint text NOT NULL,nonce bytea NOT NULL,
 reference jsonb NOT NULL,digest text NOT NULL CHECK(digest ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(org_id,credential_id),
 FOREIGN KEY(org_id,lease_id,credential_id) REFERENCES mcpguard_ra.credential_leases(org_id,lease_id,credential_id),
 FOREIGN KEY(org_id,credential_id,key_fingerprint,nonce) REFERENCES mcpguard_ra.credential_nonce_reservations(org_id,credential_id,fingerprint,nonce),
 FOREIGN KEY(org_id,issuer,key_id,key_fingerprint) REFERENCES mcpguard_ra.credential_key_registry(org_id,issuer,key_id,fingerprint)
);
CREATE TABLE mcpguard_ra.credential_blobs(
 org_id uuid NOT NULL,credential_id uuid NOT NULL,envelope bytea NOT NULL CHECK(octet_length(envelope) BETWEEN 1 AND 32768),
 PRIMARY KEY(org_id,credential_id),FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages
);
CREATE TABLE mcpguard_ra.credential_commands(
 org_id uuid NOT NULL,subject_id uuid NOT NULL,command text NOT NULL,target text NOT NULL,request_key text NOT NULL,
 payload_hash text NOT NULL,credential_id uuid NOT NULL,
 PRIMARY KEY(org_id,subject_id,command,target,request_key),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages
);
CREATE TABLE mcpguard_ra.credential_audit(
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),org_id uuid NOT NULL,subject_id uuid NOT NULL,
 credential_id uuid,reservation_id uuid,event_type text NOT NULL,server_identity text NOT NULL DEFAULT session_user,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages,
 FOREIGN KEY(org_id,reservation_id) REFERENCES mcpguard_ra.credential_nonce_reservations(org_id,credential_id)
);
CREATE INDEX credential_lease_owner ON mcpguard_ra.credential_leases(org_id,subject_id,credential_id);
CREATE FUNCTION mcpguard_ra.credential_lease_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'ACTIVE' OR NEW.revision<>1 OR NEW.issued_at>clock_timestamp() OR NEW.expires_at<=clock_timestamp() OR NOT EXISTS(
   SELECT FROM mcpguard_ra.access_grant_versions v JOIN mcpguard_ra.access_grant_heads h ON (h.org_id,h.grant_id,h.version)=(v.org_id,v.grant_id,v.version)
   JOIN mcpguard_ra.credential_issuers i ON i.org_id=v.org_id
   JOIN mcpguard_ra.credential_policies p ON (p.org_id,p.resource_id,p.resource_version,p.profile_id,p.profile_version)=(v.org_id,v.resource_id,v.resource_version,v.profile_id,v.profile_version)
   JOIN mcpguard_ra.identity_bindings b ON (b.org_id,b.binding_id,b.binding_version)=(v.org_id,v.binding_id,v.binding_version)
   WHERE v.org_id=NEW.org_id AND v.grant_id=NEW.grant_id AND v.version=NEW.grant_version AND h.state='ACTIVE'
   AND (v.subject_id,v.resource_id,v.resource_version,v.profile_id,v.profile_version,v.binding_id,v.binding_version)=(NEW.subject_id,NEW.resource_id,NEW.resource_version,NEW.profile_id,NEW.profile_version,NEW.binding_id,NEW.binding_version)
   AND i.enabled AND i.version=NEW.issuer_version AND p.enabled AND p.version=NEW.policy_version AND b.enabled
   AND NEW.expires_at<=v.expires_at AND NEW.expires_at<=NEW.issued_at+make_interval(secs=>p.max_ttl_seconds)) THEN RAISE EXCEPTION 'credential lease requires current bounded authorization';END IF;
 ELSE
  IF TG_OP='DELETE' OR OLD.state<>'ACTIVE' OR NEW.state NOT IN ('REVOKED','EXPIRED') OR NEW.revision<>OLD.revision+1 OR (to_jsonb(NEW)-ARRAY['state','revision','reason_ciphertext']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','reason_ciphertext']) THEN RAISE EXCEPTION 'credential lease cannot revive or change binding';END IF;
 END IF;RETURN NEW;
END $$;
CREATE TRIGGER credential_lease_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.credential_leases FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_lease_guard();
CREATE TRIGGER credential_package_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_packages FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER credential_blob_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_blobs FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER credential_command_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_commands FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER credential_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.credential_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;
REVOKE ALL ON mcpguard_ra.credential_key_registry,mcpguard_ra.credential_key_usage,mcpguard_ra.credential_issuers,mcpguard_ra.credential_policies,mcpguard_ra.credential_config_history,mcpguard_ra.credential_nonce_reservations,mcpguard_ra.credential_leases,mcpguard_ra.credential_packages,mcpguard_ra.credential_blobs,mcpguard_ra.credential_commands,mcpguard_ra.credential_audit FROM PUBLIC;
