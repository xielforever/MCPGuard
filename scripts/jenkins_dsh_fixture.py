#!/usr/bin/env python3
"""Run the installed Windows DeepSeek Harness against disposable Jenkins and MCPGuard in WSL."""

import argparse
import base64
from contextlib import ExitStack, contextmanager
import hashlib
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlencode, urlsplit
from uuid import UUID, uuid4
from xml.etree import ElementTree

import dev
from dsh_client import DSHClient, EXECUTABLE, install_path, windows


JENKINS_IMAGE = "jenkins/jenkins@sha256:74401de6a6f92e1afe1b02d1c8e3d0c33f263bfb5275dd8813f9150355f2367b"
GO_IMAGE = "dbhub-local/golang:1.26.9-bookworm"
ORDERS = "orders-validation"
INVENTORY = "inventory-verification"
FIXTURE = dev.ROOT / "scripts/fixtures/jenkins/20-mcpguard-ci.groovy"


def check(test, message):
    if not test:
        raise AssertionError(message)


def wait_for(predicate, message, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            result = predicate()
            if result:
                return result
        except OSError:
            pass
        time.sleep(0.4)
    raise AssertionError(message)


def http(port, method, path, headers=None, body=None):
    connection = HTTPConnection("127.0.0.1", port, timeout=10)
    try:
        connection.request(method, path, headers=headers or {}, body=body)
        result = connection.getresponse()
        return result.status, result.headers, result.read()
    finally:
        connection.close()


def basic(username, token):
    return "Basic " + base64.b64encode(f"{username}:{token}".encode()).decode()


class Jenkins:
    def __init__(self, port, token):
        self.port = port
        self.headers = {"Authorization": basic("admin", token)}

    def get(self, path, tree):
        status, _, body = http(self.port, "GET", path + "?" + urlencode({"tree": tree}), self.headers)
        check(status == 200, f"Jenkins read {path} returned HTTP {status}")
        return json.loads(body)

    def ready(self):
        status, headers, body = http(self.port, "GET", "/api/json?tree=jobs[name]", self.headers)
        if status == 200 and {it["name"] for it in json.loads(body)["jobs"]} == {ORDERS, INVENTORY}:
            return headers["X-Jenkins"]

    def builds(self, job):
        return self.get(f"/job/{job}/api/json", "builds[number,queueId,building,result,actions[parameters[name,value]]]")["builds"]

    def completed(self, job, count):
        builds = self.builds(job)
        check(len(builds) <= count, f"{job} produced an unexpected additional native build")
        return builds if len(builds) == count and all(it["result"] == "SUCCESS" for it in builds) else None

    def change_inventory_choices(self):
        path = f"/job/{INVENTORY}/config.xml"
        status, _, xml = http(self.port, "GET", path, self.headers)
        check(status == 200, "Cannot read Jenkins inventory config")
        job = ElementTree.fromstring(xml)
        choices = job.find(".//hudson.model.ChoiceParameterDefinition/choices")
        check(choices is not None, "Inventory Jenkins choice parameter missing from job XML")
        selected = next(it for it in choices.iter() if it.text == "fast")
        selected.text = "safe"
        headers = {**self.headers, "Content-Type": "application/xml"}
        status, _, _ = http(self.port, "POST", path, headers, ElementTree.tostring(job, encoding="utf-8"))
        check(status == 200, f"Could not change native inventory choices, HTTP {status}")


class JenkinsProxy(ThreadingHTTPServer):
    """Drops one response only after the fixture Jenkins has acknowledged the actual build POST."""

    def __init__(self):
        super().__init__(("127.0.0.1", 0), JenkinsProxyRequest)
        self.port = self.server_address[1]
        self.upstream = None
        self.lock = threading.Lock()
        self.lost_path = None
        self.posts = []

    def drop_next_orders_response(self):
        with self.lock:
            self.lost_path = f"/job/{ORDERS}/buildWithParameters"

    def receipts(self):
        with self.lock:
            return list(self.posts)


class JenkinsProxyRequest(BaseHTTPRequestHandler):
    def log_message(self, _format, *_args):
        pass

    def forward(self):
        path = urlsplit(self.path).path
        is_build = self.command == "POST" and path in {
            f"/job/{ORDERS}/buildWithParameters", f"/job/{INVENTORY}/buildWithParameters",
        }
        if self.command == "POST" and not is_build:
            self.send_error(403)
            return
        headers = {key: self.headers[key] for key in ("Authorization", "Content-Type", "Host") if key in self.headers}
        length = int(self.headers.get("Content-Length", 0))
        try:
            status, upstream_headers, body = http(self.server.upstream, self.command, self.path, headers, self.rfile.read(length) if length else None)
        except OSError:
            self.send_error(502)
            return
        if is_build:
            location = upstream_headers.get("Location")
            with self.server.lock:
                drop = self.server.lost_path == path and status in (201, 303) and bool(location)
                if drop:
                    self.server.lost_path = None
                queue_path = urlsplit(location).path if location else None
                self.server.posts.append({"path": path, "status": status, "queuePath": queue_path, "replyDropped": drop})
            if drop:
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)
                return
        self.send_response(status)
        for key in ("Content-Type", "Location"):
            if key in upstream_headers:
                self.send_header(key, upstream_headers[key])
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = forward


