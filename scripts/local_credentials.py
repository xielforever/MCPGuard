"""Opt-in renewal of the one enrolled loopback development credential.

The operator-side controller calls this only after checking fixture ownership.
The gateway retains its existing unprivileged runtime role and auth checks.
"""
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import tempfile
import time

import dev

STATE = dev.ROOT / ".dev/local-service"
POLICY = "credential-policy.json"
STATUS = "credential-status.json"
DB = "mcpguard-local-verified-postgres"
_next_check = 0.0
_last_status = None


def token_digest(cfg):
    if (cfg.get("container") != DB or cfg.get("MCPGUARD_AUTH_MODE") != "dev-tokens"
            or cfg.get("MCPGUARD_LISTEN") != "127.0.0.1:18080"):
        raise RuntimeError("Credential renewal requires the owned loopback dev-tokens fixture")
    token = cfg.get("alice_token")
    if not isinstance(token, str) or not 32 <= len(token) <= 256:
        raise RuntimeError("Local credential is missing or malformed")
    return hashlib.sha256(token.encode()).hexdigest()


def default_policy(cfg):
    return {"version": 1, "enabled": True, "orgId": dev.ORG, "principalId": dev.IDS["alice"],
            "scope": "scope_a", "tokenSha256": token_digest(cfg),
            "lifetimeHours": 720, "renewBeforeHours": 168, "checkIntervalSeconds": 60}


def validate_policy(policy, cfg):
    if not isinstance(policy, dict):
        raise RuntimeError("Invalid local credential renewal policy")
    expected = default_policy(cfg)
    for key in ("version", "orgId", "principalId", "scope", "tokenSha256"):
        if policy.get(key) != expected[key]:
            raise RuntimeError("Renewal policy no longer matches the enrolled local identity")
    if type(policy.get("enabled")) is not bool:
        raise RuntimeError("Invalid local credential renewal policy")
    bounds = {"lifetimeHours": (24, 2160), "renewBeforeHours": (1, 2160), "checkIntervalSeconds": (30, 3600)}
    for key, (low, high) in bounds.items():
        if type(policy.get(key)) is not int or not low <= policy[key] <= high:
            raise RuntimeError("Invalid local credential renewal schedule")
    if policy["renewBeforeHours"] >= policy["lifetimeHours"]:
        raise RuntimeError("Credential renewal margin must be shorter than its lifetime")


def save(name, value):
    # Atomic replacement keeps readers safe if a controller exits during a write.
    descriptor, temporary = tempfile.mkstemp(prefix=".credential-", dir=STATE)
    try:
        with os.fdopen(descriptor, "w") as output:
            os.fchmod(output.fileno(), 0o600)
            output.write(json.dumps(value, indent=2) + "\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, STATE / name)
    finally:
        Path(temporary).unlink(missing_ok=True)


def query(policy, extend=True):
    # UUIDs, digest and integer intervals have already been validated. SQL goes
    # over stdin, never process arguments; no plaintext token goes to PostgreSQL.
    sql = f"""BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='5s';
DO $$ BEGIN
  PERFORM 1 FROM mcpguard.authorization_heads WHERE org_id='{policy['orgId']}' FOR UPDATE;
END $$;
WITH candidate AS MATERIALIZED (
  SELECT * FROM mcpguard.principals
  WHERE org_id='{policy['orgId']}' AND id='{policy['principalId']}' FOR UPDATE
), extended AS (
  UPDATE mcpguard.principals p
  SET expires_at=clock_timestamp()+interval '{policy['lifetimeHours']} hours'
  FROM candidate c
  WHERE p.org_id=c.org_id AND p.id=c.id AND c.enabled AND c.can_read
    AND c.scope_id='{policy['scope']}' AND c.token_hash=decode('{policy['tokenSha256']}','hex')
    AND c.expires_at <= clock_timestamp()+interval '{policy['renewBeforeHours']} hours'
    AND {'TRUE' if extend else 'FALSE'}
  RETURNING p.expires_at
)
SELECT json_build_object(
 'status',CASE WHEN NOT EXISTS(SELECT 1 FROM candidate) THEN 'blocked'
    WHEN EXISTS(SELECT 1 FROM candidate WHERE NOT enabled OR NOT can_read
      OR scope_id<>'{policy['scope']}' OR token_hash<>decode('{policy['tokenSha256']}','hex')) THEN 'blocked'
    WHEN EXISTS(SELECT 1 FROM extended) THEN 'renewed' ELSE 'current' END,
 'reason',CASE WHEN NOT EXISTS(SELECT 1 FROM candidate) THEN 'identity_missing'
    WHEN EXISTS(SELECT 1 FROM candidate WHERE NOT enabled) THEN 'identity_disabled'
    WHEN EXISTS(SELECT 1 FROM candidate WHERE NOT can_read) THEN 'read_permission_changed'
    WHEN EXISTS(SELECT 1 FROM candidate WHERE scope_id<>'{policy['scope']}') THEN 'scope_changed'
    WHEN EXISTS(SELECT 1 FROM candidate WHERE token_hash<>decode('{policy['tokenSha256']}','hex')) THEN 'credential_replaced'
    ELSE NULL END,
 'checkedAt',clock_timestamp(), 'principalId','{policy['principalId']}',
 'previousExpiresAt',(SELECT expires_at FROM candidate),
 'expiresAt',coalesce((SELECT expires_at FROM extended),(SELECT expires_at FROM candidate)));
COMMIT;
"""
    result = dev.docker("exec", "-i", DB, "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1",
                        "-U", "postgres", "-d", "mcpguard_meta", input=sql,
                        capture_output=True, text=True, timeout=15)
    try:
        return json.loads(result.stdout)
    except (ValueError, TypeError):
        raise RuntimeError("Local credential renewal returned an invalid result") from None


def record_event(result):
    descriptor = os.open(STATE / "credential-events.jsonl", os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    with os.fdopen(descriptor, "w") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(json.dumps(result) + "\n")
        output.flush()
        os.fsync(output.fileno())


def maintain(cfg, force=False, source="supervisor"):
    global _next_check, _last_status
    path = STATE / POLICY
    if not path.exists():
        return {"status": "disabled"}
    try:
        policy = json.loads(path.read_text())
    except ValueError:
        raise RuntimeError("Local credential renewal policy is unreadable") from None
    validate_policy(policy, cfg)
    if not policy["enabled"]:
        return {"status": "disabled"}
    if not force and time.monotonic() < _next_check and _last_status is not None:
        return _last_status
    result = query(policy)
    result.update(source=source, lifetimeHours=policy["lifetimeHours"], renewBeforeHours=policy["renewBeforeHours"],
                  nextCheckAt=(datetime.now(timezone.utc) + timedelta(seconds=policy["checkIntervalSeconds"])).isoformat())
    if result["status"] == "renewed":
        record_event(result)
    save(STATUS, result)
    _last_status = result
    _next_check = time.monotonic() + policy["checkIntervalSeconds"]
    return result


def enable(cfg):
    policy = default_policy(cfg)
    validate_policy(policy, cfg)
    result = query(policy, extend=False)
    if result["status"] == "blocked":
        raise RuntimeError("Cannot enroll an unavailable or changed local read identity")
    previous = STATE / POLICY
    if previous.exists():
        save("credential-policy-before-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ") + ".json",
             json.loads(previous.read_text()))
    save(POLICY, policy)
    return maintain(cfg, force=True, source="enrollment")


def status():
    path = STATE / STATUS
    return json.loads(path.read_text()) if path.exists() else {"status": "not_configured"}
