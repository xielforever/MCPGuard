"""Provision private Redis and Kubernetes fixtures for console connector acceptance."""

import json
import os
import secrets
import socket
import subprocess

import dev
from jenkins_dsh_fixture import check, stop_container, wait_for


REDIS_IMAGE = "redis:7.4-alpine"
KUBERNETES_IMAGE = "rancher/k3s:v1.35.10-k3s1"
REDIS_KEY_PREFIX = "orders:"
REDIS_PROBE_KEY = REDIS_KEY_PREFIX + "x" * (1024 - len(REDIS_KEY_PREFIX))
REDIS_PROBE_VALUE = "ready"
KUBERNETES_NAMESPACE = "orders-verification"
KUBERNETES_DEPLOYMENT = "orders-validation"


def published_port(container, port):
    current = dev.inspect(container)
    binding, = current["NetworkSettings"]["Ports"][f"{port}/tcp"]
    check(binding["HostIp"] == "127.0.0.1", "Fixture container has an unexpected published address")
    return int(binding["HostPort"])


def redis_reply(conn, response, *arguments):
    request = bytearray(f"*{len(arguments)}\r\n".encode())
    for argument in arguments:
        value = argument.encode()
        request.extend(f"${len(value)}\r\n".encode() + value + b"\r\n")
    conn.sendall(request)
    return response.readline(128)


def start_redis(stack, scratch, run_id, log):
    container = f"mcpguard-connectors-redis-{run_id}"
    label = f"connectors-redis-{run_id}"
    setup_password, read_password = (secrets.token_hex(24) for _ in range(2))
    acl = scratch / "connector-redis.acl"
    dev.write_private(acl, "\n".join([
        "user default off",
        f"user fixture-setup on >{setup_password} ~* +ping +set",
        f"user reader on >{read_password} ~{REDIS_KEY_PREFIX}* +ping +get",
        "",
    ]))
    stack.callback(stop_container, container, label, log)
    dev.docker(
        "run", "--pull", "never", "-d", "--name", container,
        "--label", f"mcpguard.fixture={label}", "--user", f"{os.getuid()}:{os.getgid()}",
        "-p", "127.0.0.1::6379", "--workdir", "/tmp",
        "--mount", f"type=bind,src={acl},dst=/tmp/users.acl,readonly",
        REDIS_IMAGE, "redis-server", "--aclfile", "/tmp/users.acl", "--save", "", "--appendonly", "no",
        stdout=log, stderr=log,
    )
    port = published_port(container, 6379)

    def seed_fixture():
        with socket.create_connection(("127.0.0.1", port), timeout=3) as session, session.makefile("rb") as response:
            return (
                redis_reply(session, response, "AUTH", "fixture-setup", setup_password) == b"+OK\r\n"
                and redis_reply(session, response, "SET", REDIS_PROBE_KEY, REDIS_PROBE_VALUE) == b"+OK\r\n"
                and redis_reply(session, response, "SET", "shipping:unrelated", "other-scope") == b"+OK\r\n"
            )

    wait_for(seed_fixture, "Isolated Redis did not accept the fixture keys", 40)
    with socket.create_connection(("127.0.0.1", port), timeout=3) as session, session.makefile("rb") as response:
        check(redis_reply(session, response, "AUTH", "reader", read_password) == b"+OK\r\n", "Redis reader could not authenticate")
        check(redis_reply(session, response, "GET", REDIS_PROBE_KEY) == b"$5\r\n" and response.readline(16) == b"ready\r\n",
              "Redis reader could not read its fixture key")
        check(redis_reply(session, response, "GET", "shipping:unrelated").startswith(b"-NOPERM "),
              "Redis reader can access an unrelated key scope")
        check(redis_reply(session, response, "SET", REDIS_PROBE_KEY, "changed").startswith(b"-NOPERM "),
              "Redis reader can modify fixture keys")
    return {
        "name": "orders-cache",
        "type": "redis",
        "config": {
            "host": "127.0.0.1", "port": port, "tls": False,
            "keyPrefix": REDIS_KEY_PREFIX, "probeKey": REDIS_PROBE_KEY,
        },
        "credentials": {"username": "reader", "secret": read_password},
    }


