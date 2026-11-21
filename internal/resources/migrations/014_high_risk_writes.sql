CREATE TABLE mcpguard_ra.high_risk_writes (
 org_id uuid NOT NULL,
 action_id uuid NOT NULL,
 reviewer_id uuid NOT NULL,
 channel text NOT NULL CHECK(channel IN ('WECOM','FEISHU')),
 code_id uuid UNIQUE,
 code_digest text,
 code_ciphertext bytea,
 expires_at timestamptz,
 consumed_at timestamptz,
 dispatch_at timestamptz,
 PRIMARY KEY(org_id,action_id),
 CHECK((code_id IS NULL AND code_digest IS NULL AND code_ciphertext IS NULL AND expires_at IS NULL AND consumed_at IS NULL)
    OR (code_id IS NOT NULL AND expires_at IS NOT NULL AND
        ((consumed_at IS NULL AND code_digest IS NOT NULL AND code_ciphertext IS NOT NULL)
          OR (consumed_at IS NOT NULL AND code_digest IS NULL AND code_ciphertext IS NULL)))),
 CHECK((consumed_at IS NULL) = (dispatch_at IS NULL)),
 FOREIGN KEY(org_id,action_id) REFERENCES mcpguard_ra.write_requests(org_id,request_id),
 FOREIGN KEY(org_id,reviewer_id) REFERENCES mcpguard.principals(org_id,id)
);

CREATE FUNCTION mcpguard_ra.guard_high_risk_transition() RETURNS trigger LANGUAGE plpgsql
 SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
BEGIN
 IF NEW.state IN ('APPROVED','REJECTED','EXECUTING') AND NEW.state <> OLD.state AND EXISTS (
  SELECT FROM mcpguard_ra.high_risk_writes h
  WHERE h.org_id=OLD.org_id AND h.action_id=OLD.request_id
   AND ((NEW.state='APPROVED' AND (h.code_id IS NULL OR NEW.decided_by IS DISTINCT FROM h.reviewer_id))
     OR (NEW.state='REJECTED' AND NEW.decided_by IS DISTINCT FROM h.reviewer_id)
     OR (NEW.state='EXECUTING' AND h.dispatch_at IS NULL))
 ) THEN
  RAISE EXCEPTION 'high-risk transition requires authorization';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_high_risk_transition BEFORE UPDATE OF state ON mcpguard_ra.write_requests
 FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.guard_high_risk_transition();
REVOKE EXECUTE ON FUNCTION mcpguard_ra.guard_high_risk_transition() FROM PUBLIC;

CREATE TABLE mcpguard_ra.high_risk_audit (
 seq bigint PRIMARY KEY DEFAULT nextval('mcpguard.audit_events_seq_seq'),
 org_id uuid NOT NULL,
 action_id uuid NOT NULL,
 actor_id uuid NOT NULL,
 event_kind text NOT NULL,
 detail jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(org_id,action_id) REFERENCES mcpguard_ra.high_risk_writes(org_id,action_id),
 FOREIGN KEY(org_id,actor_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE INDEX high_risk_audit_action ON mcpguard_ra.high_risk_audit(org_id,action_id,seq);
CREATE UNIQUE INDEX high_risk_audit_dispatch_once ON mcpguard_ra.high_risk_audit(org_id,action_id)
 WHERE event_kind='DISPATCH_INTENT';
CREATE TRIGGER high_risk_audit_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.high_risk_audit
 FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();

CREATE TABLE mcpguard_ra.high_risk_notifications (
 org_id uuid NOT NULL,
 action_id uuid NOT NULL,
 recipient_id uuid NOT NULL,
 notification_id uuid NOT NULL UNIQUE,
 channel text NOT NULL CHECK(channel IN ('WECOM','FEISHU')),
 event_kind text NOT NULL CHECK(event_kind IN ('APPROVAL_REQUIRED','APPROVED','REJECTED')),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','ACKNOWLEDGED','FAILED','EXPIRED','SUPERSEDED')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_until timestamptz,
 acknowledged_at timestamptz,
 last_failure text,
 PRIMARY KEY(org_id,action_id,event_kind),
 FOREIGN KEY(org_id,action_id) REFERENCES mcpguard_ra.high_risk_writes(org_id,action_id),
 FOREIGN KEY(org_id,recipient_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE INDEX high_risk_notifications_due ON mcpguard_ra.high_risk_notifications(next_at,notification_id)
 WHERE state IN ('PENDING','SENDING');

CREATE TABLE mcpguard_ra.high_risk_notification_attempts (
 notification_id uuid NOT NULL REFERENCES mcpguard_ra.high_risk_notifications(notification_id),
 attempt integer NOT NULL CHECK(attempt BETWEEN 1 AND 5),
 outcome text NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(notification_id,attempt)
);
CREATE TRIGGER high_risk_notification_attempts_immutable BEFORE UPDATE OR DELETE ON mcpguard_ra.high_risk_notification_attempts
 FOR EACH ROW EXECUTE FUNCTION mcpguard.immutable();
