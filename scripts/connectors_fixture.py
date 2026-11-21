#!/usr/bin/env python3
"""Prepare isolated real backends, wait for browser actions, and verify MCP reads."""

import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import tempfile
import threading
import time
from urllib.parse import urlsplit
from urllib.request import Request, urlopen
from uuid import uuid4

from connectors_backends_fixture import REDIS_PROBE_KEY, REDIS_PROBE_VALUE, start_kubernetes, start_redis
import jenkins_dsh_fixture as fixture


def mcp_tools(port, token):
    headers = {
        "Accept": "application/json, text/event-stream",
        "Content-Type": "application/json",
        "Authorization": "Bearer " + token,
        "MCP-Protocol-Version": "2025-11-25",
    }

    def rpc(method, params, request_id=None):
        payload = {"jsonrpc": "2.0", "method": method, "params": params}
        if request_id is not None:
            payload["id"] = request_id
        request = Request(f"http://127.0.0.1:{port}/mcp", data=json.dumps(payload).encode(), headers=headers)
        with urlopen(request, timeout=10) as response:
            session = response.headers.get("Mcp-Session-Id")
            if session:
                headers["Mcp-Session-Id"] = session
            body = response.read()
            return json.loads(body) if body else None

    init = rpc("initialize", {
        "protocolVersion": "2025-11-25", "capabilities": {},
        "clientInfo": {"name": "connectors-isolated-acceptance", "version": "1"},
    }, 1)
    fixture.check("result" in init, "MCP session could not initialize")
    rpc("notifications/initialized", {})
    serial = 1

    def tool(name, arguments, expect_allowed=True):
        nonlocal serial
        serial += 1
        response = rpc("tools/call", {"name": name, "arguments": arguments}, serial)
        result = response.get("result", {})
        allowed = "error" not in response and "structuredContent" in result and not result.get("isError", False)
        fixture.check(allowed == expect_allowed,
                      f"Unexpected access decision for {name}")
        return result.get("structuredContent")

    return tool


def console_change(reviewer, method, target, payload, revision=None, expected=200):
    headers = {**reviewer.headers, "Idempotency-Key": str(uuid4())}
    if revision is not None:
        headers["If-Match"] = f'"{revision}"'
    status, _, body = fixture.http(reviewer.port, method, "/console/api/connectors" + target,
                                   headers, json.dumps(payload))
    fixture.check(status == expected, f"Unexpected console result for connector operation, HTTP {status}")
    return json.loads(body) if status == 200 else None


