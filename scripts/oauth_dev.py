#!/usr/bin/env python3
"""Opt-in OAuth/HTTPS fixture, isolated from scripts/dev.py's local test mode.

Use a random, temporary HTTPS ingress forwarding ONLY 127.0.0.1:18081.
All generated credentials stay under ignored .dev/oauth; never paste its files
into chats or commit them. This is a test IdP, not enterprise SSO.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
from urllib.parse import urlsplit

import dev

STATE = dev.ROOT / ".dev/oauth"
CALLBACK = "https://chatgpt.com/connector_platform_oauth_redirect"


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def read(name):
    return json.loads((STATE / name).read_text())


def save(name, value):
    dev.write_private(STATE / name, json.dumps(value, indent=2) + "\n")


def up():
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not (STATE / "env.json").exists():
        save("env.json", {
            "container": "mcpguard-oauth-postgres",
            **{k: secrets.token_hex(24) for k in ("admin_password", "app_password", "reader_password")},
            **{k: base64.b64encode(secrets.token_bytes(32)).decode() for k in ("MCPGUARD_ENCRYPTION_KEY", "MCPGUARD_PLAN_KEY")},
            "MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_LISTEN": "127.0.0.1:18081",
            **{name + "_token": secrets.token_urlsafe(32) for name in dev.IDS},
        })
    dev.STATE = STATE
    dev.up()
    if not (STATE / "credentials.json").exists():
        save("credentials.json", {
            "client_id": "mcpguard-chatgpt-local",
            "client_secret": secrets.token_urlsafe(32),
            "passwords": {name: secrets.token_urlsafe(32) for name in dev.IDS},
        })
    print("Isolated OAuth database and generated test credentials ready in .dev/oauth (private).")


def configure(origin, redirect=None):
    u = urlsplit(origin)
    if u.scheme != "https" or not u.netloc or u.path or u.query or u.fragment or u.username:
        raise SystemExit("--origin must be an exact HTTPS origin, without trailing slash")
    credentials = read("credentials.json")
    save("server.json", {
        "public_origin": origin,
        "client_id": credentials["client_id"],
        "client_secret_sha256": digest(credentials["client_secret"]),
        "redirect_uris": [redirect or CALLBACK],
        "users": [{"username": name, "password_sha256": digest(credentials["passwords"][name]),
                   "org_id": dev.ORG, "principal_id": ident} for name, ident in dev.IDS.items()],
    })
    print("OAuth HTTPS resource configured:", origin + "/mcp")


def runtime():
    cfg = read("env.json")
    env = dev.runtime_env(cfg)
    env["MCPGUARD_AUTH_MODE"] = "oauth-fixture"
    env["MCPGUARD_OAUTH_FIXTURE_CONFIG"] = str(STATE / "server.json")
    return env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["up", "configure", "run", "status"])
    parser.add_argument("--origin")
    parser.add_argument("--redirect", help="Exact callback actually displayed by the ChatGPT connection UI")
    args = parser.parse_args()
    if args.command == "up":
        up()
    elif args.command == "configure":
        if not args.origin:
            parser.error("configure requires --origin")
        configure(args.origin, args.redirect)
    elif args.command == "run":
        # No database admin credentials or login/client secrets in argv or env.
        os.execve(str(dev.ROOT / "bin/mcpguard"), ["mcpguard"], runtime())
    else:
        cfg, server = read("env.json"), read("server.json")
        print(json.dumps({"listen": cfg["MCPGUARD_LISTEN"], "resource": server["public_origin"] + "/mcp",
                          "client_id": server["client_id"], "redirect_uris": server["redirect_uris"],
                          "authentication": "oauth-fixture", "secrets_printed": False}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as exc:
        raise SystemExit(f"OAuth fixture command failed: executable={Path(exc.cmd[0]).name}, exit={exc.returncode}; secret details suppressed.")