def stop_container(name, label, log):
    owned = dev.inspect(name)
    if owned and owned["Config"]["Labels"].get("mcpguard.fixture") == label:
        dev.docker("logs", name, stdout=log, stderr=log)
        dev.docker("rm", "-f", "-v", name, capture_output=True)


def start_jenkins(stack, scratch, run_id, proxy, log):
    home = scratch / "jenkins-home"
    init = home / "init.groovy.d"
    init.mkdir(mode=0o700, parents=True)
    shutil.copyfile(FIXTURE, init / FIXTURE.name)
    name = f"mcpguard-jenkins-test-{run_id}"
    label = f"jenkins-acceptance-{run_id}"
    admin_token, api_token = ("11" + secrets.token_hex(16) for _ in range(2))
    proxy_url = f"http://127.0.0.1:{proxy.port}"
    dev.write_private(scratch / "jenkins.env", "\n".join([
        "JAVA_OPTS=-Djenkins.install.runSetupWizard=false -Xmx512m",
        f"MCPGUARD_FIXTURE_ADMIN_TOKEN={admin_token}", f"MCPGUARD_FIXTURE_GATEWAY_TOKEN={api_token}",
        f"MCPGUARD_FIXTURE_PROXY_URL={proxy_url}", "",
    ]))
    stack.callback(stop_container, name, label, log)
    dev.docker("run", "--pull", "never", "-d", "--name", name, "--label", f"mcpguard.fixture={label}",
               "--user", f"{os.getuid()}:{os.getgid()}", "--env-file", str(scratch / "jenkins.env"),
               "-p", "127.0.0.1::8080", "--mount", f"type=bind,src={home},dst=/var/jenkins_home",
               JENKINS_IMAGE, stdout=log, stderr=log)
    info = dev.inspect(name)
    proxy.upstream = int(info["NetworkSettings"]["Ports"]["8080/tcp"][0]["HostPort"])
    jenkins = Jenkins(proxy.upstream, admin_token)
    version = wait_for(jenkins.ready, "Disposable Jenkins jobs did not become ready")
    return jenkins, {"orgId": dev.ORG, "controllerId": "jenkins-acceptance", "baseUrl": proxy_url,
                     "username": "gateway", "apiToken": api_token, "folders": [""]}, version


def build(scratch, log):
    dev.docker("run", "--rm", "--pull", "never", "--network", "none",
               "--mount", f"type=bind,src={dev.ROOT},dst=/work,readonly",
               "--mount", f"type=bind,src={scratch},dst=/fixture",
               "--mount", "type=volume,src=mcpguard-ra-go-build,dst=/root/.cache/go-build",
               "--mount", "type=volume,src=mcpguard-ra-go-modules,dst=/go/pkg/mod",
               "-w", "/work", "-e", "GOTOOLCHAIN=local", "-e", "GOPROXY=off", "-e", "GOFLAGS=-mod=readonly -buildvcs=false",
               GO_IMAGE, "go", "build", "-p", "2", "-o", "/fixture/mcpguard", "./cmd/mcpguard", stdout=log, stderr=log)
    return scratch / "mcpguard"


