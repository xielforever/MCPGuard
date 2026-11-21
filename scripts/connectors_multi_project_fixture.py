#!/usr/bin/env python3
"""Verify MCPGuard connector reuse from two real Windows project checkouts."""

from argparse import ArgumentParser
from contextlib import ExitStack
import json
import ntpath
import os
from pathlib import Path
import select
import socket
from socketserver import BaseRequestHandler, ThreadingTCPServer
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlsplit
from uuid import uuid4

from connectors_fixture import console_change
from dsh_client import DSHClient, EXECUTABLE, install_path, windows
import jenkins_dsh_fixture as fixture


LAUNCHER = """
import os, sys
from pathlib import Path
token = Path(sys.argv[1]).read_text().strip()
os.execve(sys.argv[2], sys.argv[2:], dict(os.environ, MCPGUARD_TOKEN=token))
"""


class CountedPostgres(ThreadingTCPServer):
    """Count actual provider TCP connections without recording traffic."""

    daemon_threads = True

    def __init__(self, upstream):
        self.upstream = upstream
        self.lock = threading.Lock()
        self.count = 0
        super().__init__(("127.0.0.1", 0), PostgresRelay)

    def connections(self):
        with self.lock:
            return self.count


class PostgresRelay(BaseRequestHandler):
    def handle(self):
        with self.server.lock:
            self.server.count += 1
        try:
            with socket.create_connection(self.server.upstream, timeout=5) as remote:
                sockets = [self.request, remote]
                while True:
                    readable, _, _ = select.select(sockets, [], [], 8)
                    if not readable:
                        return
                    for source in readable:
                        chunk = source.recv(65536)
                        if not chunk:
                            return
                        (remote if source is self.request else self.request).sendall(chunk)
        except OSError:
            return


def checkout(path):
    path = path.resolve(strict=True)
    command = ["git", "-c", f"safe.directory={path}", "-C", str(path), "rev-parse"]
    root = Path(subprocess.check_output([*command, "--show-toplevel"], text=True, timeout=15).strip())
    fixture.check(root.resolve() == path, "The requested project path is not an actual checkout root")
    revision = subprocess.check_output([*command, "HEAD"], text=True, timeout=15).strip()
    return {"repository": path.name, "workspace": windows(path), "head": revision}


def build(scratch, log):
    fixture.dev.docker(
        "run", "--rm", "--pull", "never", "--network", "none",
        "--mount", f"type=bind,src={fixture.dev.ROOT},dst=/work,readonly",
        "--mount", f"type=bind,src={scratch},dst=/fixture",
        "--mount", "type=volume,src=mcpguard-ra-go-build,dst=/root/.cache/go-build",
        "--mount", "type=volume,src=mcpguard-ra-go-modules,dst=/go/pkg/mod",
        "-w", "/work", "-e", "GOTOOLCHAIN=local", "-e", "GOPROXY=off", "-e", "GOFLAGS=-mod=readonly -buildvcs=false",
        fixture.GO_IMAGE, "go", "build", "-p", "2", "-o", "/fixture/", "./cmd/mcpguard", "./cmd/mcpguard-connect",
        stdout=log, stderr=log,
    )


def enable_connector(reviewer, pg, relay_port, name, schema, principal_ids):
    connector = console_change(reviewer, "POST", "", {
        "name": name, "type": "postgresql",
        "config": {"host": "127.0.0.1", "port": relay_port, "tls": False,
                   "database": pg.path[1:], "schema": schema},
        "credentials": {"username": pg.username, "secret": pg.password},
        "principalIds": principal_ids,
    })
    target = "/" + connector["id"]
    tested = console_change(reviewer, "POST", target + "/test", {}, connector["revision"])
    fixture.check(tested["testStatus"] == "PASSED", "Connector did not reach its native PostgreSQL schema")
    return console_change(reviewer, "PATCH", target, {"enabled": True}, tested["revision"])


