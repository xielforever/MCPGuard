#!/usr/bin/env python3
"""Local read-slice fixture. No LLM, IdP, Python packages or host psql required."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / ".dev"
IMAGE = "postgres:16.14-alpine@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777"
LABEL = "mcpguard.fixture=read-slice"
ORG = "10000000-0000-4000-8000-000000000001"
IDS = {"alice": "20000000-0000-4000-8000-000000000001", "bob": "20000000-0000-4000-8000-000000000002", "visitor": "20000000-0000-4000-8000-000000000003"}


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def docker(*args, **kwargs):
    # This workspace exposes a local daemon. Preserve Docker credential config.
    env = os.environ.copy()
    for k in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
        env.pop(k, None)
    return run(["docker", "--host=unix:///var/run/docker.sock", *args], env=env, **kwargs)


def load():
    try:
        return json.loads((STATE / "env.json").read_text())
    except FileNotFoundError:
        raise SystemExit("Run: python3 scripts/dev.py up")


def write_private(path, text):
    # Permissions apply even when replacing an existing file.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(text)


def inspect(name):
    try:
        return json.loads(docker("inspect", name, capture_output=True, text=True).stdout)[0]
    except subprocess.CalledProcessError:
        return None


def check_owned(info):
    if info.get("Config", {}).get("Labels", {}).get("mcpguard.fixture") != "read-slice":
        raise SystemExit("Container name belongs to another workload; refusing to modify it")


def psql(cfg, database, sql):
    return docker("exec", "-i", cfg["container"], "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", database, input=sql, capture_output=True, text=True)


def go_env():
    env = os.environ.copy()
    env.setdefault("GOTOOLCHAIN", "go1.26.8")
    return env


def build():
    compiler = os.environ.get("GO", "go")
    run([compiler, "version"], env=go_env())
    (ROOT / "bin").mkdir(exist_ok=True)
    for name in ("mcpguard", "mcpguard-connect"):
        run([compiler, "build", "-p", "2", "-o", "bin/" + name, "./cmd/" + name], env=go_env())


def runtime_env(cfg):
    env = os.environ.copy()
    # The running gateway does not inherit fixture admin DSNs or employee tokens.
    for k in list(env):
        if k.startswith("MCPGUARD_"):
            env.pop(k)
    env.update({k: str(v) for k, v in cfg.items() if k in {"MCPGUARD_LISTEN", "MCPGUARD_METADATA_DSN", "MCPGUARD_BUSINESS_DSN", "MCPGUARD_ENCRYPTION_KEY", "MCPGUARD_PLAN_KEY", "MCPGUARD_AUTH_MODE"}})
    return env


def up():
    STATE.mkdir(mode=0o700, exist_ok=True)
    os.chmod(STATE, 0o700)
    cfg = load() if (STATE / "env.json").exists() else {
        "container": "mcpguard-dev-postgres",
        "admin_password": secrets.token_hex(24), "app_password": secrets.token_hex(24), "reader_password": secrets.token_hex(24),
        "MCPGUARD_ENCRYPTION_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        "MCPGUARD_PLAN_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        "MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_LISTEN": "127.0.0.1:8080",
        **{name + "_token": secrets.token_urlsafe(32) for name in IDS},
    }
    write_private(STATE / "env.json", json.dumps(cfg, indent=2) + "\n")
    write_private(STATE / "postgres.env", "POSTGRES_PASSWORD=" + cfg["admin_password"] + "\n")
    info = inspect(cfg["container"])
    if info:
        check_owned(info)
        if not info["State"]["Running"]:
            docker("start", cfg["container"], capture_output=True)
    else:
        docker("run", "-d", "--name", cfg["container"], "--label", LABEL,
               "--env-file", str(STATE / "postgres.env"), "-p", "127.0.0.1::5432", IMAGE, capture_output=True)
    info = inspect(cfg["container"])
    port = info["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
    for _ in range(100):
        try:
            # The image's temporary init server accepts Unix sockets before it
            # shuts down. TCP opens only after initialization has completed.
            docker("exec", cfg["container"], "pg_isready", "-q", "-h", "127.0.0.1", "-p", "5432", "-U", "postgres", capture_output=True, text=True)
            psql(cfg, "postgres", "SELECT 1;")
            break
        except subprocess.CalledProcessError:
            time.sleep(0.1)
    else:
        raise SystemExit("Fixture PostgreSQL did not become ready")
    # All interpolated passwords/tokens are generated hex/base64url, never user SQL.
    sql = ""
    for role, key in (("mcpguard_app", "app_password"), ("mcpguard_reader", "reader_password")):
        sql += f"DO $$ BEGIN IF NOT EXISTS(SELECT FROM pg_roles WHERE rolname='{role}') THEN CREATE ROLE {role} LOGIN; END IF; END $$;\n"
        sql += f"ALTER ROLE {role} WITH NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg[key]}';\n"
    sql += "SELECT 'CREATE DATABASE mcpguard_meta' WHERE NOT EXISTS(SELECT FROM pg_database WHERE datname='mcpguard_meta') \\gexec\n"
    sql += "SELECT 'CREATE DATABASE mcpguard_business' WHERE NOT EXISTS(SELECT FROM pg_database WHERE datname='mcpguard_business') \\gexec\n"
    sql += "REVOKE ALL ON DATABASE mcpguard_meta,mcpguard_business FROM PUBLIC; GRANT CONNECT ON DATABASE mcpguard_meta TO mcpguard_app; GRANT CONNECT ON DATABASE mcpguard_business TO mcpguard_reader;\n"
    psql(cfg, "postgres", sql)
    addr = "127.0.0.1:" + port
    cfg.update({
        "MCPGUARD_METADATA_DSN": f"postgres://mcpguard_app:{cfg['app_password']}@{addr}/mcpguard_meta?sslmode=disable",
        "MCPGUARD_BUSINESS_DSN": f"postgres://mcpguard_reader:{cfg['reader_password']}@{addr}/mcpguard_business?sslmode=disable",
        "MCPGUARD_MIGRATION_DSN": f"postgres://postgres:{cfg['admin_password']}@{addr}/mcpguard_meta?sslmode=disable",
        "MCPGUARD_TEST_BUSINESS_ADMIN_DSN": f"postgres://postgres:{cfg['admin_password']}@{addr}/mcpguard_business?sslmode=disable",
    })
    # Save early so a failed build/bootstrap can be retried with the same secrets.
    write_private(STATE / "env.json", json.dumps(cfg, indent=2) + "\n")
    build()
    env = runtime_env(cfg)
    env["MCPGUARD_MIGRATION_DSN"] = cfg["MCPGUARD_MIGRATION_DSN"]
    run([str(ROOT / "bin/mcpguard"), "migrate"], env=env)
    run([str(ROOT / "bin/mcpguard"), "migrate"], env=env)  # Idempotent checksum verification.
    psql(cfg, "mcpguard_business", (ROOT / "scripts/fixtures/business.sql").read_text())
    sql = f"BEGIN; INSERT INTO mcpguard.authorization_heads(org_id) VALUES('{ORG}') ON CONFLICT DO NOTHING; SELECT org_id FROM mcpguard.authorization_heads WHERE org_id='{ORG}' FOR UPDATE;\n"
    for name, ident in IDS.items():
        digest = hashlib.sha256(cfg[name + "_token"].encode()).hexdigest()
        scope = "scope_b" if name == "bob" else "scope_a"
        allowed = "false" if name == "visitor" else "true"
        sql += f"INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,can_read,scope_id) VALUES('{ORG}','{ident}','{name}',decode('{digest}','hex'),clock_timestamp()+interval '24 hours',{allowed},'{scope}') ON CONFLICT(org_id,id) DO UPDATE SET token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,enabled=true,can_read=EXCLUDED.can_read,scope_id=EXCLUDED.scope_id;\n"
    sql += "COMMIT;\n" + (ROOT / "scripts/fixtures/grants.sql").read_text()
    psql(cfg, "mcpguard_meta", sql)
    print("PostgreSQL 16.14 ready; migrations 1-2 verified; Alice/scope_a, Bob/scope_b, Visitor/no tools. Secrets: .dev/env.json (0600).")


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def test():
    cfg = load()
    build()
    env = go_env()
    env.update(runtime_env(cfg))
    env["MCPGUARD_TEST_METADATA_ADMIN_DSN"] = cfg["MCPGUARD_MIGRATION_DSN"]
    env["MCPGUARD_TEST_BUSINESS_ADMIN_DSN"] = cfg["MCPGUARD_TEST_BUSINESS_ADMIN_DSN"]
    for name in IDS:
        env["MCPGUARD_TEST_" + name.upper() + "_TOKEN"] = cfg[name + "_token"]
    run([os.environ.get("GO", "go"), "test", "-race", "-p", "2", "-count=1", "./..."], env=env)
    run([sys.executable, "tests/e2e.py"], env=env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["up", "run", "test", "down", "build", "connect"])
    parser.add_argument("--employee", choices=list(IDS), default="alice")
    args = parser.parse_args()
    if args.command == "up":
        up()
    elif args.command == "build":
        build()
    elif args.command == "test":
        test()
    elif args.command == "run":
        os.execve(str(ROOT / "bin/mcpguard"), ["mcpguard"], runtime_env(load()))
    elif args.command == "connect":
        cfg = load()
        env = os.environ.copy()
        env["MCPGUARD_TOKEN"] = cfg[args.employee + "_token"]
        os.execve(str(ROOT / "bin/mcpguard-connect"), ["mcpguard-connect", "-gateway", "http://" + cfg["MCPGUARD_LISTEN"] + "/mcp", "-allow-loopback-http"], env)
    else:
        cfg = load()
        info = inspect(cfg["container"])
        if info:
            check_owned(info)
            docker("rm", "-fv", cfg["container"], capture_output=True)
        shutil.rmtree(STATE)
        print("Removed this project's local PostgreSQL fixture and .dev secrets.")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as exc:
        # Captured SQL/DSN details can contain secrets; do not dump them.
        raise SystemExit(f"Local setup/check failed: executable={Path(exc.cmd[0]).name}, exit={exc.returncode}. Secrets were not printed.")
