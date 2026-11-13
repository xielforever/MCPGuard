"""Guards for the local service adoption boundary; no live Docker needed."""
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import local_service as service


class LocalServiceGuards(unittest.TestCase):
    def test_invalid_dsn_is_rejected_before_container_mutations(self):
        cfg = {"container": service.DB, "MCPGUARD_LISTEN": "127.0.0.1:18080", "MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_METADATA_DSN": "postgres://user:password@another-host/meta"}
        with tempfile.TemporaryDirectory() as directory, patch.object(service, "STATE", Path(directory)), patch.object(service.dev, "load", return_value=cfg), patch.object(service.dev, "inspect") as inspect:
            with self.assertRaises(RuntimeError):
                service.setup(18432)
            inspect.assert_not_called()

    def test_explicit_stop_prevents_supervisor_from_starting_containers(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(service, "STATE", Path(directory)), patch.object(service, "_ensure_running") as run:
            (Path(directory) / "stopped").touch()
            self.assertFalse(service.ensure())
            run.assert_not_called()

    def test_refuses_another_workload(self):
        with self.assertRaises(RuntimeError):
            service.owned({"Config": {"Labels": {"com.docker.compose.project": "other"}}}, "database")

    def test_accepts_only_fixture_or_managed_ownership(self):
        service.owned({"Config": {"Labels": {"mcpguard.fixture": "read-slice"}}}, "database")
        service.owned({"Config": {"Labels": {service.LABEL: "gateway"}}}, "gateway")
        with self.assertRaises(RuntimeError):
            service.owned({"Config": {"Labels": {service.LABEL: "database"}}}, "gateway")

    def test_dsn_preserves_credentials_database_and_options(self):
        self.assertEqual(service.fixed_dsn("postgres://reader:p%40ss@127.0.0.1:32768/business?sslmode=disable&application_name=demo", 18432),
                         "postgres://reader:p%40ss@127.0.0.1:18432/business?sslmode=disable&application_name=demo")

    def test_refuses_non_loopback_dsn(self):
        for dsn in ("postgres://reader:secret@db.example.com:5432/demo", "file:///tmp/db", "postgres://127.0.0.1/demo"):
            with self.subTest(dsn=dsn), self.assertRaises(RuntimeError):
                service.fixed_dsn(dsn, 18432)

    def test_refuses_public_or_oauth_fixture(self):
        cfg = {"container": service.DB, "MCPGUARD_LISTEN": "127.0.0.1:18080", "MCPGUARD_AUTH_MODE": "dev-tokens"}
        service.validate_fixture(cfg)
        for field, value in (("MCPGUARD_AUTH_MODE", "oauth-fixture"), ("MCPGUARD_LISTEN", "0.0.0.0:18080"), ("container", "other")):
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                service.validate_fixture({**cfg, field: value})

    def test_runtime_does_not_receive_admin_or_employee_credentials(self):
        cfg = {key: "runtime-only" for key in service.RUNTIME_KEYS}
        cfg.update(admin_password="private-admin", alice_token="private-employee", MCPGUARD_MIGRATION_DSN="private-admin-dsn")
        rendered = service.runtime_text(cfg)
        self.assertNotIn("private-", rendered)
        self.assertEqual(len(rendered.splitlines()), len(service.RUNTIME_KEYS))

    def test_fixed_database_port_rejects_public_or_random_bindings(self):
        for host, port in (("0.0.0.0", "18432"), ("127.0.0.1", "")):
            info = {"HostConfig": {"PortBindings": {"5432/tcp": [{"HostIp": host, "HostPort": port}]}}}
            self.assertNotEqual(service.database_port(info), "18432")


if __name__ == "__main__":
    unittest.main()
