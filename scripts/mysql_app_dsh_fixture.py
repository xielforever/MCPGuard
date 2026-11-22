#!/usr/bin/env python3
"""Verify an isolated MySQL App with Chrome and installed DeepSeek Harness."""

from argparse import ArgumentParser
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
from uuid import uuid4

from connectors_backends_fixture import published_port
from connectors_fixture import console_change, mcp_tools
from dsh_client import DSHClient, EXECUTABLE, install_path, windows
import jenkins_dsh_fixture as fixture


MYSQL_IMAGE = "mysql@sha256:24989179f58856db763f418010c1169fecc944e3ec1ea7e921572d83eb070f70"
AUTHORIZED_DB = "commerce_catalog"
PRIVATE_DB = "finance_private"
CASE_DISTINCT_DB = "commerce_Catalog"
LIST_TABLES = "connector.mysql.list_tables"
LIST_COLUMNS = "connector.mysql.list_columns"


def start_mysql(stack, scratch, run_id, log):
    container = f"mcpguard-mysql-test-{run_id}"
    label = f"mysql-app-{run_id}"
    root_secret = scratch / "mysql-root.txt"
    reader_config = scratch / "mysql-reader.cnf"
    read_password = secrets.token_hex(24)
    fixture.dev.write_private(root_secret, secrets.token_hex(24))
    fixture.dev.write_private(reader_config, f"[client]\nuser=app_catalog_reader\npassword={read_password}\n")
    stack.callback(reader_config.unlink, missing_ok=True)
    stack.callback(root_secret.unlink, missing_ok=True)
    stack.callback(fixture.stop_container, container, label, log)
    fixture.dev.docker(
        "run", "--pull", "never", "-d", "--name", container,
        "--label", f"mcpguard.fixture={label}", "-p", "127.0.0.1::3306",
        "--mount", f"type=bind,src={root_secret},dst=/run/mcpguard-root.txt,readonly",
        "--mount", f"type=bind,src={reader_config},dst=/run/mcpguard-reader.cnf,readonly",
        "-e", "MYSQL_ROOT_PASSWORD_FILE=/run/mcpguard-root.txt",
        MYSQL_IMAGE, "--lower-case-table-names=0", stdout=log, stderr=log, timeout=60,
    )
    root_query = (
        "exec", "-i", container, "sh", "-c",
        'MYSQL_PWD="$(cat /run/mcpguard-root.txt)" exec mysql --protocol=socket -uroot --batch --skip-column-names',
    )

    def ready():
        try:
            result = fixture.dev.docker(*root_query, input="SELECT VERSION(), @@skip_networking;",
                                        text=True, capture_output=True, timeout=5).stdout.strip().split("\t")
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            return None
        return result[0] if len(result) == 2 and result[1] == "0" else None

    version = fixture.wait_for(ready, "Isolated MySQL did not become ready", 120)
    fixture.dev.docker(*root_query, input=f"""
        CREATE DATABASE {AUTHORIZED_DB};
        CREATE DATABASE {PRIVATE_DB};
        CREATE DATABASE {CASE_DISTINCT_DB};
        CREATE TABLE {AUTHORIZED_DB}.orders (id BIGINT NOT NULL PRIMARY KEY, order_status VARCHAR(24) NULL);
        CREATE TABLE {AUTHORIZED_DB}.products (id BIGINT NOT NULL PRIMARY KEY);
        INSERT INTO {AUTHORIZED_DB}.orders VALUES (1, 'fixture_row_value');
        CREATE TABLE {PRIVATE_DB}.orders (finance_marker INT NOT NULL);
        CREATE TABLE {PRIVATE_DB}.payouts (payee_marker VARCHAR(32) NULL);
        CREATE TABLE {CASE_DISTINCT_DB}.orders (case_sensitive_marker INT NOT NULL);
        CREATE TABLE {CASE_DISTINCT_DB}.case_only (case_sensitive_marker INT NOT NULL);
        CREATE VIEW {AUTHORIZED_DB}.payouts_view AS SELECT payee_marker FROM {PRIVATE_DB}.payouts;
        CREATE USER 'app_catalog_reader'@'%' IDENTIFIED BY '{read_password}';
        GRANT SELECT ON {AUTHORIZED_DB}.* TO 'app_catalog_reader'@'%';
        GRANT SELECT ON {PRIVATE_DB}.* TO 'app_catalog_reader'@'%';
        GRANT SELECT ON {CASE_DISTINCT_DB}.* TO 'app_catalog_reader'@'%';
    """, text=True, stdout=log, stderr=log, timeout=15)
    sibling = fixture.dev.docker(
        "exec", "-i", container, "mysql", "--defaults-extra-file=/run/mcpguard-reader.cnf",
        "--batch", "--skip-column-names", input=f"SHOW TABLES FROM {PRIVATE_DB}; SHOW TABLES FROM {CASE_DISTINCT_DB};",
        text=True, capture_output=True, timeout=15,
    ).stdout.splitlines()
    fixture.check(sibling == ["orders", "payouts", "case_only", "orders"],
                  "Native fixture reader could not see the ordinary and case-distinct sibling schemas")
    return version, {
        "name": "orders-mysql",
        "type": "mysql",
        "config": {"host": "127.0.0.1", "port": published_port(container, 3306),
                   "database": AUTHORIZED_DB, "tls": False},
        "credentials": {"username": "app_catalog_reader", "secret": read_password},
        "principalIds": [fixture.dev.IDS["alice"]],
    }


