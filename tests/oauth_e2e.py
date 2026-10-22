#!/usr/bin/env python3
"""Independent OAuth + Streamable HTTP + real PostgreSQL fixture verification.

Run after scripts/oauth_dev.py up/configure. --url can verify the same fixture
through its real HTTPS ingress. Credentials and tokens are never printed.
"""
import argparse
import base64
from http.client import HTTPConnection, HTTPSConnection
import hashlib
import json
from pathlib import Path
import re
import secrets
import signal
import subprocess
import sys
import time
from urllib.parse import urlencode, urlsplit, parse_qs

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import oauth_dev as fixture

CHECKS = []
PROFILE = "2025-11-25"


def check(value, name):
    if not value:
        raise AssertionError(name)
    CHECKS.append(name)


class Client:
    def __init__(self, base):
        self.base = urlsplit(base)
        self.server = fixture.read("server.json")
        self.credentials = fixture.read("credentials.json")

    def request(self, method, path, body=None, headers=None):
        conn_type = HTTPSConnection if self.base.scheme == "https" else HTTPConnection
        conn = conn_type(self.base.hostname, self.base.port, timeout=30)
        try:
            conn.request(method, path, body, headers or {})
            r = conn.getresponse()
            return r.status, {k.lower(): v for k, v in r.getheaders()}, r.read()
        finally:
            conn.close()

    def post(self, path, data):
        status, headers, body = self.request("POST", path, urlencode(data), {"Content-Type": "application/x-www-form-urlencoded"})
        return status, json.loads(body) if body else {}

    def grant(self, employee):
        verifier, state = secrets.token_urlsafe(32), secrets.token_urlsafe(24)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
        params = {"response_type": "code", "client_id": self.credentials["client_id"],
                  "redirect_uri": self.server["redirect_uris"][0], "scope": "mcp:read", "state": state,
                  "resource": self.server["public_origin"] + "/mcp", "code_challenge_method": "S256", "code_challenge": challenge}
        status, headers, body = self.request("GET", "/oauth/authorize?" + urlencode(params))
        check(status == 200 and "Secure" in headers["set-cookie"] and "HttpOnly" in headers["set-cookie"], "secure login form: " + employee)
        csrf = re.search(r'name="csrf" value="([^"]+)"', body.decode()).group(1)
        status, headers, body = self.request("POST", "/oauth/authorize", urlencode({"csrf": csrf, "username": employee,
            "password": self.credentials["passwords"][employee], "decision": "allow"}), {
            "Content-Type": "application/x-www-form-urlencoded", "Cookie": headers["set-cookie"].split(";", 1)[0], "Origin": self.server["public_origin"]})
        check(status == 303, "explicit test login and consent: " + employee)
        callback = urlsplit(headers["location"])
        q = parse_qs(callback.query)
        check(q["state"] == [state] and q["iss"] == [self.server["public_origin"]], "OAuth callback issuer/state: " + employee)
        status, tokens = self.post("/oauth/token", {"grant_type": "authorization_code", "code": q["code"][0],
            "redirect_uri": self.server["redirect_uris"][0], "resource": params["resource"], "code_verifier": verifier,
            "client_id": self.credentials["client_id"], "client_secret": self.credentials["client_secret"]})
        check(status == 200 and tokens["scope"] == "mcp:read", "PKCE code exchange: " + employee)
        return tokens

    def rpc(self, access, method, params=None, session=None, ident="oauth-real-read"):
        headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Authorization": "Bearer " + access}
        if session:
            headers.update({"Mcp-Session-Id": session, "MCP-Protocol-Version": PROFILE})
        envelope = {"jsonrpc": "2.0", "method": method}
        if ident is not None:
            envelope["id"] = ident
        if params is not None:
            envelope["params"] = params
        status, h, raw = self.request("POST", "/mcp", json.dumps(envelope), headers)
        return status, h, json.loads(raw) if raw and h.get("content-type", "").startswith("application/json") else None

    def initialize(self, access):
        status, headers, value = self.rpc(access, "initialize", {"protocolVersion": PROFILE, "capabilities": {}, "clientInfo": {"name": "oauth-independent-python", "version": "1"}})
        check(status == 200 and value["result"]["protocolVersion"] == PROFILE, "OAuth MCP initialization")
        session = headers["mcp-session-id"]
        check(self.rpc(access, "notifications/initialized", session=session, ident=None)[0] == 202, "OAuth MCP initialized notification")
        return session


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", help="Existing server base URL, HTTPS for ingress proof")
    parser.add_argument("--output", default=".dev/oauth/e2e-result.json")
    args = parser.parse_args()
    process = log = None
    try:
        if not args.url:
            cfg = fixture.read("env.json")
            args.url = "http://" + cfg["MCPGUARD_LISTEN"]
            log = open(fixture.STATE / "gateway-e2e.log", "a")
            process = subprocess.Popen([str(ROOT / "bin/mcpguard")], env=fixture.runtime(), stdout=log, stderr=log)
        client = Client(args.url)
        for _ in range(100):
            try:
                if client.request("GET", "/healthz")[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(.1)
        else:
            raise AssertionError("OAuth fixture not ready")
        status, headers, _ = client.request("POST", "/mcp", "{}", {"Content-Type": "application/json"})
        check(status == 401 and client.server["public_origin"] in headers.get("www-authenticate", ""), "unauthenticated MCP discovers OAuth")
        for path, field, expected in [("/.well-known/oauth-protected-resource/mcp", "resource", client.server["public_origin"] + "/mcp"),
                ("/.well-known/oauth-authorization-server", "issuer", client.server["public_origin"])]:
            status, _, body = client.request("GET", path)
            check(status == 200 and json.loads(body)[field] == expected, "HTTPS canonical discovery: " + field)
        users = {}
        evidence = []
        for name in ("alice", "bob", "visitor"):
            issued = client.grant(name)
            token = issued["access_token"]
            session = client.initialize(token)
            users[name] = (issued, session)
            status, _, message = client.rpc(token, "tools/list", session=session)
            tools = message["result"]["tools"]
            check(status == 200 and len(tools) == (0 if name == "visitor" else 1), "OAuth directory permission: " + name)
            status, _, message = client.rpc(token, "tools/call", {"name": "pg.get_demo_item", "arguments": {"itemId": "item_demo_1"}}, session)
            r = message["result"]["structuredContent"]
            if name == "visitor":
                check(r["error"]["code"] == "ACCESS_DENIED" and r["executionId"] is None, "OAuth visitor direct call denied")
            else:
                check(r["requestStatus"] == "SUCCEEDED" and r["dispatchState"] == "DISPATCHED" and r["sideEffectStatus"] == "NONE", "OAuth real PG read: " + name)
                check(r["data"]["item"]["note"] == ("Alice scope A" if name == "alice" else "Bob scope B"), "OAuth native scope isolation: " + name)
                check(json.loads(message["result"]["content"][0]["text"]) == r, "OAuth text/structured result agreement: " + name)
                evidence.append({"employee": name, "result": r})
                cfg = fixture.read("env.json")
                rows = fixture.dev.psql(cfg, "mcpguard_meta", f"SELECT count(*) FROM mcpguard.audit_events WHERE action_id='{r['actionId']}' AND attempt_id='{r['attemptId']}' AND execution_id='{r['executionId']}' AND event_type IN ('EXECUTION_INTENT','SUCCEEDED');").stdout
                check(re.search(r"\n\s*2\s*\n", rows) is not None, "OAuth read correlates durable audit: " + name)
        alice, sid = users["alice"]
        bob, _ = users["bob"]
        check(client.rpc(bob["access_token"], "tools/list", session=sid)[0] == 403, "OAuth identity cannot hijack another session")
        status, _, r = client.rpc(alice["access_token"], "tools/call", {"name": "pg.get_demo_item", "arguments": {"itemId": "item_demo_b_only"}}, sid)
        check(r["result"]["structuredContent"]["data"] == {"found": False, "item": None}, "OAuth cannot read another scope's exclusive row")
        status, _, _ = client.request("GET", "/api/v1/audit", headers={"Authorization": "Bearer " + alice["access_token"]})
        check(status == 404, "temporary public profile excludes audit management API")
        check(client.request("GET", "/mcp", headers={"Authorization": "Bearer " + alice["access_token"]})[0] == 405, "optional standalone SSE explicitly disabled")
        client_fields = {"client_id": client.credentials["client_id"], "client_secret": client.credentials["client_secret"]}
        status, renewed = client.post("/oauth/token", {**client_fields, "grant_type": "refresh_token", "refresh_token": alice["refresh_token"], "resource": client.server["public_origin"] + "/mcp"})
        check(status == 200 and renewed["refresh_token"] != alice["refresh_token"], "OAuth refresh token rotates")
        check(client.rpc(renewed["access_token"], "tools/list", session=sid)[0] == 200, "renewed identity retains its MCP session")
        check(client.post("/oauth/revoke", {**client_fields, "token": renewed["refresh_token"]})[0] == 200, "OAuth revocation accepted")
        check(client.rpc(renewed["access_token"], "tools/list", session=sid)[0] == 401, "revocation invalidates access on next request")
        output = {"status": "PASS", "checks": CHECKS, "checkCount": len(CHECKS), "baseURL": args.url,
                  "transport": "MCP Streamable HTTP, JSON POST responses; standalone SSE disabled", "profile": PROFILE,
                  "identityProvider": "isolated OAuth test fixture; not enterprise SSO", "reads": evidence}
        (ROOT / args.output).write_text(json.dumps(output, indent=2) + "\n")
        print(f"PASS: {len(CHECKS)} OAuth/HTTP/PG/audit checks; no credentials printed.")
    finally:
        if process and process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        if log:
            log.close()


if __name__ == "__main__":
    main()