def kubernetes_resources(namespace, deployment, account):
    label = {"app": deployment}
    return {
        "apiVersion": "v1",
        "kind": "List",
        "items": [
            {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": namespace}},
            {"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": account, "namespace": namespace}},
            {
                "apiVersion": "rbac.authorization.k8s.io/v1",
                "kind": "Role",
                "metadata": {"name": account, "namespace": namespace},
                "rules": [{"apiGroups": ["apps"], "resources": ["deployments"], "verbs": ["get", "list"]}],
            },
            {
                "apiVersion": "rbac.authorization.k8s.io/v1",
                "kind": "RoleBinding",
                "metadata": {"name": account, "namespace": namespace},
                "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": account},
                "subjects": [{"kind": "ServiceAccount", "name": account, "namespace": namespace}],
            },
            {
                "apiVersion": "apps/v1",
                "kind": "Deployment",
                "metadata": {"name": deployment, "namespace": namespace},
                "spec": {
                    "replicas": 0,
                    "selector": {"matchLabels": label},
                    "template": {"metadata": {"labels": label}, "spec": {
                        "automountServiceAccountToken": False,
                        "containers": [{"name": "validation", "image": "registry.k8s.io/pause:3.10"}],
                    }},
                },
            },
        ],
    }


def start_kubernetes(stack, scratch, run_id, log):
    container = f"mcpguard-connectors-k8s-{run_id}"
    label = f"connectors-k8s-{run_id}"
    stack.callback(stop_container, container, label, log)
    dev.docker(
        "run", "--pull", "never", "-d", "--name", container, "--label", f"mcpguard.fixture={label}",
        "-p", "127.0.0.1::6443",
        KUBERNETES_IMAGE, "server", "--disable-agent",
        "--disable=traefik,servicelb,local-storage,metrics-server,coredns",
        "--flannel-backend=none", "--disable-network-policy", "--disable-kube-proxy", "--tls-san", "127.0.0.1",
        stdout=log, stderr=log,
    )
    port = published_port(container, 6443)

    def ready():
        try:
            response = dev.docker("exec", container, "kubectl", "get", "--raw", "/readyz",
                                  capture_output=True, timeout=8)
            return response.stdout.strip() == b"ok"
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            return False

    wait_for(ready, "Agentless Kubernetes API did not become ready", 120)
    account = "mcpguard-workload-reader"
    manifest = json.dumps(kubernetes_resources(KUBERNETES_NAMESPACE, KUBERNETES_DEPLOYMENT, account)).encode()
    dev.docker("exec", "-i", container, "kubectl", "create", "-f", "-", input=manifest,
               stdout=log, stderr=log, timeout=30)
    role_identity = f"system:serviceaccount:{KUBERNETES_NAMESPACE}:{account}"

    def allowed(verb, namespace):
        try:
            outcome = dev.docker("exec", container, "kubectl", "auth", "can-i", verb, "deployments.apps",
                                 "--namespace", namespace, "--as", role_identity, capture_output=True, timeout=10)
        except subprocess.CalledProcessError as error:
            check(error.returncode == 1 and error.stdout.strip() == b"no", "Could not verify fixture Kubernetes RBAC")
            return False
        return outcome.stdout.strip() == b"yes"

    check(allowed("get", KUBERNETES_NAMESPACE) and allowed("list", KUBERNETES_NAMESPACE),
          "Kubernetes fixture service account cannot read its namespace")
    check(not allowed("list", "default") and not allowed("patch", KUBERNETES_NAMESPACE),
          "Kubernetes fixture service account has excessive privileges")
    token = dev.docker("exec", container, "kubectl", "create", "token", account,
                       "--namespace", KUBERNETES_NAMESPACE, "--duration=30m", capture_output=True, timeout=15).stdout.decode().strip()
    ca = dev.docker("exec", container, "cat", "/var/lib/rancher/k3s/server/tls/server-ca.crt",
                    capture_output=True, timeout=10).stdout.decode()
    check(len(token.split(".")) == 3 and "BEGIN CERTIFICATE" in ca, "Kubernetes fixture credentials are incomplete")
    return {
        "name": "orders-workloads",
        "type": "kubernetes",
        "config": {"baseUrl": f"https://127.0.0.1:{port}", "namespace": KUBERNETES_NAMESPACE, "caCertificate": ca},
        "credentials": {"secret": token},
    }
