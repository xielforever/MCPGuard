#!/usr/bin/env python3
"""Manage the existing loopback fixture without reseeding data or identities.

Run under WSL. The Windows launcher owns the foreground WSL lifetime.
Only explicitly owned containers are managed; archived containers stay stopped.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlsplit, urlunsplit

import dev
import local_credentials

ROOT = dev.ROOT
STATE = ROOT / ".dev/local-service"
GATEWAY = "mcpguard-local-gateway"
DB = "mcpguard-local-verified-postgres"
LABEL = "mcpguard.local-service"
HEALTH = "http://127.0.0.1:18080/healthz"
RUNTIME_KEYS = (
    "MCPGUARD_LISTEN", "MCPGUARD_AUTH_MODE", "MCPGUARD_METADATA_DSN",
    "MCPGUARD_BUSINESS_DSN", "MCPGUARD_ENCRYPTION_KEY", "MCPGUARD_PLAN_KEY",
)
OPTIONAL_RUNTIME_KEYS = ("MCPGUARD_NATIVE_CONFIG", "MCPGUARD_CONSOLE_CONFIG", "MCPGUARD_NOTIFICATION_CONFIG")


def managed_containers():
    result = [(DB, "database")]
    manifest = STATE / "controlled-services.json"
    if manifest.exists():
        selected = json.loads(manifest.read_text()).get("containers")
        expected = [{"name": "mcpguard-local-redis", "kind": "redis"},
                    {"name": "mcpguard-local-demo-http", "kind": "demo-http"}]
        if selected != expected:
            raise RuntimeError("Controlled service manifest is not an approved local fixture")
        result.extend((item["name"], item["kind"]) for item in selected)
    result.append((GATEWAY, "gateway"))
    return result


def stamp():
    return datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")


def save(path, value):
    dev.write_private(path, json.dumps(value, indent=2) + "\n")


def docker(*args):
    return dev.docker(*args, capture_output=True, text=True, timeout=40).stdout


def owned(info, kind):
    labels = info.get("Config", {}).get("Labels", {})
    if kind in ("redis", "demo-http"):
        if labels.get(LABEL) != kind:
            raise RuntimeError("Controlled service ownership mismatch")
        return
    expected = "read-slice" if kind == "database" else "local-runtime"
    if labels.get(LABEL) != kind and labels.get("mcpguard.fixture") != expected:
        raise RuntimeError("Container ownership mismatch; refusing to change it")


def validate_fixture(cfg):
    if cfg.get("container") != DB or cfg.get("MCPGUARD_LISTEN") != "127.0.0.1:18080" or cfg.get("MCPGUARD_AUTH_MODE") != "dev-tokens":
        raise RuntimeError("This launcher requires the existing local dev-tokens fixture")


def fixed_dsn(value, port):
    parsed = urlsplit(value)
    if parsed.scheme not in ("postgres", "postgresql") or parsed.hostname != "127.0.0.1" or "@" not in parsed.netloc:
        raise RuntimeError("Refusing to rewrite a non-loopback database DSN")
    authority = parsed.netloc.rsplit("@", 1)[0] + "@127.0.0.1:" + str(port)
    return urlunsplit((parsed.scheme, authority, parsed.path, parsed.query, parsed.fragment))


def runtime_text(cfg):
    return "".join(key + "=" + str(cfg[key]) + "\n" for key in RUNTIME_KEYS + OPTIONAL_RUNTIME_KEYS if key in cfg)


def database_port(info):
    ports = info.get("HostConfig", {}).get("PortBindings", {}).get("5432/tcp") or []
    if len(ports) != 1 or ports[0].get("HostIp") != "127.0.0.1":
        return None
    return ports[0].get("HostPort")


def ready_db():
    for _ in range(100):
        try:
            docker("exec", DB, "pg_isready", "-q", "-h", "127.0.0.1", "-p", "5432", "-U", "postgres")
            return
        except subprocess.CalledProcessError:
            time.sleep(0.2)
    raise RuntimeError("Local PostgreSQL did not become TCP-ready")


def health():
    try:
        with urllib.request.urlopen(HEALTH, timeout=2) as response:
            return response.status == 200 and json.load(response).get("auth") == "dev-tokens"
    except Exception:
        return False


def counts():
    sql = "SELECT json_build_object('actions',(SELECT count(*) FROM mcpguard.actions),'attempts',(SELECT count(*) FROM mcpguard.attempts),'executions',(SELECT count(*) FROM mcpguard.executions),'auditEvents',(SELECT count(*) FROM mcpguard.audit_events),'maxAuditSeq',(SELECT coalesce(max(seq),0) FROM mcpguard.audit_events));"
    return json.loads(docker("exec", DB, "psql", "-X", "-U", "postgres", "-d", "mcpguard_meta", "-At", "-c", sql))


@contextmanager
def operation_lock():
    with (STATE / "operation.lock").open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield


def _ensure_running():
    deployment = json.loads((STATE / "service.json").read_text())
    cfg = dev.load()
    validate_fixture(cfg)
    for name, kind in managed_containers():
        info = dev.inspect(name)
        if not info:
            raise RuntimeError("Managed local container is missing; run setup explicitly")
        owned(info, kind)
        if kind == "database" and database_port(info) != str(deployment["databasePort"]):
            raise RuntimeError("Local database port binding changed")
        if info["HostConfig"]["RestartPolicy"]["Name"] != "unless-stopped":
            docker("update", "--restart", "unless-stopped", name)
        if not info["State"]["Running"]:
            docker("start", name)
        if kind == "database":
            ready_db()
            local_credentials.maintain(cfg)
    for _ in range(60):
        if health():
            return
        time.sleep(0.25)
    raise RuntimeError("执契本机服务未通过健康检查")


def ensure():
    with operation_lock():
        if (STATE / "stopped").exists():
            return False
        _ensure_running()
        return True


def stop():
    (STATE / "stopped").touch(mode=0o600)
    with operation_lock():
        for name, kind in reversed(managed_containers()):
            info = dev.inspect(name)
            if info:
                owned(info, kind)
                if info["State"]["Running"]:
                    docker("stop", name)


def setup(port):
    STATE.mkdir(parents=True, mode=0o700, exist_ok=True)
    os.chmod(STATE, 0o700)
    if (STATE / "service.json").exists():
        config = json.loads((STATE / "service.json").read_text())
        if config["databasePort"] != port:
            raise RuntimeError("Existing deployment uses another fixed port")
        ensure()
        return config
    cfg = dev.load()
    validate_fixture(cfg)
    fixed_cfg = dict(cfg)
    for key in ("MCPGUARD_METADATA_DSN", "MCPGUARD_BUSINESS_DSN", "MCPGUARD_MIGRATION_DSN", "MCPGUARD_TEST_BUSINESS_ADMIN_DSN"):
        fixed_cfg[key] = fixed_dsn(cfg[key], port)
    old_db, old_gateway = dev.inspect(DB), dev.inspect(GATEWAY)
    if not old_db or not old_gateway:
        raise RuntimeError("Existing fixture containers are required; no fresh database is created")
    owned(old_db, "database")
    owned(old_gateway, "gateway")
    volumes = [v for v in old_db["Mounts"] if v["Destination"] == "/var/lib/postgresql/data"]
    if len(volumes) != 1 or volumes[0]["Type"] != "volume":
        raise RuntimeError("Expected one existing PostgreSQL data volume")
    if not (ROOT / "bin/mcpguard").is_file():
        raise RuntimeError("Build the gateway before installing its local service")
    # A busy fixed port is an error; never stop the service which owns it.
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", port))
    archive_stamp = stamp().lower()
    backup = STATE / ("before-" + archive_stamp)
    backup.mkdir(mode=0o700)
    shutil.copy2(ROOT / ".dev/env.json", backup / "env.json")
    snapshot = [{"name": i["Name"], "state": i["State"]["Status"], "image": i["Image"], "restart": i["HostConfig"]["RestartPolicy"]["Name"]} for i in (old_db, old_gateway)]
    save(backup / "containers.json", snapshot)
    if old_gateway["State"]["Running"]:
        docker("stop", GATEWAY)
    if not old_db["State"]["Running"]:
        docker("start", DB)
    ready_db()
    before = counts()
    save(backup / "ledger-counts.json", before)
    docker("stop", DB)
    archived_db = DB + "-before-" + archive_stamp
    archived_gateway = GATEWAY + "-before-" + archive_stamp
    docker("rename", DB, archived_db)
    docker("rename", GATEWAY, archived_gateway)
    cfg = fixed_cfg
    dev.write_private(STATE / "postgres.env", "POSTGRES_PASSWORD=" + cfg["admin_password"] + "\n")
    docker("run", "-d", "--name", DB, "--restart", "unless-stopped",
           "--label", "mcpguard.fixture=read-slice", "--label", LABEL + "=database",
           "--env-file", str(STATE / "postgres.env"), "-p", f"127.0.0.1:{port}:5432",
           "--mount", "type=volume,src=" + volumes[0]["Name"] + ",dst=/var/lib/postgresql/data",
           old_db["Config"]["Image"])
    ready_db()
    if counts() != before:
        raise RuntimeError("Existing ledger counts changed during database migration; evidence preserved")
    save(ROOT / ".dev/env.json", cfg)
    dev.write_private(STATE / "runtime.env", runtime_text(cfg))
    docker("run", "-d", "--name", GATEWAY, "--restart", "unless-stopped", "--network", "host",
           "--label", LABEL + "=gateway", "--label", "mcpguard.fixture=local-runtime",
           "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
           "--env-file", str(STATE / "runtime.env"),
           "--mount", "type=bind,src=" + str(ROOT / "bin/mcpguard") + ",dst=/usr/local/bin/mcpguard,readonly",
           old_gateway["Image"], "/usr/local/bin/mcpguard")
    deployment = {"installedAt": stamp(), "endpoint": "http://127.0.0.1:18080/mcp", "databasePort": port,
                  "dataVolume": volumes[0]["Name"], "gatewayImage": old_gateway["Image"],
                  "archivedContainers": [archived_db, archived_gateway], "backupDirectory": str(backup),
                  "ledgerBefore": before, "historicalDataPreserved": True}
    save(STATE / "service.json", deployment)
    ensure()
    return deployment


def status():
    containers = []
    for name, _ in managed_containers():
        info = dev.inspect(name)
        containers.append({"name": name, "state": info["State"]["Status"] if info else "missing",
                           "startedAt": info["State"]["StartedAt"] if info else None,
                           "restartPolicy": info["HostConfig"]["RestartPolicy"]["Name"] if info else None})
    return {"capturedAt": stamp(), "endpoint": "http://127.0.0.1:18080/mcp", "healthy": health(),
            "desiredRunning": not (STATE / "stopped").exists(), "containers": containers,
            "credentialRenewal": local_credentials.status()}


def serve():
    # flock releases on process death, including WSL termination. No stale PID lock.
    with (STATE / "supervisor.lock").open("a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        failures = 0
        while not (STATE / "stopped").exists():
            try:
                if not ensure():
                    return
                save(STATE / "heartbeat.json", {"at": stamp(), "pid": os.getpid(), "healthy": True, "recoveryFailures": failures})
            except (RuntimeError, OSError, subprocess.SubprocessError) as error:
                failures += 1
                # Never log subprocess output or config/DSN values.
                save(STATE / "heartbeat.json", {"at": stamp(), "pid": os.getpid(), "healthy": False,
                                                "recoveryFailures": failures, "errorType": type(error).__name__})
            for _ in range(10):
                if (STATE / "stopped").exists():
                    return
                time.sleep(0.5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["setup", "start", "stop", "restart", "serve", "status", "enable-renewal", "renew"])
    parser.add_argument("--database-port", type=int, default=18432)
    args = parser.parse_args()
    if not 1024 <= args.database_port <= 65535:
        parser.error("database port must be between 1024 and 65535")
    if args.action == "setup":
        print(json.dumps(setup(args.database_port), indent=2))
    elif args.action == "serve":
        serve()
    elif args.action == "status":
        print(json.dumps(status(), indent=2))
    elif args.action in ("enable-renewal", "renew"):
        with operation_lock():
            if (STATE / "stopped").exists():
                raise RuntimeError("Start the local service before maintaining its credential")
            cfg = dev.load()
            validate_fixture(cfg)
            info = dev.inspect(DB)
            if not info or not info["State"]["Running"]:
                raise RuntimeError("Local PostgreSQL is not running")
            owned(info, "database")
            deployment = json.loads((STATE / "service.json").read_text())
            if database_port(info) != str(deployment["databasePort"]):
                raise RuntimeError("Local database port binding changed")
            result = local_credentials.enable(cfg) if args.action == "enable-renewal" else local_credentials.maintain(cfg, force=True, source="connection")
            print(json.dumps(result, indent=2))
            if result["status"] == "blocked":
                raise RuntimeError("Enrolled local identity was disabled or changed; renewal refused")
    else:
        if args.action in ("stop", "restart"):
            stop()
        if args.action in ("start", "restart"):
            (STATE / "stopped").unlink(missing_ok=True)
            ensure()
        print(json.dumps(status(), indent=2))


if __name__ == "__main__":
    try:
        main()
    except RuntimeError as error:
        # These messages are fixed operator diagnostics, without native errors.
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
    except (OSError, subprocess.SubprocessError) as error:
        print("Local service operation failed (" + type(error).__name__ + "); private credentials and subprocess output suppressed.", file=sys.stderr)
        raise SystemExit(1)