@contextmanager
def isolated_dev(scratch, binary):
    state, compile_gateway, run = dev.STATE, dev.build, dev.run
    dev.STATE = scratch / "metadata"
    dev.build = lambda: None

    def run_in_fixture(args, **kwargs):
        if args == [str(dev.ROOT / "bin/mcpguard"), "migrate"]:
            args = [str(binary), "migrate"]
        return run(args, **kwargs)

    dev.run = run_in_fixture
    try:
        yield
    finally:
        dev.STATE, dev.build, dev.run = state, compile_gateway, run


def prepare_metadata(stack, scratch, binary, run_id, log):
    cfg = {
        "container": f"mcpguard-jenkins-pg-{run_id}",
        **{key: secrets.token_hex(24) for key in ("admin_password", "app_password", "reader_password")},
        **{key: base64.b64encode(secrets.token_bytes(32)).decode() for key in ("MCPGUARD_ENCRYPTION_KEY", "MCPGUARD_PLAN_KEY")},
        **{name + "_token": secrets.token_urlsafe(32) for name in dev.IDS},
        "MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_LISTEN": f"127.0.0.1:{dev.free_port()}",
    }
    stack.callback(stop_container, cfg["container"], "read-slice", log)
    with isolated_dev(scratch, binary):
        dev.STATE.mkdir(mode=0o700)
        dev.write_private(dev.STATE / "env.json", json.dumps(cfg))
        dev.up()
        cfg = dev.load()
    applicant, reviewer = dev.IDS["alice"], dev.IDS["visitor"]
    dev.psql(cfg, "mcpguard_meta", f"""
        INSERT INTO mcpguard.control_heads(org_id) VALUES ('{dev.ORG}');
        INSERT INTO mcpguard.control_resources(org_id,name,id,enabled) VALUES ('{dev.ORG}','jenkins','{uuid4()}',true);
        INSERT INTO mcpguard.tool_grants VALUES ('{dev.ORG}','{applicant}','jenkins.submit_build',true);
        INSERT INTO mcpguard.reviewer_grants VALUES ('{dev.ORG}','{reviewer}','jenkins',true);
    """)
    return cfg


class Reviewer:
    def __init__(self, port, password):
        self.port = port
        self.origin = f"http://127.0.0.1:{port}"
        self.headers = {"Origin": self.origin, "Content-Type": "application/json"}
        status, headers, body = http(port, "POST", "/console/login", self.headers, json.dumps({"username": "visitor", "password": password}))
        check(status == 200, f"Independent MCPGuard reviewer login returned HTTP {status}")
        self.headers["Cookie"] = headers["Set-Cookie"].split(";", 1)[0]
        self.headers["X-CSRF-Token"] = json.loads(body)["csrf"]

    def approve(self, action_id):
        path = "/console/api/actions/" + action_id
        status, _, body = http(self.port, "GET", path, self.headers)
        check(status == 200, f"Reviewer could not read pending action, HTTP {status}")
        action = json.loads(body)
        check(action["state"] == "PENDING_APPROVAL" and action["canReview"], "Action is not pending for independent review")
        headers = {**self.headers, "If-Match": f'"{action["revision"]}"', "Idempotency-Key": str(uuid4())}
        status, _, body = http(self.port, "POST", path + "/decision", headers, json.dumps({"vote": "APPROVE", "planHash": action["planHash"]}))
        check(status == 200, f"Reviewer could not approve action, HTTP {status}")
        return json.loads(body)

    def enable_connector(self, connector, enabled):
        path = "/console/api/connectors/" + connector["id"]
        headers = {**self.headers, "If-Match": f'"{connector["revision"]}"', "Idempotency-Key": str(uuid4())}
        status, _, body = http(self.port, "PATCH", path, headers, json.dumps({"enabled": enabled}))
        check(status == 200, f"Reviewer could not change the Jenkins App state, HTTP {status}")
        return json.loads(body)


