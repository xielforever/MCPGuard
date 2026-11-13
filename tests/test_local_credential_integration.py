"""Opt-in guard checks against the enrolled local development database.

Temporarily changes only Alice's authorization fields, restoring them in cleanup.
Run with MCPGUARD_TEST_LOCAL_RENEWAL=1 after enabling the local renewal policy.
"""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import dev
import local_credentials as credentials
import local_service as service


def sql_json(sql):
    result = dev.docker("exec", "-i", service.DB, "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1",
                        "-U", "postgres", "-d", "mcpguard_meta", input=sql,
                        capture_output=True, text=True, timeout=15)
    return json.loads(result.stdout)


def snapshot():
    return sql_json("SELECT json_build_object('principals',(SELECT json_agg(row_to_json(p) ORDER BY id) FROM "
                    "(SELECT org_id,id,enabled,can_read,scope_id,expires_at,encode(token_hash,'hex') AS digest FROM mcpguard.principals) p),"
                    "'epoch',(SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id='" + dev.ORG + "'));" )


@unittest.skipUnless(os.environ.get("MCPGUARD_TEST_LOCAL_RENEWAL") == "1", "requires opt-in local fixture mutation")
class LocalCredentialDatabaseGuards(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(service.operation_lock())
        self.cfg = dev.load()
        service.validate_fixture(self.cfg)
        info = dev.inspect(service.DB)
        service.owned(info, "database")
        self.assertTrue(info["State"]["Running"])
        self.policy = json.loads((credentials.STATE / credentials.POLICY).read_text())
        credentials.validate_policy(self.policy, self.cfg)
        self.before = snapshot()
        self.alice = next(p for p in self.before["principals"] if p["id"] == dev.IDS["alice"])
        # This backup remains private; assertions avoid printing credential hashes.
        dev.write_private(credentials.STATE / "guard-test-before.json", json.dumps(self.before, indent=2) + "\n")
        self.addCleanup(self.restore)

    def restore(self):
        row = self.alice
        # Values came from this owned fixture, never MCP request parameters.
        scope = row["scope_id"].replace("'", "''")
        expiry = row["expires_at"].replace("'", "''")
        if snapshot()["principals"] != self.before["principals"]:
            sql_json("UPDATE mcpguard.principals SET enabled=" + str(row["enabled"]).lower() +
                     ",can_read=" + str(row["can_read"]).lower() + ",scope_id='" + scope +
                     "',expires_at='" + expiry + "',token_hash=decode('" + row["digest"] + "','hex') "
                     "WHERE org_id='" + dev.ORG + "' AND id='" + dev.IDS["alice"] + "'; SELECT to_json(true);")
        self.assertTrue(snapshot()["principals"] == self.before["principals"], "Fixture identity cleanup failed")

    def assert_blocked(self, assignment, reason):
        sql_json("UPDATE mcpguard.principals SET expires_at=clock_timestamp()-interval '1 hour'," + assignment +
                 " WHERE org_id='" + dev.ORG + "' AND id='" + dev.IDS["alice"] + "'; SELECT to_json(true);")
        changed = snapshot()
        result = credentials.maintain(self.cfg, force=True, source="guard-test")
        self.assertEqual(result["status"], "blocked")
        self.assertEqual(result["reason"], reason)
        self.assertTrue(snapshot() == changed, "Blocked renewal changed authorization state")

    def test_disabled_employee_is_not_reenabled_or_extended(self):
        self.assert_blocked("enabled=false", "identity_disabled")

    def test_revoked_read_permission_is_not_restored(self):
        self.assert_blocked("can_read=false", "read_permission_changed")

    def test_changed_scope_is_not_restored(self):
        self.assert_blocked("scope_id='scope_b'", "scope_changed")

    def test_replaced_credential_is_not_resurrected(self):
        self.assert_blocked("token_hash=decode('" + "a1" * 32 + "','hex')", "credential_replaced")

    def test_current_credential_check_is_a_database_noop(self):
        result = credentials.maintain(self.cfg, force=True, source="guard-test")
        self.assertEqual(result["status"], "current")
        self.assertTrue(snapshot() == self.before, "Current credential check changed authorization state")


if __name__ == "__main__":
    unittest.main()
