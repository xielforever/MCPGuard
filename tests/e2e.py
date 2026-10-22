#!/usr/bin/env python3
"""Independent Python wire tests of real binaries + PostgreSQL; standard library only."""
from concurrent.futures import ThreadPoolExecutor
from http.client import HTTPConnection
import importlib.util
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import threading
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("dev", ROOT / "scripts/dev.py")
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)
CFG = dev.load()
PROFILE = "2025-11-25"
CHECKS = []


def check(condition, name):
    if not condition:
        raise AssertionError(name)
    CHECKS.append(name)


def until(predicate, seconds=2):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.025)
    raise AssertionError("condition timed out")


def http(port, path, token=None, body=None, headers=None, method="POST"):
    conn = HTTPConnection("127.0.0.1", port, timeout=7)
    h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if token:
        h["Authorization"] = "Bearer " + token
    h.update(headers or {})
    data = body if isinstance(body, bytes) else json.dumps(body).encode() if body is not None else None
    try:
        conn.request(method, path, data, h)
        resp = conn.getresponse()
        return resp.status, dict(resp.getheaders()), resp.read()
    finally:
        conn.close()


class HTTPMCP:
    def __init__(self, port, employee):
        self.port, self.token = port, CFG[employee + "_token"]
        status, headers, body = http(port, "/mcp", self.token, {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": PROFILE, "capabilities": {}, "clientInfo": {"name": "python-independent", "version": "1"}}})
        check(status == 200 and json.loads(body)["result"]["protocolVersion"] == PROFILE, "HTTP initialize: " + employee)
        self.sid = next(v for k, v in headers.items() if k.lower() == "mcp-session-id")
        self.headers = {"Mcp-Session-Id": self.sid, "MCP-Protocol-Version": PROFILE}
        status, _, _ = http(port, "/mcp", self.token, {"jsonrpc": "2.0", "method": "notifications/initialized"}, self.headers)
        check(status == 202, "HTTP initialized notification: " + employee)

    def call(self, method, params=None, ident="same-id"):
        status, _, body = http(self.port, "/mcp", self.token, {"jsonrpc": "2.0", "id": ident, "method": method, "params": params or {}}, self.headers)
        assert status == 200, "MCP HTTP status"
        msg = json.loads(body)
        assert msg["id"] == ident and type(msg["id"]) is type(ident), "RPC ID changed"
        assert "error" not in msg, "Unexpected MCP protocol error"
        return msg["result"]

    def read(self, item="item_demo_1", ident="same-id"):
        return self.call("tools/call", {"name": "pg.get_demo_item", "arguments": {"itemId": item}}, ident)

    def audit(self):
        events, after = [], 0
        while True:
            status, _, body = http(self.port, "/api/v1/audit?limit=25&after=" + str(after), self.token, method="GET")
            assert status == 200
            page = json.loads(body)
            events.extend(page["events"])
            if not page["events"]:
                return events
            after = page["nextAfter"]


class StdioMCP:
    def __init__(self, port, employee="alice"):
        env = os.environ.copy()
        for k in list(env):
            if k.startswith("MCPGUARD_"):
                env.pop(k)
        env["MCPGUARD_TOKEN"] = CFG[employee + "_token"]
        self.p = subprocess.Popen([str(ROOT / "bin/mcpguard-connect"), "-gateway", f"http://127.0.0.1:{port}/mcp", "-allow-loopback-http"], cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1)
        self.q, self.pending, self.lines = queue.Queue(), {}, []
        self.thread = threading.Thread(target=self._reader, daemon=True)
        self.thread.start()
        self.send("initialize", {"protocolVersion": PROFILE, "capabilities": {}, "clientInfo": {"name": "python-stdio", "version": "1"}}, "init")
        check(self.receive("init")["result"]["protocolVersion"] == PROFILE, "stdio initialize: " + employee)
        self.send("notifications/initialized", {})

    def _reader(self):
        for line in self.p.stdout:
            self.lines.append(line)
            try:
                self.q.put(json.loads(line))
            except ValueError:
                self.q.put({"invalid_stdout": True})

    def send(self, method, params, ident=None):
        msg = {"jsonrpc": "2.0", "method": method, "params": params}
        if ident is not None:
            msg["id"] = ident
        self.p.stdin.write(json.dumps(msg) + "\n")
        self.p.stdin.flush()

    def receive(self, ident, timeout=6):
        if ident in self.pending:
            return self.pending.pop(ident)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            msg = self.q.get(timeout=max(0.01, deadline - time.monotonic()))
            assert "invalid_stdout" not in msg, "Connector diagnostics corrupted protocol stdout"
            if msg.get("id") == ident:
                return msg
            self.pending[msg.get("id")] = msg
        raise AssertionError("stdio response timed out")

    def call(self, method, params=None, ident="same-id", timeout=6):
        self.send(method, params or {}, ident)
        msg = self.receive(ident, timeout)
        assert type(msg["id"]) is type(ident), "stdio RPC ID type changed"
        assert "error" not in msg, "stdio protocol error"
        return msg["result"]

    def close(self):
        self.p.stdin.close()
        try:
            self.p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.p.kill()
            self.p.wait()
            raise AssertionError("Connector did not shut down on EOF")
        self.thread.join(timeout=1)
        stderr = self.p.stderr.read()
        check(self.p.returncode == 0, "connector exits cleanly on EOF")
        combined = "".join(self.lines) + stderr
        check(all(CFG[n + "_token"] not in combined for n in dev.IDS), "connector output contains no access token")
        check(all(json.loads(line)["jsonrpc"] == "2.0" for line in self.lines), "stdout contains protocol frames only")


class GatewayProcess:
    def __init__(self, port):
        self.log = open(ROOT / ".dev/gateway-e2e.log", "a")
        env = dev.runtime_env(CFG)
        env["MCPGUARD_LISTEN"] = f"127.0.0.1:{port}"
        self.p = subprocess.Popen([str(ROOT / "bin/mcpguard")], cwd=ROOT, env=env, stdout=self.log, stderr=self.log)
        def ready():
            if self.p.poll() is not None:
                raise AssertionError("Gateway startup failed; see .dev/gateway-e2e.log")
            try:
                return http(port, "/healthz", method="GET")[0] == 200
            except (ConnectionError, OSError):
                return False
        try:
            until(ready, 5)
        except BaseException:
            self.close()
            raise

    def close(self):
        if self.p.poll() is None:
            self.p.send_signal(signal.SIGTERM)
            try:
                self.p.wait(timeout=9)
            except subprocess.TimeoutExpired:
                self.p.kill()
                self.p.wait()
                raise AssertionError("Gateway graceful shutdown exceeded its bound")
        self.log.close()
        check(self.p.returncode == 0, "gateway exits cleanly on SIGTERM")


def validate_read(result, note):
    r = result["structuredContent"]
    assert result.get("isError", False) is False and r["requestStatus"] == "SUCCEEDED"
    assert r["dispatchState"] == "DISPATCHED" and r["sideEffectStatus"] == "NONE"
    assert r["data"]["item"]["note"] == note
    assert json.loads(result["content"][0]["text"]) == r
    return r


def scalar(database, sql):
    return dev.psql(CFG, database, "\\pset format unaligned\n\\pset tuples_only on\n" + sql).stdout.strip().splitlines()[-1]


def stdio_cancel_isolation(port, alice):
    env = os.environ.copy()
    for k in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
        env.pop(k, None)
    lock = subprocess.Popen(["docker", "--host=unix:///var/run/docker.sock", "exec", "-i", CFG["container"], "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "mcpguard_business"], env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1)
    bridge = None
    try:
        lock.stdin.write("BEGIN; LOCK TABLE demo.items IN ACCESS EXCLUSIVE MODE; SELECT 'locked';\n")
        lock.stdin.flush()
        assert lock.stdout.readline().strip() == "locked"
        bridge = StdioMCP(port)
        params = {"name": "pg.get_demo_item", "arguments": {"itemId": "item_demo_1"}}
        bridge.send("tools/call", params, "cancel-me")
        bridge.send("tools/call", params, "keep-me")
        query = "SELECT count(*) FROM pg_stat_activity WHERE application_name='mcpguard-native-reader' AND wait_event_type='Lock' AND query LIKE 'SELECT item_id%';"
        until(lambda: scalar("mcpguard_business", query) == "2", 1.5)
        start = time.monotonic()
        bridge.send("notifications/cancelled", {"requestId": "cancel-me", "reason": "independent wire test"})
        check(len(bridge.call("tools/list", ident="while-blocked", timeout=1)["tools"]) == 1, "stdio processes independent request while native calls are blocked")
        until(lambda: scalar("mcpguard_business", query) == "1", 1)
        check(time.monotonic() - start < 1.5, "stdio cancellation reaches one real PG query before deadline")
        lock.stdin.write("ROLLBACK;\n\\q\n")
        lock.stdin.flush()
        lock.wait(timeout=3)
        kept = bridge.receive("keep-me")["result"]
        validate_read(kept, "Alice scope A")
        check(True, "cancelling one stdio request does not cancel another")
        until(lambda: any(e["eventType"] == "CANCELLED" for e in alice.audit()))
    finally:
        if lock.poll() is None:
            lock.stdin.close()
            try:
                lock.wait(timeout=3)
            except subprocess.TimeoutExpired:
                lock.kill()
                lock.wait()
        if bridge:
            bridge.close()


def main():
    port = dev.free_port()
    gateway = GatewayProcess(port)
    bridges = []
    try:
        alice, bob, visitor = (HTTPMCP(port, n) for n in ("alice", "bob", "visitor"))
        check(len(alice.call("tools/list")["tools"]) == 1 and len(visitor.call("tools/list")["tools"]) == 0, "directory is filtered per employee")
        with ThreadPoolExecutor(2) as pool:
            a, b = list(pool.map(lambda c: c.read(ident=9007199254740993), (alice, bob)))
        ar, br = validate_read(a, "Alice scope A"), validate_read(b, "Bob scope B")
        check(ar["actionId"] != br["actionId"] and b["structuredContent"]["data"]["item"]["version"] == "9007199254740993", "same large integer RPC ID across employees preserves identity and native bigint precision")
        with ThreadPoolExecutor(2) as pool:
            list(pool.map(lambda ident: alice.call("tools/list", ident=ident), (7, "7")))
        check(True, "integer and string RPC IDs with the same text remain distinct")
        alice.call("tools/list", ident=1234567890123456789012345678901234567890)
        check(True, "integer RPC ID beyond int64 round trips exactly")
        missing = alice.read("item_demo_b_only")["structuredContent"]
        check(missing["data"] == {"found": False, "item": None}, "PG query cannot read another scope's exclusive item")
        denied = visitor.read()["structuredContent"]
        check(denied["error"]["code"] == "ACCESS_DENIED" and denied["actionId"] is None and denied["data"] is None, "hidden tool also denies direct invocation without fake business IDs")
        attack = alice.call("tools/call", {"name": "pg.get_demo_item", "arguments": {"itemId": "item_demo_1", "scopeId": "scope_b"}})
        check(attack["isError"] and attack["structuredContent"]["executionId"] is None, "caller cannot inject scope")
        status, _, _ = http(port, "/mcp", bob.token, {"jsonrpc": "2.0", "id": 3, "method": "tools/list"}, alice.headers)
        check(status == 403, "MCP session cannot be reused by another employee")
        for token in (None, "invalid", "x" * 43):
            status, _, _ = http(port, "/mcp", token, {"jsonrpc": "2.0", "id": 4, "method": "tools/list"})
            check(status == 401, "missing/invalid bearer rejected before business acceptance")
        status, _, _ = http(port, "/mcp", alice.token, b"x" * 66000, alice.headers)
        check(status == 413, "HTTP request size is bounded")
        status, _, _ = http(port, "/mcp", alice.token, {}, {**alice.headers, "Origin": "https://untrusted.invalid"})
        check(status == 403, "untrusted browser origin rejected")
        status, _, _ = http(port, "/api/v1/audit?principalId=" + dev.IDS["alice"], bob.token, method="GET")
        check(status == 400, "audit API refuses caller-supplied identity selectors")
        aa, ba = alice.audit(), bob.audit()
        check(ar["actionId"] in {e["actionId"] for e in aa} and ar["actionId"] not in {e["actionId"] for e in ba}, "persistent audit is correlated and isolated per employee")
        for employee, note in (("alice", "Alice scope A"), ("bob", "Bob scope B")):
            bridge = StdioMCP(port, employee)
            bridges.append(bridge)
            check(len(bridge.call("tools/list")["tools"]) == 1, "stdio forwards authorized directory: " + employee)
            result = bridge.call("tools/call", {"name": "pg.get_demo_item", "arguments": {"itemId": "item_demo_1"}}, ident=9007199254740993)
            validate_read(result, note)
            check(True, "stdio reaches actual native PG result: " + employee)
            bridge.call("tools/list", ident=1234567890123456789012345678901234567890)
            check(True, "stdio preserves RPC integer IDs beyond int64: " + employee)
        for bridge in bridges:
            bridge.close()
        bridges.clear()
        stdio_cancel_isolation(port, alice)
        snapshot = alice.audit()
        gateway.close()
        gateway = GatewayProcess(port)
        restored = HTTPMCP(port, "alice")
        check(restored.audit() == snapshot, "gateway restart preserves original audit event IDs and correlations")
        validate_read(restored.read(), "Alice scope A")
        check(True, "gateway works after restart with the same encrypted store")
    finally:
        for bridge in bridges:
            bridge.close()
        gateway.close()
    report = {"date": time.strftime("%Y-%m-%d", time.gmtime()), "profile": PROFILE, "postgres": "16.14", "checks": CHECKS, "passed": len(CHECKS), "modelKeyRequired": False, "realChatGPTValidated": False}
    (ROOT / ".dev/e2e-results.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(f"Independent Python E2E passed: {len(CHECKS)} checks; HTTP + stdio + real PG + cancellation isolation + persistent audit/restart. No model key.")


if __name__ == "__main__":
    main()