def start_gateway(stack, scratch, binary, cfg, controller, log, *, prepare_console=None):
    password = secrets.token_urlsafe(32)
    console = scratch / "console.json"
    settings = scratch / "jenkins.json"
    dev.write_private(console, json.dumps([{
        "Username": "visitor", "OrgID": dev.ORG, "PrincipalID": dev.IDS["visitor"],
        "PasswordHash": hashlib.sha256(password.encode()).hexdigest(),
    }]))
    env = dev.runtime_env(cfg)
    env.update(MCPGUARD_CONSOLE_CONFIG=str(console), MCPGUARD_LEGACY_DEMO="disabled")
    if controller is not None:
        dev.write_private(settings, json.dumps(controller))
        env["MCPGUARD_JENKINS_CONFIG"] = str(settings)
    gateway = stack.enter_context(subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log))
    stack.callback(gateway.terminate)
    port = int(cfg["MCPGUARD_LISTEN"].rsplit(":", 1)[1])

    def ready():
        check(gateway.poll() is None, "Isolated gateway exited before accepting traffic")
        status, _, _ = http(port, "GET", "/healthz")
        return status == 200

    wait_for(ready, "Isolated gateway did not accept traffic", 30)
    if prepare_console is not None:
        prepare_console(port, password)
    return port, Reviewer(port, password)


def create_jenkins_apps(port, password, controller, scratch):
    """Register an executable App and a compatibility read-only App in Chrome."""
    connection = {
        "type": "jenkins",
        "config": {"baseUrl": controller["baseUrl"], "folder": "/"},
        "credentials": {"username": controller["username"], "secret": controller["apiToken"]},
        "principalIds": [dev.IDS["alice"]],
    }
    browser_input = {
        "url": f"http://127.0.0.1:{port}/web/",
        "loginUsername": "visitor",
        "loginSecret": password,
        "connections": [
            {"name": "delivery-ci", **connection, "config": {**connection["config"], "allowBuilds": True},
             "principalIds": [dev.IDS["alice"], dev.IDS["bob"]]},
            {"name": "existing-read-only-ci", **connection},
        ],
    }
    node = shutil.which("node.exe") or "/mnt/c/Program Files/nodejs/node.exe"
    script = windows(dev.ROOT / "scripts/connectors_console_browser.mjs")
    private_input = scratch / "browser-input.json"
    dev.write_private(private_input, json.dumps(browser_input))
    try:
        with (scratch / "browser.log").open("w") as diagnostics:
            completed = subprocess.run([node, script, windows(private_input)], cwd="/mnt/c", stdin=subprocess.DEVNULL,
                                       encoding="utf-8", stdout=subprocess.PIPE, stderr=diagnostics, timeout=120)
    finally:
        private_input.unlink(missing_ok=True)
    check(completed.returncode == 0, "Chrome could not register, test and enable the Jenkins Apps; see browser.log")
    proof = json.loads(completed.stdout)
    check(proof["status"] == "PASS" and len(proof["connectors"]) == 2, "Chrome did not report both Jenkins Apps")
    return proof


def call(client, name, arguments, allow_error=False):
    returned = client.call(name, arguments)
    result = returned.get("result", returned)
    data = result.get("structuredContent")
    if data is None and result.get("content"):
        try:
            data = json.loads(result["content"][0].get("text", ""))
        except ValueError:
            pass
    if allow_error and result.get("isError"):
        return data or {}
    check(not result.get("isError") and isinstance(data, dict), f"Installed Harness returned no success for {name}: {data}")
    return data