def register_in_chrome(scratch, port, password, connection):
    browser_input = scratch / "browser-input.json"
    fixture.dev.write_private(browser_input, json.dumps({
        "url": f"http://127.0.0.1:{port}/web/",
        "loginUsername": "visitor", "loginSecret": password, "connections": [connection],
    }))
    node = shutil.which("node.exe") or "/mnt/c/Program Files/nodejs/node.exe"
    script = windows(fixture.dev.ROOT / "scripts/connectors_console_browser.mjs")
    try:
        with (scratch / "browser.log").open("w", encoding="utf-8") as diagnostics:
            completed = subprocess.run([node, script, windows(browser_input)], cwd="/mnt/c", stdin=subprocess.DEVNULL,
                                       encoding="utf-8", stdout=subprocess.PIPE, stderr=diagnostics, timeout=120)
    finally:
        browser_input.unlink(missing_ok=True)
    fixture.check(completed.returncode == 0, "Chrome MySQL App registration failed; see private browser.log")
    proof = json.loads(completed.stdout)
    app, = proof.get("connectors", [])
    fixture.check(proof.get("status") == "PASS" and app["type"] == "mysql" and app["enabled"]
                  and app["testStatus"] == "PASSED", "Chrome did not create, test and enable the MySQL App")
    return proof


def open_dsh(stack, scratch, install, port, name, token):
    log = stack.enter_context((scratch / (name + "-dsh.log")).open("w", encoding="utf-8"))
    client = stack.enter_context(DSHClient({
        "transport": "streamable-http", "url": f"http://127.0.0.1:{port}/mcp",
        "headers": {"Authorization": "Bearer " + token},
        "startupTimeoutMs": 35000, "reportWorkspace": True,
    }, log, install=install, home=scratch / (name + "-dsh-home")))
    return client, client.receive("ready", timeout=55)


def rejects(client, name, arguments, expected=None):
    reply = client.call(name, arguments)
    result = reply.get("result", reply)
    fixture.check(result.get("isError") is True, f"DSH unexpectedly accepted {name}")
    detail = result.get("structuredContent") or {}
    error = detail.get("error") or {}
    code = error.get("code") or detail.get("clientError")
    if expected:
        fixture.check(code == expected, f"Unexpected denial code for {name}")
    return code or "REJECTED"


