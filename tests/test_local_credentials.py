"""Renewal enrollment, revocation boundaries and scheduling without live Docker."""
from contextlib import ExitStack
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import local_credentials as credentials


class LocalCredentialGuards(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        directory = self.stack.enter_context(tempfile.TemporaryDirectory())
        self.state = Path(directory)
        self.stack.enter_context(patch.object(credentials, "STATE", self.state))
        self.stack.enter_context(patch.object(credentials, "_next_check", 0.0))
        self.stack.enter_context(patch.object(credentials, "_last_status", None))
        self.cfg = {"container": credentials.DB, "MCPGUARD_AUTH_MODE": "dev-tokens",
                    "MCPGUARD_LISTEN": "127.0.0.1:18080", "alice_token": "unit-test-private-token-" + "a" * 32}
        self.policy = credentials.default_policy(self.cfg)

    def enroll(self):
        credentials.save(credentials.POLICY, self.policy)

    def test_disabled_by_default_without_operator_enrollment(self):
        with patch.object(credentials, "query") as query:
            self.assertEqual(credentials.maintain(self.cfg), {"status": "disabled"})
            query.assert_not_called()

    def test_enrollment_binds_one_identity_and_never_stores_plaintext(self):
        self.assertEqual(self.policy["principalId"], credentials.dev.IDS["alice"])
        self.assertEqual(self.policy["tokenSha256"], hashlib.sha256(self.cfg["alice_token"].encode()).hexdigest())
        self.enroll()
        self.assertNotIn(self.cfg["alice_token"], (self.state / credentials.POLICY).read_text())

    def test_refuses_other_server_or_auth_mode(self):
        for key, value in (("container", "another-project"), ("MCPGUARD_LISTEN", "0.0.0.0:18080"),
                           ("MCPGUARD_AUTH_MODE", "oauth-fixture"), ("alice_token", "short")):
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                credentials.default_policy({**self.cfg, key: value})

    def test_policy_cannot_select_other_employee_or_scope(self):
        for key, value in (("orgId", "another-org"), ("principalId", credentials.dev.IDS["bob"]),
                           ("scope", "scope_b"), ("tokenSha256", "0" * 64)):
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                credentials.validate_policy({**self.policy, key: value}, self.cfg)

    def test_refuses_unsafe_schedule_or_malformed_policy(self):
        invalid = [[], {**self.policy, "enabled": 1}, {**self.policy, "lifetimeHours": "720; SQL"},
                   {**self.policy, "lifetimeHours": True}, {**self.policy, "renewBeforeHours": 720},
                   {**self.policy, "checkIntervalSeconds": 0}]
        for policy in invalid:
            with self.subTest(policy=policy), self.assertRaises(RuntimeError):
                credentials.validate_policy(policy, self.cfg)

    def test_changed_private_token_cannot_silently_reenroll(self):
        self.enroll()
        with patch.object(credentials, "query") as query:
            with self.assertRaises(RuntimeError):
                credentials.maintain({**self.cfg, "alice_token": "different-private-token-" + "b" * 32}, force=True)
            query.assert_not_called()

    def test_explicit_policy_disable_prevents_database_access(self):
        self.policy["enabled"] = False
        self.enroll()
        with patch.object(credentials, "query") as query:
            self.assertEqual(credentials.maintain(self.cfg, force=True)["status"], "disabled")
            query.assert_not_called()

    def test_refuses_enrollment_of_disabled_or_changed_identity(self):
        with patch.object(credentials, "query", return_value={"status": "blocked"}):
            with self.assertRaises(RuntimeError):
                credentials.enable(self.cfg)
        self.assertFalse((self.state / credentials.POLICY).exists())

    def test_background_checks_are_throttled_but_connection_forces_check(self):
        self.enroll()
        with patch.object(credentials, "query", return_value={"status": "current"}) as query:
            credentials.maintain(self.cfg)
            credentials.maintain(self.cfg)
            self.assertEqual(query.call_count, 1)
            credentials.maintain(self.cfg, force=True, source="connection")
            self.assertEqual(query.call_count, 2)
        self.assertEqual(credentials.status()["source"], "connection")

    def test_background_schedule_becomes_due_without_a_connection(self):
        self.enroll()
        with patch.object(credentials, "query", return_value={"status": "current"}) as query:
            with patch.object(credentials.time, "monotonic", return_value=100.0):
                credentials.maintain(self.cfg)
            with patch.object(credentials.time, "monotonic", return_value=161.0):
                credentials.maintain(self.cfg)
            self.assertEqual(query.call_count, 2)

    def test_only_actual_renewal_appends_credential_event(self):
        self.enroll()
        with patch.object(credentials, "query", return_value={"status": "current"}):
            credentials.maintain(self.cfg, force=True)
        self.assertFalse((self.state / "credential-events.jsonl").exists())
        with patch.object(credentials, "query", return_value={"status": "renewed"}):
            credentials.maintain(self.cfg, force=True)
        event = json.loads((self.state / "credential-events.jsonl").read_text())
        self.assertEqual(event["status"], "renewed")
        self.assertNotIn(self.cfg["alice_token"], json.dumps(event))


if __name__ == "__main__":
    unittest.main()