def verify_app_grants(client, apps, registered):
    shared = apps["delivery-ci"]["id"]
    private = apps["existing-read-only-ci"]["id"]
    check({row["id"] for row in call(client, "connector.list", {})["connectors"]} == {shared},
          "Read-only DSH identity did not get exactly its granted Jenkins App")
    jobs = call(client, "connector.jenkins.list_jobs", {"connectorId": shared})["jobs"]
    denied = call(client, "connector.jenkins.list_jobs", {"connectorId": private}, allow_error=True)
    check({job["name"] for job in jobs} == {ORDERS, INVENTORY} and
          denied.get("error", denied).get("code") == "ACCESS_DENIED", "Shared/private Jenkins App access diverged")
    check(not any("jenkins_submit_build" in name for name in registered),
          "App grant exposed the build tool without the separate controlled tool grant")
    return {"sharedReadAllowed": True, "privateReadDenied": True, "buildToolHidden": True}


def exercise(client, reviewer, jenkins, proxy, report, apps=None):
    targets = {"connectorId": apps["delivery-ci"]["id"]} if apps else {}
    tool_prefix = "connector.jenkins." if apps else "jenkins."
    if apps:
        listed_apps = call(client, "connector.list", {})["connectors"]
        check({row["id"] for row in listed_apps} == {row["id"] for row in apps.values()},
              "DSH did not discover both Apps created in the console")
        report["app"] = {"connectorId": targets["connectorId"], "revision": apps["delivery-ci"]["revision"]}
    listed = call(client, tool_prefix + "list_jobs", targets)["jobs"]
    check({job["name"] for job in listed} == {ORDERS, INVENTORY}, "Gateway did not discover the two registered Jenkins jobs")
    jobs = {job["name"]: call(client, tool_prefix + "describe_job", {**targets, "jobRef": job["jobRef"]}) for job in listed}
    report["parameterNames"] = {name: [item["name"] for item in job["parameters"]] for name, job in jobs.items()}
    check(set(report["parameterNames"][ORDERS]) == {"GIT_REF", "RUN_TESTS"} and set(report["parameterNames"][INVENTORY]) == {"PROFILE", "RELEASE_TAG"}, "Dynamic parameters differ from actual Jenkins definitions")

    scenarios = report["scenarios"] = {}

    def submission(job, parameters):
        return {**targets, "jobRef": jobs[job]["jobRef"], "schemaRevision": jobs[job]["schemaRevision"],
                "parameters": parameters, "requestKey": str(uuid4())}

    def prepare(args):
        action = call(client, "jenkins.submit_build", args)
        check(action["kind"] == "DEFERRED" and action["state"] == "PENDING_APPROVAL", "Jenkins submission did not wait for independent approval")
        check(action["dispatchState"] == "NOT_DISPATCHED", "Jenkins submission dispatched before approval")
        return action["actionId"]

    def status(action_id):
        return call(client, "action.status", {"actionId": action_id})

    def resume(action_id):
        return call(client, "action.resume", {"actionId": action_id})

    def terminal(action_id):
        value = status(action_id)
        return value if value["action"]["state"] in {"SUCCEEDED", "FAILED", "OUTCOME_UNKNOWN"} else None

    def after_resume(action_id):
        resume(action_id)
        return wait_for(lambda: terminal(action_id), "Controlled Jenkins action did not finish", 25)

    def native_build(job, expected_count, queue_id, parameters):
        builds = wait_for(lambda: jenkins.completed(job, expected_count), "Native Jenkins builds did not finish")
        matching = [it for it in builds if str(it["queueId"]) == str(queue_id)]
        check(len(matching) == 1, "Returned queue ID did not identify exactly one native Jenkins build")
        received = {param["name"]: param["value"] for action in matching[0]["actions"] for param in action.get("parameters", [])}
        check(received == parameters, "Jenkins build did not receive the approved values")
        return matching[0]["number"]

    if apps:
        read_only = {"connectorId": apps["existing-read-only-ci"]["id"]}
        read_jobs = call(client, tool_prefix + "list_jobs", read_only)["jobs"]
        read_job = next(job for job in read_jobs if job["name"] == ORDERS)
        read_definition = call(client, tool_prefix + "describe_job", {**read_only, "jobRef": read_job["jobRef"]})
        denied = call(client, "jenkins.submit_build", {
            **read_only, "jobRef": read_job["jobRef"], "schemaRevision": read_definition["schemaRevision"],
            "parameters": {"GIT_REF": "release/read-only-check", "RUN_TESTS": False}, "requestKey": str(uuid4()),
        }, allow_error=True)
        check(denied.get("error", denied).get("code") == "ACCESS_DENIED" and not proxy.receipts(),
              "Existing read-only connector accepted a build submission")
        report["readOnlyConnector"] = {"connectorId": read_only["connectorId"], "readAllowed": True, "buildDenied": True}

    for job, parameters in ((ORDERS, {"GIT_REF": "release/orders", "RUN_TESTS": False}),
                            (INVENTORY, {"PROFILE": "full", "RELEASE_TAG": "release-2026"})):
        before = len(proxy.receipts())
        args = submission(job, parameters)
        action_id = prepare(args)
        check(call(client, "jenkins.submit_build", args)["actionId"] == action_id, "Matching retry returned a different pending action")
        check(len(proxy.receipts()) == before and not jenkins.builds(job), "Jenkins built before approval")
        reviewer.approve(action_id)
        check(status(action_id)["action"]["state"] == "READY" and len(proxy.receipts()) == before, "Approval dispatched without applicant resume")
        result = after_resume(action_id)
        check(result["action"]["state"] == "SUCCEEDED", f"Approved Jenkins action did not return a confirmed receipt: {result['result']}")
        queue_id = result["result"]["data"]["queueId"]
        queue = call(client, "jenkins.get_queue", {"actionId": action_id})
        check(str(queue["queueId"]) == str(queue_id), "DSH queue query returned a different native receipt")
        check(call(client, "jenkins.submit_build", args)["actionId"] == action_id, "Same key did not restore original action after completion")
        changed = {**args, "parameters": {**parameters, **({"GIT_REF": "different"} if job == ORDERS else {"RELEASE_TAG": "different"})}}
        indicated = call(client, "jenkins.submit_build", changed, allow_error=True)
        check(indicated.get("error", indicated).get("category") == "CONFLICT", f"Changed parameters using the original key did not return a conflict: {indicated}")
        native_number = native_build(job, 1, queue_id, parameters)
        check(len(proxy.receipts()) == before + 1, "Repeated Jenkins submission sent another build POST")
        scenarios[job] = {"actionId": action_id, "queueId": queue_id, "nativeBuild": native_number, "postCount": 1}

    drift_args = submission(INVENTORY, {"PROFILE": "fast", "RELEASE_TAG": "drift-check"})
    drift_id = prepare(drift_args)
    jenkins.change_inventory_choices()
    description = call(client, tool_prefix + "describe_job", {**targets, "jobRef": drift_args["jobRef"]})
    check(description["schemaRevision"] != drift_args["schemaRevision"], "Native schema change did not change Gateway schema revision")
    reviewer.approve(drift_id)
    drift = after_resume(drift_id)["result"]
    check(drift["requestStatus"] == "FAILED" and drift["dispatchState"] == "NOT_DISPATCHED"
          and drift["error"]["code"] == "JENKINS_SCHEMA_CHANGED" and len(proxy.receipts()) == 2,
          f"Jenkins schema drift did not block dispatch: {drift}")
    check(jenkins.completed(INVENTORY, 1), "Inventory build count changed after schema drift")
    scenarios["schemaDrift"] = {"actionId": drift_id, "dispatchState": drift["dispatchState"], "errorCode": drift["error"]["code"], "postCount": 0}

    parameters = {"GIT_REF": "release/lost-reply", "RUN_TESTS": True}
    lost_args = submission(ORDERS, parameters)
    lost_id = prepare(lost_args)
    reviewer.approve(lost_id)
    proxy.drop_next_orders_response()
    lost = after_resume(lost_id)["result"]
    check(lost["requestStatus"] == "OUTCOME_UNKNOWN" and lost["dispatchState"] == "DISPATCH_UNKNOWN", f"Lost reply did not produce an unknown outcome: {lost}")
    receipts = proxy.receipts()
    check(len(receipts) == 3 and receipts[-1]["replyDropped"], "Lost-reply fault did not occur after an acknowledged native build POST")
    check(call(client, "jenkins.submit_build", lost_args)["actionId"] == lost_id, "Lost-reply submission did not resolve to the existing action")
    for _ in range(2):
        call(client, "action.resume", {"actionId": lost_id}, allow_error=True)
        check(status(lost_id)["action"]["state"] == "OUTCOME_UNKNOWN", "Lost reply retry changed the terminal state")
    check(len(proxy.receipts()) == 3, "Lost reply was retransmitted to Jenkins")
    queue_id = receipts[-1]["queuePath"].strip("/").rsplit("/", 1)[-1]
    number = native_build(ORDERS, 2, queue_id, parameters)
    scenarios["lostReply"] = {"actionId": lost_id, "nativeQueueId": queue_id, "nativeBuild": number,
                              "requestStatus": lost["requestStatus"], "dispatchState": lost["dispatchState"], "postCount": 1}
    if apps:
        revision_args = submission(ORDERS, {"GIT_REF": "release/revision-check", "RUN_TESTS": False})
        revision_id = prepare(revision_args)
        reviewer.approve(revision_id)
        original = apps["delivery-ci"]
        disabled = reviewer.enable_connector(original, False)
        check(not disabled["enabled"], "Jenkins App did not record its disabled state")
        denied = call(client, "connector.jenkins.list_jobs", targets, allow_error=True)
        check(denied.get("error", denied).get("code") == "ACCESS_DENIED", "Disabled Jenkins App accepted a read")
        enabled = reviewer.enable_connector(disabled, True)
        check(enabled["enabled"] and enabled["revision"] > original["revision"],
              "Re-enabled App did not acquire a new revision")
        blocked = call(client, "action.resume", {"actionId": revision_id}, allow_error=True)
        check(blocked.get("outcome") == "NOT_RESUMABLE" and blocked["action"]["state"] == "CANCELLED"
              and blocked["action"]["dispatchState"] == "NOT_DISPATCHED" and len(proxy.receipts()) == 3,
              "Previously approved action reached Jenkins or survived an App revision change")
        scenarios["appRevision"] = {"actionId": revision_id, "dispatchState": blocked["action"]["dispatchState"],
                                    "outcome": blocked["outcome"], "postCount": 0, "disabledReadDenied": True,
                                    "previousRevision": original["revision"], "currentRevision": enabled["revision"]}
    report["nativePosts"] = proxy.receipts()
    report["nativeBuildCounts"] = {ORDERS: len(jenkins.builds(ORDERS)), INVENTORY: len(jenkins.builds(INVENTORY))}


