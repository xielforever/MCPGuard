-- RA-08. Only verifier hashes are stored. No raw token, response snapshot,
-- ciphertext, URL, header, arbitrary reason or exception text enters audit.
CREATE TABLE mcpguard_ra.delivery_authorizations(
 org_id uuid NOT NULL,authorization_id uuid NOT NULL,subject_id uuid NOT NULL,
 principal_token_hash bytea NOT NULL,verifier_hash text NOT NULL CHECK(verifier_hash ~ '^[A-Za-z0-9_-]{43}$'),
 audience text NOT NULL CHECK(audience='mcpguard-delivery'),scope text NOT NULL CHECK(scope='credential:claim'),
 issued_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),revision bigint NOT NULL CHECK(revision>0),
 PRIMARY KEY(org_id,authorization_id),UNIQUE(verifier_hash),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 CHECK(expires_at>issued_at AND expires_at<=issued_at+interval '3600 seconds')
);
CREATE FUNCTION mcpguard_ra.delivery_auth_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'ACTIVE' OR NEW.revision<>1 OR NEW.issued_at>clock_timestamp() OR NEW.expires_at<=clock_timestamp() OR NOT EXISTS(SELECT FROM mcpguard.principals p WHERE p.org_id=NEW.org_id AND p.id=NEW.subject_id AND p.token_hash=NEW.principal_token_hash AND p.enabled AND p.expires_at>=NEW.expires_at) THEN RAISE EXCEPTION 'current bounded delivery authentication required';END IF;
 ELSE
  IF TG_OP='DELETE' OR OLD.state<>'ACTIVE' OR NEW.state<>'REVOKED' OR NEW.revision<>OLD.revision+1 OR (to_jsonb(NEW)-ARRAY['state','revision']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision']) THEN RAISE EXCEPTION 'delivery authorization cannot revive or change identity';END IF;
 END IF;RETURN NEW;
END $$;
CREATE TRIGGER delivery_auth_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.delivery_authorizations FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.delivery_auth_guard();
CREATE TABLE mcpguard_ra.download_tickets(
 org_id uuid NOT NULL,download_id uuid NOT NULL,subject_id uuid NOT NULL,credential_id uuid NOT NULL,lease_id uuid NOT NULL,
 issued_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('READY','CLAIMED','EXPIRED','CANCELLED')),revision bigint NOT NULL CHECK(revision>0),
 claimed_authorization_id uuid,claimed_at timestamptz,
 PRIMARY KEY(org_id,download_id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,lease_id,credential_id) REFERENCES mcpguard_ra.credential_leases(org_id,lease_id,credential_id),
 FOREIGN KEY(org_id,claimed_authorization_id) REFERENCES mcpguard_ra.delivery_authorizations(org_id,authorization_id),
 CHECK(expires_at>issued_at AND expires_at<=issued_at+interval '300 seconds'),
 CHECK((state='CLAIMED')=(claimed_at IS NOT NULL AND claimed_authorization_id IS NOT NULL))
);
CREATE INDEX download_ready_owner ON mcpguard_ra.download_tickets(org_id,subject_id,expires_at) WHERE state='READY';
CREATE FUNCTION mcpguard_ra.download_ticket_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'READY' OR NEW.revision<>1 OR NEW.issued_at>clock_timestamp() OR NEW.expires_at<=clock_timestamp() OR NOT EXISTS(SELECT FROM mcpguard_ra.credential_leases l WHERE (l.org_id,l.lease_id,l.credential_id,l.subject_id)=(NEW.org_id,NEW.lease_id,NEW.credential_id,NEW.subject_id) AND l.state='ACTIVE' AND l.expires_at>=NEW.expires_at) THEN RAISE EXCEPTION 'download requires current bounded lease';END IF;
 ELSE
  IF TG_OP='DELETE' OR OLD.state<>'READY' OR NEW.state NOT IN ('CLAIMED','EXPIRED','CANCELLED') OR NEW.revision<>OLD.revision+1 OR (to_jsonb(NEW)-ARRAY['state','revision','claimed_at','claimed_authorization_id']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','claimed_at','claimed_authorization_id']) THEN RAISE EXCEPTION 'download ticket is consumed once';END IF;
  IF NEW.state='CLAIMED' AND (OLD.expires_at<=clock_timestamp() OR NOT EXISTS(SELECT FROM mcpguard_ra.delivery_authorizations a JOIN mcpguard.principals p ON (p.org_id,p.id)=(a.org_id,a.subject_id) WHERE a.org_id=NEW.org_id AND a.authorization_id=NEW.claimed_authorization_id AND a.subject_id=NEW.subject_id AND a.audience='mcpguard-delivery' AND a.scope='credential:claim' AND a.state='ACTIVE' AND a.expires_at>clock_timestamp() AND a.principal_token_hash=p.token_hash AND p.enabled AND p.expires_at>clock_timestamp())) THEN RAISE EXCEPTION 'current private claim authorization required';END IF;
 END IF;RETURN NEW;
END $$;
CREATE TRIGGER download_ticket_guard BEFORE INSERT OR UPDATE OR DELETE ON mcpguard_ra.download_tickets FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.download_ticket_guard();
CREATE TABLE mcpguard_ra.download_commands(
 org_id uuid NOT NULL,subject_id uuid NOT NULL,request_key text NOT NULL,payload_hash text NOT NULL,download_id uuid NOT NULL,
 PRIMARY KEY(org_id,subject_id,request_key),
 FOREIGN KEY(org_id,download_id) REFERENCES mcpguard_ra.download_tickets
);
CREATE TABLE mcpguard_ra.download_audit(
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),org_id uuid NOT NULL,subject_id uuid NOT NULL,
 download_id uuid,credential_id uuid,authorization_id uuid,
 event_type text NOT NULL CHECK(event_type IN ('AUTHORIZATION_ENROLLED','TICKET_READY','TICKET_EXPIRED','TICKET_CANCELLED','CLAIM_COMMITTED','DELIVERY_WRITE_COMPLETED','DELIVERY_WRITE_INTERRUPTED')),
 server_identity text NOT NULL DEFAULT session_user,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,download_id) REFERENCES mcpguard_ra.download_tickets,
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra.credential_packages,
 FOREIGN KEY(org_id,authorization_id) REFERENCES mcpguard_ra.delivery_authorizations
);
CREATE INDEX download_audit_ticket ON mcpguard_ra.download_audit(org_id,download_id,seq DESC);
CREATE UNIQUE INDEX download_one_claim_event ON mcpguard_ra.download_audit(org_id,download_id) WHERE event_type='CLAIM_COMMITTED';
CREATE UNIQUE INDEX download_one_write_outcome ON mcpguard_ra.download_audit(org_id,download_id) WHERE event_type IN ('DELIVERY_WRITE_COMPLETED','DELIVERY_WRITE_INTERRUPTED');
CREATE TRIGGER download_command_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.download_commands FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
CREATE TRIGGER download_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.download_audit FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
REVOKE ALL ON mcpguard_ra.delivery_authorizations,mcpguard_ra.download_tickets,mcpguard_ra.download_commands,mcpguard_ra.download_audit FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard_ra FROM PUBLIC;