def verify(stack, scratch, install, port, cfg, operator, browser, connection):
    app, = browser["connectors"]
    target = {"connectorId": app["id"]}
    alice, ready = open_dsh(stack, scratch, install, port, "alice", cfg["alice_token"])
    bob, _ = open_dsh(stack, scratch, install, port, "bob", cfg["bob_token"])
    registered = [name for name in ready["names"] if "connector_mysql_" in name]
    fixture.check(len(registered) == 2 and all(any(tool.replace(".", "_") in name for name in registered)
                                              for tool in (LIST_TABLES, LIST_COLUMNS)), "Unexpected DSH MySQL tool catalog")
    available = fixture.call(alice, "connector.list", {})["connectors"]
    fixture.check([row["id"] for row in available] == [app["id"]], "Authorized DSH identity did not discover the MySQL App")
    fixture.check(fixture.call(bob, "connector.list", {})["connectors"] == [], "Ungranted identity discovered the MySQL App")
    tables = fixture.call(alice, LIST_TABLES, target)
    fixture.check(tables == {"database": AUTHORIZED_DB, "tables": ["orders", "products"], "truncated": False},
                  "DSH reported tables outside the saved database or did not return its expected catalog")
    columns = fixture.call(alice, LIST_COLUMNS, {**target, "table": "orders"})
    fixture.check(columns == {"database": AUTHORIZED_DB, "table": "orders", "truncated": False, "columns": [
        {"name": "id", "dataType": "bigint", "nullable": False, "ordinalPosition": 1},
        {"name": "order_status", "dataType": "varchar", "nullable": True, "ordinalPosition": 2},
    ]}, "DSH returned unexpected fields or included details beyond the authorized field metadata")

    raw = mcp_tools(port, cfg["alice_token"])
    raw(LIST_TABLES, {**target, "database": PRIVATE_DB}, False)
    raw(LIST_COLUMNS, {**target, "table": "orders", "database": PRIVATE_DB}, False)
    qualified = rejects(alice, LIST_COLUMNS, {**target, "table": PRIVATE_DB + ".orders"})
    for unavailable in ("payouts", "case_only", "payouts_view", "Orders"):
        rejects(alice, LIST_COLUMNS, {**target, "table": unavailable}, "ACCESS_DENIED")
    denial = rejects(bob, LIST_TABLES, target, "ACCESS_DENIED")

    bad = console_change(operator, "POST", "", {**connection, "name": "mysql-invalid-secret",
                         "credentials": {**connection["credentials"], "secret": uuid4().hex}})
    failed = console_change(operator, "POST", "/" + bad["id"] + "/test", {}, bad["revision"])
    fixture.check(failed["testStatus"] == "FAILED" and not failed["enabled"], "Bad MySQL credentials did not fail closed")
    console_change(operator, "PATCH", "/" + bad["id"], {"enabled": True}, failed["revision"], expected=409)

    stopped = console_change(operator, "PATCH", "/" + app["id"], {"enabled": False}, app["revision"])
    fixture.check(not stopped["enabled"] and fixture.call(alice, "connector.list", {})["connectors"] == [],
                  "Stopped MySQL App still appeared in the same DSH session")
    stopped_code = rejects(alice, LIST_COLUMNS, {**target, "table": "orders"}, "ACCESS_DENIED")
    status, _, body = fixture.http(port, "GET", f"/console/api/connectors/{app['id']}/events", operator.headers)
    fixture.check(status == 200, "Connector audit could not be read")
    events = [entry["type"] for entry in json.loads(body)["events"]]
    fixture.check("DISABLED" in events, "MySQL App stop event was not audited")

    return {
        "transport": "streamable-http", "runtimeVersion": ready["runtimeVersion"], "registeredMySQLTools": registered,
        "authorizedTableMetadata": tables, "authorizedFieldMetadata": columns,
        "negativeChecks": {
            "siblingDatabaseVisibleToNativeReader": True, "siblingOnlyTableMetadataHidden": True,
            "caseDistinctDatabaseMetadataHidden": True, "caseDistinctTableAliasDenied": True,
            "viewMetadataDenied": True,
            "qualifiedTableRejected": qualified, "databaseOverrideRejectedByGateway": True,
            "ungrantedIdentityDenied": denial, "badCredentialsProbeFailedAndActivationRejected": True,
            "stoppedAppDeniedInSameSession": stopped_code,
        },
        "auditEvents": events,
    }


def main():
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path)
    parser.add_argument("--gateway-binary", type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    install = install_path()
    fixture.check((install / EXECUTABLE).is_file(), "The installed DeepSeek Harness client is unavailable")
    run_id = uuid4().hex[:12]
    scratch = Path(tempfile.mkdtemp(prefix="mcpguard-mysql-app-"))
    print(f"Preparing {run_id}; private fixture logs in {scratch}", flush=True)
    with ExitStack() as stack:
        log = stack.enter_context((scratch / "fixture.log").open("w", encoding="utf-8"))
        binary = args.gateway_binary.resolve(strict=True) if args.gateway_binary else fixture.build(scratch, log)
        mysql_version, connection = start_mysql(stack, scratch, run_id, log)
        cfg = fixture.prepare_metadata(stack, scratch, binary, run_id, log)
        cfg["MCPGUARD_CONNECTOR_ALLOWED_CIDRS"] = "127.0.0.1/32"
        fixture.dev.psql(cfg, "mcpguard_meta", f"INSERT INTO mcpguard.control_members VALUES ('{fixture.dev.ORG}','{fixture.dev.IDS['visitor']}',true)")
        browser = {}

        def prepare_browser(port, password):
            browser.update(register_in_chrome(scratch, port, password, connection))

        port, operator = fixture.start_gateway(stack, scratch, binary, cfg, None, log, prepare_console=prepare_browser)
        dsh = verify(stack, scratch, install, port, cfg, operator, browser, connection)
        with binary.open("rb") as gateway:
            digest = hashlib.file_digest(gateway, "sha256").hexdigest()
        proof = json.dumps({"status": "PASS", "runId": run_id, "mysqlImage": MYSQL_IMAGE, "mysqlVersion": mysql_version,
                            "gatewaySHA256": digest, "browser": browser, "dsh": dsh}, indent=2) + "\n"
        if args.report:
            args.report.write_text(proof, encoding="utf-8")
        print(proof, end="", flush=True)


if __name__ == "__main__":
    main()