def verify_gateway_audit(cfg, scenarios):
    results = {}
    for scenario, receipt in scenarios.items():
        action_id = str(UUID(receipt["actionId"]))
        output = dev.psql(cfg, "mcpguard_meta", f"""
            SELECT json_build_object(
                'events', (SELECT json_agg(event_type ORDER BY seq) FROM mcpguard.managed_audit
                           WHERE org_id='{dev.ORG}' AND action_id='{action_id}'),
                'executionCount', (SELECT count(*) FROM mcpguard.managed_executions
                                   WHERE org_id='{dev.ORG}' AND action_id='{action_id}')
            );
        """).stdout
        audit = json.loads(next(line.strip() for line in output.splitlines() if line.strip().startswith("{")))
        expected = ["ACTION_CREATED", "APPROVAL_APPROVE", "REVIEW_APPROVE"]
        if scenario == "appRevision":
            expected += ["PLAN_INVALIDATED"]
            execution_count = 0
        else:
            terminal = {"schemaDrift": "FAILED", "lostReply": "OUTCOME_UNKNOWN"}.get(scenario, "SUCCEEDED")
            expected += ["EXECUTION_INTENT", terminal]
            execution_count = 1
        check(audit["events"] == expected and audit["executionCount"] == execution_count,
              f"Unexpected audit or execution count for {scenario}: {audit}")
        results[scenario] = audit
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, help="Destination for sanitized JSON acceptance evidence")
    parser.add_argument("--jenkins-only", action="store_true", help="Verify the disposable Jenkins job fixture can start")
    parser.add_argument("--app", action="store_true", help="Configure Jenkins through Chrome instead of using a file")
    parser.add_argument("--gateway-binary", type=Path, help="Use an already-built Linux MCPGuard gateway instead of compiling")
    args = parser.parse_args()
    if args.app and args.jenkins_only:
        parser.error("--app requires the gateway and installed DSH")
    if not args.jenkins_only:
        harness = install_path()
        check((harness / EXECUTABLE).is_file(), "DeepSeek Harness is missing; set MCPGUARD_DSH_INSTALL to its WSL path")
    os.umask(0o077)
    scratch = Path(tempfile.mkdtemp(prefix="mcpguard-jenkins-acceptance-"))
    run_id = uuid4().hex[:12]
    print(f"Private fixture logs: {scratch}", flush=True)
    report = {"runId": run_id, "jenkinsImage": JENKINS_IMAGE}
    with ExitStack() as stack:
        proxy = stack.enter_context(JenkinsProxy())
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        stack.callback(proxy.shutdown)
        log = stack.enter_context((scratch / "fixture.log").open("w"))
        jenkins, controller, version = start_jenkins(stack, scratch, run_id, proxy, log)
        report["jenkinsVersion"] = version
        print(f"Jenkins {version} ready with two native parameterized jobs", flush=True)
        if not args.jenkins_only:
            binary = args.gateway_binary.resolve(strict=True) if args.gateway_binary else build(scratch, log)
            cfg = prepare_metadata(stack, scratch, binary, run_id, log)
            prepare_console = None
            if args.app:
                cfg["MCPGUARD_CONNECTOR_ALLOWED_CIDRS"] = "127.0.0.1/32"
                operator = dev.IDS["visitor"]
                dev.psql(cfg, "mcpguard_meta",
                         f"INSERT INTO mcpguard.control_members VALUES ('{dev.ORG}','{operator}',true)")

                def prepare_console(port, password):
                    report["browser"] = create_jenkins_apps(port, password, controller, scratch)

            port, reviewer = start_gateway(stack, scratch, binary, cfg, None if args.app else controller, log,
                                           prepare_console=prepare_console)
            config = {"transport": "streamable-http", "url": f"http://127.0.0.1:{port}/mcp",
                      "headers": {"Authorization": "Bearer " + cfg["alice_token"]}, "startupTimeoutMs": 35000,
                      "reportWorkspace": True}
            dsh_log = stack.enter_context((scratch / "dsh.log").open("w"))
            client = stack.enter_context(DSHClient(config, dsh_log, install=harness, home=scratch / "applicant-dsh"))
            ready = client.receive("ready", timeout=45)
            report["dsh"] = {"transport": "streamable-http", "runtimeVersion": ready["runtimeVersion"],
                             "registeredTools": [name for name in ready["names"] if "jenkins" in name]}
            apps = {row["name"]: row for row in report["browser"]["connectors"]} if args.app else None
            exercise(client, reviewer, jenkins, proxy, report, apps)
            report["gatewayAudit"] = verify_gateway_audit(cfg, report["scenarios"])
            if apps:
                read_config = {**config, "headers": {"Authorization": "Bearer " + cfg["bob_token"]}}
                read_log = stack.enter_context((scratch / "read-only-dsh.log").open("w"))
                with DSHClient(read_config, read_log, install=harness, home=scratch / "read-only-dsh") as read_client:
                    ready = read_client.receive("ready", timeout=45)
                    report["appGrants"] = verify_app_grants(read_client, apps, ready["names"])
    evidence = json.dumps(report, indent=2) + "\n"
    if args.report:
        args.report.write_text(evidence, encoding="utf-8")
    print(evidence, end="")


if __name__ == "__main__":
    main()