def open_client(stack, install, scratch, runtime_root, workspace, metadata, alias, port, token):
    auth = scratch / (alias + ".token")
    fixture.dev.write_private(auth, token)
    stack.callback(auth.unlink, missing_ok=True)
    log = stack.enter_context((scratch / (alias + "-dsh.log")).open("w", encoding="utf-8"))
    client = stack.enter_context(DSHClient({
        "transport": "stdio", "command": r"C:\Windows\System32\wsl.exe",
        "args": ["-d", os.environ.get("WSL_DISTRO_NAME", "Ubuntu-24.04"), "--exec",
                 "/usr/bin/python3", "-B", "-c", LAUNCHER, str(auth),
                 str(scratch / "mcpguard-connect"), "--gateway", f"http://127.0.0.1:{port}/mcp",
                 "--allow-loopback-http"],
        "startupTimeoutMs": 40000, "reportWorkspace": True,
    }, log, install=install, workspace=workspace, home=runtime_root / alias))
    ready = client.receive("ready", timeout=55)
    fixture.check(ntpath.normcase(ready["workspace"]) == ntpath.normcase(metadata["workspace"]),
                  "Installed Harness did not launch from the real project working directory")
    fixture.check(any("connector_list" in name for name in ready["names"]) and
                  any("connector_pg_list_tables" in name for name in ready["names"]),
                  "Installed Harness did not discover the configured connector tools")
    metadata["workspace"] = ready["workspace"]
    metadata["clientVersion"] = ready["runtimeVersion"]
    return client


def catalog(client, relay, expected_ids):
    before = relay.connections()
    result = fixture.call(client, "connector.list", {})
    actual_ids = sorted(row["id"] for row in result["connectors"])
    fixture.check(actual_ids == sorted(expected_ids), "Connector directory exposed a private or stopped entry")
    fixture.check(relay.connections() == before, "Connector discovery unexpectedly connected to PostgreSQL")
    return actual_ids


def project_read(client, relay, connector, schema, table):
    before = relay.connections()
    result = fixture.call(client, "connector.pg.list_tables", {"connectorId": connector["id"]})
    fixture.check(result["schema"] == schema and result["tables"] == [table], "Native PostgreSQL result is incorrect")
    dispatched = relay.connections() - before
    fixture.check(dispatched == 1, "Authorized connector read did not open exactly one native connection")
    return {"schema": schema, "tables": result["tables"], "nativeConnections": dispatched}


def project_denied(client, relay, connector):
    before = relay.connections()
    returned = client.call("connector.pg.list_tables", {"connectorId": connector["id"]})
    result = returned.get("result", returned)
    error = (result.get("structuredContent") or {}).get("error", {})
    fixture.check(result.get("isError") is True and error.get("code") == "ACCESS_DENIED",
                  "Connector denial was not an observed gateway authorization reply")
    dispatched = relay.connections() - before
    fixture.check(dispatched == 0, "Denied connector read reached PostgreSQL")
    return {"code": error["code"], "nativeConnections": dispatched}


def verify(clients, projects, shared, private, reviewer, relay):
    start_count = relay.connections()
    schemas = [("dbhub_private", "inventory"), ("dsh_project_control_private", "pipelines")]
    for index, (client, report) in enumerate(zip(clients, projects)):
        report["visibleBefore"] = catalog(client, relay, [shared["id"], private[index]["id"]])
        report["sharedRead"] = project_read(client, relay, shared, "shared_delivery", "jobs")
        report["privateRead"] = project_read(client, relay, private[index], *schemas[index])
        report["otherPrivateDirectCall"] = project_denied(client, relay, private[1 - index])

    before = relay.connections()
    stopped = console_change(reviewer, "PATCH", "/" + shared["id"], {"enabled": False}, shared["revision"])
    fixture.check(not stopped["enabled"], "The shared connector was still enabled after disable committed")
    fixture.check(relay.connections() == before, "Disabling a connector triggered a new native connection")
    stopped_at = time.monotonic()
    for client, report in zip(clients, projects):
        request_delay_ms = round((time.monotonic() - stopped_at) * 1000, 1)
        report["stoppedSharedDirectCall"] = project_denied(client, relay, shared) | {"requestDelayMs": request_delay_ms}
    for index, (client, report) in enumerate(zip(clients, projects)):
        report["visibleAfter"] = catalog(client, relay, [private[index]["id"]])
        report["privateReadAfterDisable"] = project_read(client, relay, private[index], *schemas[index])
        fixture.check(client.process.poll() is None, "An installed Harness process exited during the checks")
        report["sameClientSession"] = True

    status, _, body = fixture.http(reviewer.port, "GET", f"/console/api/connectors/{shared['id']}/events", reviewer.headers)
    fixture.check(status == 200, "Connector audit could not be read")
    events = [entry["type"] for entry in json.loads(body)["events"]]
    fixture.check("DISABLED" in events, "The shared connector disable event was not audited")
    fixture.check(relay.connections() - start_count == 6, "Native PostgreSQL connection count did not match the six authorized reads")
    return {"id": shared["id"], "revisionAfterDisable": stopped["revision"], "auditEvents": events,
            "authorizedNativeConnections": relay.connections() - start_count, "deniedNativeConnections": 0}