def check_mcp(port, cfg, reviewer, browser, connections, keep_enabled=False):
    fixture.check(browser.get("status") == "PASS", "Browser acceptance did not pass")
    items = {row["type"]: row for row in browser["connectors"]}
    fixture.check(set(items) == {"postgresql", "redis", "jenkins", "kubernetes"}, "Browser rows incomplete")
    alice = mcp_tools(port, cfg["alice_token"])
    bob = mcp_tools(port, cfg["bob_token"])
    listed = {row["id"] for row in alice("connector.list", {})["connectors"]}
    fixture.check(listed == {row["id"] for row in items.values()}, "Enabled connector discovery mismatch")
    fixture.check(bob("connector.list", {})["connectors"] == [], "Ungranted caller can discover connectors")

    pg = {"connectorId": items["postgresql"]["id"]}
    redis = {"connectorId": items["redis"]["id"], "key": REDIS_PROBE_KEY}
    jenkins = {"connectorId": items["jenkins"]["id"]}
    k8s = {"connectorId": items["kubernetes"]["id"]}
    pg_result = alice("connector.pg.list_tables", pg)
    fixture.check(pg_result["schema"] == "commerce" and pg_result["tables"] == ["orders"], "PG table scope mismatch")
    redis_result = alice("connector.redis.get", redis)
    fixture.check(redis_result["found"] and redis_result["value"] == REDIS_PROBE_VALUE, "Redis value mismatch")
    alice("connector.redis.get", {**redis, "key": REDIS_PROBE_KEY + "x"}, False)
    jobs = alice("connector.jenkins.list_jobs", jenkins)["jobs"]
    job = next(item for item in jobs if item["name"] == fixture.ORDERS)
    dynamic = alice("connector.jenkins.describe_job", {**jenkins, "jobRef": job["jobRef"]})
    names = sorted(p["name"] for p in dynamic["parameters"])
    fixture.check(names == ["GIT_REF", "RUN_TESTS"], "Jenkins dynamic parameter mismatch")
    workloads = alice("connector.k8s.list_deployments", k8s)
    fixture.check(workloads["namespace"] == "orders-verification" and
                  [row["name"] for row in workloads["deployments"]] == ["orders-validation"], "Kubernetes namespace mismatch")
    alice("connector.k8s.list_deployments", {**k8s, "namespace": "default"}, False)
    alice("connector.redis.get", {**redis, "key": "inventory:private"}, False)
    bob("connector.pg.list_tables", pg, False)

    for good in connections:
        rejected = console_change(reviewer, "POST", "", {
            **good, "name": "wrong-credential-" + good["type"],
            "credentials": {**good["credentials"], "secret": uuid4().hex},
        })
        failed = console_change(reviewer, "POST", "/" + rejected["id"] + "/test", {}, rejected["revision"])
        fixture.check(failed["testStatus"] == "FAILED" and not failed["enabled"], "Bad credentials did not fail closed")
        console_change(reviewer, "PATCH", "/" + rejected["id"], {"enabled": True}, failed["revision"], expected=409)
    disabled = [console_change(reviewer, "PATCH", "/" + row["id"], {"enabled": False}, row["revision"])
                for row in items.values()]
    fixture.check(alice("connector.list", {})["connectors"] == [], "Stopped connectors still discoverable")
    for name, args in (("connector.pg.list_tables", pg), ("connector.redis.get", redis),
                       ("connector.jenkins.list_jobs", jenkins), ("connector.k8s.list_deployments", k8s)):
        alice(name, args, False)
    if keep_enabled:
        for row in disabled:
            console_change(reviewer, "PATCH", "/" + row["id"], {"enabled": True}, row["revision"])

    return {
        "postgresql": {"schema": pg_result["schema"], "tables": pg_result["tables"]},
        "redis": {"found": redis_result["found"], "keyBytes": len(REDIS_PROBE_KEY), "oversizedRejected": True},
        "jenkins": {"jobs": sorted(job["name"] for job in jobs), "parameters": names},
        "kubernetes": {"namespace": workloads["namespace"], "deployments": ["orders-validation"]},
        "checks": ["grant_isolation", "scope", "bad_credentials", "hot_disable"],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path)
    parser.add_argument("--hold-seconds", type=int, default=0, choices=range(3601), metavar="0–3600")
    parser.add_argument("--gateway-port", type=int, choices=range(1024, 65536), metavar="1024–65535")
    args = parser.parse_args()
    os.umask(0o077)
    run_id = uuid4().hex[:12]
    scratch = Path(tempfile.mkdtemp(prefix="mcpguard-connectors-acceptance-"))
    print(f"Preparing {run_id}; private fixture logs in {scratch}", flush=True)
    marker_dir = fixture.dev.ROOT / ".dev"
    marker_dir.mkdir(exist_ok=True)
    browser_result = marker_dir / f"connectors-browser-{run_id}.json"
    with ExitStack() as stack:
        proxy = stack.enter_context(fixture.JenkinsProxy())
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        stack.callback(proxy.shutdown)
        log = stack.enter_context((scratch / "fixture.log").open("w"))
        _, jenkins, jenkins_version = fixture.start_jenkins(stack, scratch, run_id, proxy, log)
        redis = start_redis(stack, scratch, run_id, log)
        k8s = start_kubernetes(stack, scratch, run_id, log)
        print("Jenkins, Redis and Kubernetes are ready", flush=True)
        binary = fixture.build(scratch, log)
        cfg = fixture.prepare_metadata(stack, scratch, binary, run_id, log)
        if args.gateway_port:
            cfg["MCPGUARD_LISTEN"] = f"127.0.0.1:{args.gateway_port}"
        cfg["MCPGUARD_CONNECTOR_ALLOWED_CIDRS"] = "127.0.0.1/32"
        applicant, operator = fixture.dev.IDS["alice"], fixture.dev.IDS["visitor"]
        fixture.dev.psql(cfg, "mcpguard_meta",
                         f"INSERT INTO mcpguard.control_members VALUES ('{fixture.dev.ORG}','{operator}',true)")
        fixture.dev.psql(cfg, "mcpguard_business", """
            CREATE SCHEMA commerce;
            CREATE TABLE commerce.orders (id bigint PRIMARY KEY);
            GRANT USAGE ON SCHEMA commerce TO mcpguard_reader;
            GRANT SELECT ON commerce.orders TO mcpguard_reader;
        """)
        pg = urlsplit(cfg["MCPGUARD_BUSINESS_DSN"])
        connections = [
            {"name": "orders-db", "type": "postgresql", "config": {"host": pg.hostname, "port": pg.port,
             "tls": False, "database": pg.path[1:], "schema": "commerce"},
             "credentials": {"username": pg.username, "secret": pg.password}},
            redis,
            {"name": "delivery-ci", "type": "jenkins", "config": {"baseUrl": jenkins["baseUrl"], "folder": "/"},
             "credentials": {"username": jenkins["username"], "secret": jenkins["apiToken"]}},
            k8s,
        ]
        for connection in connections:
            connection["principalIds"] = [applicant]
        reviewer_class = fixture.Reviewer
        browser_input = scratch / "browser-input.json"

        def prepare_browser(port, password):
            reviewer = reviewer_class(port, password)
            fixture.dev.write_private(browser_input, json.dumps({"url": reviewer.origin + "/web/",
                "loginUsername": "visitor", "loginSecret": password, "connections": connections}))
            return reviewer

        fixture.Reviewer = prepare_browser
        port, reviewer = fixture.start_gateway(stack, scratch, binary, cfg, jenkins, log)
        print(json.dumps({"status": "READY", "runId": run_id, "port": port, "browserInput": str(browser_input),
                          "browserResult": str(browser_result)}), flush=True)

        def browser_proof():
            if browser_result.exists():
                return json.loads(browser_result.read_text(encoding="utf-8-sig"))
            return None

        try:
            browser = fixture.wait_for(browser_proof, "No browser acceptance result was received", 600)
            verified = check_mcp(port, cfg, reviewer, browser, connections, args.hold_seconds > 0)
            result = {"status": "PASS", "runId": run_id, "jenkinsVersion": jenkins_version,
                      "browser": browser, "mcp": verified}
            text = json.dumps(result, indent=2) + "\n"
            if args.report:
                args.report.write_text(text, encoding="utf-8")
            print(text, end="", flush=True)
            if args.hold_seconds:
                time.sleep(args.hold_seconds)
        except Exception:
            if args.hold_seconds:
                print("Acceptance failed; isolated backends available for 60 seconds of diagnosis", flush=True)
                time.sleep(60)
            raise
        finally:
            browser_input.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