def main():
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("--project-a", type=Path, required=True)
    parser.add_argument("--project-b", type=Path, required=True)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    workspaces = [args.project_a.resolve(strict=True), args.project_b.resolve(strict=True)]
    fixture.check(workspaces[0] != workspaces[1], "Two distinct project checkouts are required")
    projects = [checkout(path) for path in workspaces]
    install = install_path()
    fixture.check((install / EXECUTABLE).is_file(), "The installed DeepSeek Harness client is unavailable")
    run_id = uuid4().hex[:12]
    scratch = Path(tempfile.mkdtemp(prefix="mcpguard-connectors-projects-"))
    runtime_root = fixture.dev.ROOT / ".dev" / f"dsh-connector-projects-{run_id}"
    runtime_root.mkdir(mode=0o700, parents=True)
    print(f"Preparing {run_id} for two projects with isolated roles and PostgreSQL", flush=True)
    with ExitStack() as stack:
        log = stack.enter_context((scratch / "fixture.log").open("w", encoding="utf-8"))
        build(scratch, log)
        cfg = fixture.prepare_metadata(stack, scratch, scratch / "mcpguard", run_id, log)
        cfg["MCPGUARD_CONNECTOR_ALLOWED_CIDRS"] = "127.0.0.1/32"
        operator = fixture.dev.IDS["visitor"]
        fixture.dev.psql(cfg, "mcpguard_meta",
                         f"INSERT INTO mcpguard.control_members VALUES ('{fixture.dev.ORG}','{operator}',true)")
        fixture.dev.psql(cfg, "mcpguard_business", """
            CREATE SCHEMA shared_delivery;
            CREATE SCHEMA dbhub_private;
            CREATE SCHEMA dsh_project_control_private;
            CREATE TABLE shared_delivery.jobs (id bigint PRIMARY KEY);
            CREATE TABLE dbhub_private.inventory (id bigint PRIMARY KEY);
            CREATE TABLE dsh_project_control_private.pipelines (id bigint PRIMARY KEY);
            GRANT USAGE ON SCHEMA shared_delivery, dbhub_private, dsh_project_control_private TO mcpguard_reader;
            GRANT SELECT ON shared_delivery.jobs, dbhub_private.inventory, dsh_project_control_private.pipelines TO mcpguard_reader;
        """)
        pg = urlsplit(cfg["MCPGUARD_BUSINESS_DSN"])
        relay = stack.enter_context(CountedPostgres((pg.hostname, pg.port)))
        threading.Thread(target=relay.serve_forever, daemon=True).start()
        stack.callback(relay.shutdown)
        relay_port = relay.server_address[1]
        port, reviewer = fixture.start_gateway(stack, scratch, scratch / "mcpguard", cfg, None, log)
        identities = [fixture.dev.IDS[alias] for alias in ("alice", "bob")]
        shared = enable_connector(reviewer, pg, relay_port, "shared-delivery-db", "shared_delivery", identities)
        private = [
            enable_connector(reviewer, pg, relay_port, "dbhub-inventory-db", "dbhub_private", [identities[0]]),
            enable_connector(reviewer, pg, relay_port, "dsh-project-control-pipelines-db", "dsh_project_control_private", [identities[1]]),
        ]
        clients = []
        for index, alias in enumerate(("alice", "bob")):
            projects[index]["principalId"] = identities[index]
            projects[index]["principalAlias"] = alias
            projects[index]["privateConnectorId"] = private[index]["id"]
            clients.append(open_client(stack, install, scratch, runtime_root, workspaces[index],
                                       projects[index], alias, port, cfg[alias + "_token"]))
        print("Both installed DeepSeek Harness clients are connected from their project checkouts", flush=True)
        shared_evidence = verify(clients, projects, shared, private, reviewer, relay)
    evidence = {"status": "PASS", "runId": run_id, "client": "installed DeepSeek Harness MCP registry and executor",
                "transport": "mcpguard-connect stdio to Streamable HTTP", "backend": "isolated PostgreSQL 16.14",
                "projects": projects, "sharedConnector": shared_evidence}
    content = json.dumps(evidence, indent=2) + "\n"
    if args.report:
        args.report.write_text(content, encoding="utf-8")
    print(content, end="")


if __name__ == "__main__":
    main()
