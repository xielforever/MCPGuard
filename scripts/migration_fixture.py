#!/usr/bin/env python3
"""RA-17 migration and rollback rehearsal on fresh, loopback-only fixtures."""

import argparse
from contextlib import contextmanager
import functools
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit
from uuid import UUID

import read_fixture as read


ROOT = Path(__file__).resolve().parents[1]
STATE_ROOT = Path.home() / '.local/state/mcpguard-ra17'
BUILD = ROOT / '.dev/ra17-build'
BINARIES = ('mcpguard', 'mcpguard-catalog', 'mcpguard-provision', 'mcpguard-demo-http',
            'mcpguard-connect', 'mcpguard-credentials', 'mcpguard-session')
base = read.base
container_runner = read.context.download.credential.recovery.provision.original_runner
CHECKS = []


def check(value, description):
    if not value:
        raise AssertionError(description)
    CHECKS.append(description)


def invoke(state, name, args, env, expected=0):
    result = subprocess.run([str(BUILD / name), *args], env=env, capture_output=True, text=True, timeout=35)
    if result.returncode != expected:
        base.dev.write_private(state / 'ra17-command-error.log', result.stdout + result.stderr)
        command = ' '.join([name, *args[:1]])
        raise AssertionError(f'{command} exited {result.returncode}; expected {expected}')
    return result


def query(cfg, database, sql):
    try:
        return base.dev.docker('exec', '-i', cfg['container'], 'psql', '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1',
                               '-U', 'postgres', '-d', database, input=sql, capture_output=True, text=True).stdout.strip()
    except subprocess.CalledProcessError as error:
        base.dev.write_private(base.dev.STATE / 'ra17-postgres-error.log', error.stderr or '')
        raise AssertionError(f'PostgreSQL operation failed in isolated {database}; see private test log') from None


def ledger(cfg, action):
    action = str(UUID(action))
    return {table: query(cfg, 'mcpguard_meta', f"""
        SELECT jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text
        FROM mcpguard.{table} t WHERE {column}='{action}';
        """) for table, column in (('actions', 'id'), ('attempts', 'action_id'),
                                  ('executions', 'action_id'), ('audit_events', 'action_id'))}


def demo_digest(cfg):
    return query(cfg, 'mcpguard_business', 'SELECT md5(jsonb_agg(to_jsonb(t) ORDER BY scope_id,item_id)::text) FROM demo.items t;')


def wire_module(state):
    os.environ['MCPGUARD_TEST_STATE'] = str(state)
    spec = importlib.util.spec_from_file_location('migration_wire', ROOT / 'tests/e2e.py')
    wire = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(wire)
    return wire


@contextmanager
def gateway(state, cfg, wire, extra_env):
    port = base.dev.free_port()
    env = base.dev.runtime_env(cfg) | {'MCPGUARD_LISTEN': f'127.0.0.1:{port}'} | extra_env
    with (state / 'ra17-gateway.log').open('a') as log:
        process = subprocess.Popen([str(BUILD / 'mcpguard')], env=env, stdout=log, stderr=log)
        try:
            def ready():
                if process.poll() is not None:
                    raise AssertionError('gateway startup failed; see private RA-17 log')
                try:
                    return wire.http(port, '/healthz', method='GET')[0] == 200
                except OSError:
                    return False
            wire.until(ready, 10)
            yield port
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise AssertionError('gateway graceful shutdown timed out')


def readonly(dsn):
    parts = urlsplit(dsn)
    params = dict(parse_qsl(parts.query)) | {'default_transaction_read_only': 'on'}
    return urlunsplit(parts._replace(query=urlencode(params)))


def before_catalog(state, cfg):
    wire = wire_module(state)
    with gateway(state, cfg, wire, {}) as port:
        client = wire.HTTPMCP(port, 'alice')
        read_result = client.read()['structuredContent']
        check(read_result['requestStatus'] == 'SUCCEEDED', 'legacy read writes actual action, attempt, execution and audit records')
        action = read_result['actionId']
        names = sorted(tool['name'] for tool in client.call('tools/list')['tools'])

    baseline = ledger(cfg, action)
    check(all(baseline.values()), 'legacy ledger includes encrypted result and audit sequence')
    base.dev.write_private(state / 'ra17-baseline.json', json.dumps({
        'action': action, 'ledger': baseline, 'demo': demo_digest(cfg), 'tools': names,
    }))
    backup = base.dev.docker('exec', cfg['container'], 'pg_dump', '-U', 'postgres', '-d', 'mcpguard_meta',
                             '--schema=mcpguard', capture_output=True, text=True).stdout
    base.dev.write_private(state / 'ra17-legacy-backup.sql', backup)

    env = base.dev.runtime_env(cfg) | {'MCPGUARD_MIGRATION_DSN': readonly(cfg['MCPGUARD_MIGRATION_DSN'])}
    metadata_plan = json.loads(invoke(state, 'mcpguard', ['migrate', '--dry-run'], env).stdout)
    check(metadata_plan['currentVersion'] == 4 and metadata_plan['pendingVersions'] == [], 'legacy migration preflight verifies versions 1–4')
    catalog_plan = json.loads(invoke(state, 'mcpguard-catalog', ['migrate', '--dry-run'], env).stdout)
    check(catalog_plan['currentVersion'] == 0 and catalog_plan['pendingVersions'] == list(range(1, 14)), 'catalog preflight lists the pending changes on the legacy database')
    invoke(state, 'mcpguard-catalog', ['migrate'], env, expected=1)
    check(query(cfg, 'mcpguard_meta', "SELECT to_regnamespace('mcpguard_ra') IS NULL;") == 't'
          and ledger(cfg, action) == baseline, 'preflight and rejected apply preserve schema and legacy ledger')


def provider_upgrade(state, cfg):
    # The provider deliberately supports a fixed database name. Use a separate
    # PostgreSQL instance to keep the live v4 fixtures intact.
    pg = {'container': 'mcpguard-ra17-provider-' + cfg['runId']}
    password = secrets.token_hex(24)
    envfile = state / 'ra17-provider-postgres.env'
    base.dev.write_private(envfile, 'POSTGRES_PASSWORD=' + password + '\n')
    base.dev.docker('run', '-d', '--name', pg['container'], '--label', 'mcpguard.fixture=ra17',
                    '--label', 'mcpguard.run=' + cfg['runId'], '--env-file', str(envfile),
                    '-p', '127.0.0.1::5432', base.dev.IMAGE, capture_output=True)
    base.wait_pg(pg['container'])
    port = base.dev.inspect(pg['container'])['NetworkSettings']['Ports']['5432/tcp'][0]['HostPort']
    database = 'mcpguard_business'
    query(pg, 'postgres', f'CREATE DATABASE {database};')
    query(pg, 'postgres', 'CREATE ROLE mcpguard_account_manager NOLOGIN;')
    schema = base.dev.docker('exec', cfg['raPgAdmins'][0]['container'], 'pg_dump', '-U', 'postgres', '-d', database,
                             '--schema=resource_demo', '--schema-only', '--no-owner', '--no-privileges',
                             capture_output=True, text=True).stdout
    query(pg, database, schema)
    directory = ROOT / 'internal/resources/native_provider'
    for version, path in enumerate(sorted(directory.glob('*.sql'))[:3], 1):
        sql = path.read_text(encoding='utf-8')
        # Emulate the byte checksum written by an earlier Windows checkout.
        previous = hashlib.sha256(sql.replace('\n', '\r\n').encode()).hexdigest()
        query(pg, database, sql + f"\nINSERT INTO mcpguard_provision.schema_versions VALUES ({version},'{previous}');")
    before = query(pg, database, 'SELECT checksum FROM mcpguard_provision.schema_versions ORDER BY version;')
    provider = state / 'ra17-provider.json'
    base.dev.write_private(provider, json.dumps({'schemaVersion': '0.2', 'targets': [], 'existingAccounts': []}))
    dsn = f'postgres://postgres:{password}@127.0.0.1:{port}/{database}?sslmode=disable'
    env = base.dev.runtime_env(cfg) | {'MCPGUARD_PROVIDER_ADMIN_DSN': readonly(dsn),
                                     'MCPGUARD_PROVIDER_MANAGER_ROLE': 'mcpguard_account_manager'}
    args = ['install-provider', str(provider)]
    plan = json.loads(invoke(state, 'mcpguard-provision', args + ['--dry-run'], env).stdout)
    check(plan['currentVersion'] == 3 and plan['pendingVersions'] == [4, 5], 'native provider preflight recognizes installed v3 and historical CRLF checksums')
    check(query(pg, database, "SELECT to_regclass('mcpguard_provision.fence_state') IS NULL;") == 't',
          'native preflight does not create the recovery fence')
    env['MCPGUARD_PROVIDER_ADMIN_DSN'] = dsn
    for _ in range(2):
        invoke(state, 'mcpguard-provision', args, env)
    plan = json.loads(invoke(state, 'mcpguard-provision', args + ['--dry-run'], env).stdout)
    check(plan['currentVersion'] == 5 and plan['pendingVersions'] == []
          and before == query(pg, database, 'SELECT checksum FROM mcpguard_provision.schema_versions WHERE version<=3 ORDER BY version;'),
          'native upgrade is idempotent and preserves all installed checksums')
    query(pg, database, "UPDATE mcpguard_provision.schema_versions SET checksum='bad' WHERE version=3;")
    invoke(state, 'mcpguard-provision', args + ['--dry-run'], env, expected=1)
    invoke(state, 'mcpguard-provision', args, env, expected=1)
    check(query(pg, database, 'SELECT max(version) FROM mcpguard_provision.schema_versions;') == '5',
          'native preflight and apply both reject a changed checksum')


def v2_digest(cfg):
    return {table: query(cfg, 'mcpguard_meta', f'SELECT md5(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text) FROM mcpguard_ra.{table} t;')
            for table in ('schema_versions', 'access_grant_heads', 'credential_packages',
                          'credential_leases', 'credential_contexts', 'read_plans', 'read_audit',
                          'recovery_fence', 'revocation_audit', 'revocation_cleanup')}


def verify_rollback(state, cfg):
    wire = wire_module(state)
    before = json.loads((state / 'ra17-baseline.json').read_text())
    v2_before = v2_digest(cfg)
    disabled = {
        'MCPGUARD_RESOURCE_CATALOG': 'disabled',
        'MCPGUARD_RESOURCE_ACCESS': 'enabled',
        'MCPGUARD_RESOURCE_CREDENTIALS': 'enabled',
        'MCPGUARD_RESOURCE_DOWNLOADS': 'enabled',
        'MCPGUARD_RESOURCE_CONTEXTS': 'enabled',
        'MCPGUARD_RESOURCE_READS': 'enabled',
        'MCPGUARD_RECOVERY_FENCE_DSN': 'stale-config-must-not-be-opened',
    }
    with gateway(state, cfg, wire, disabled) as port:
        client = wire.HTTPMCP(port, 'alice')
        check(sorted(t['name'] for t in client.call('tools/list')['tools']) == before['tools'], 'rollback restores the legacy tool catalog exactly')
        check(wire.http(port, '/api/v2/resources', cfg['alice_token'], method='GET')[0] == 404
              and wire.http(port, '/api/v2/credential-packages', cfg['alice_token'], body={})[0] == 404,
              'rollback disables resource routes and new package issuance even with stale child flags')
        history = client.call('tools/call', {'name': 'history.get_result', 'arguments': {'actionId': before['action']}})
        check(not history.get('isError', False) and history['structuredContent']['result'] is not None, 'rollback still decrypts the original legacy result')
        check(client.read()['structuredContent']['requestStatus'] == 'SUCCEEDED', 'legacy input still reads the isolated demo data')
        query(cfg, 'mcpguard_business', 'CREATE SCHEMA resource_demo;')
        blocked = client.read()['structuredContent']
        check(blocked['dispatchState'] == 'NOT_DISPATCHED' and blocked['requestStatus'] == 'FAILED',
              'a running legacy gateway refuses a database that becomes a protected target')
    env = base.dev.runtime_env(cfg) | disabled
    failure = invoke(state, 'mcpguard', [], env, expected=1)
    check('legacy PG startup check failed' in failure.stderr, 'legacy restart refuses the protected database after rollback')
    check(v2_digest(cfg) == v2_before and ledger(cfg, before['action']) == before['ledger'] and demo_digest(cfg) == before['demo'],
          'rollback preserves v2 state, legacy audit and ledger, and demo rows')


def verify_restore_check(state, cfg):
    env = base.dev.runtime_env(cfg) | {'MCPGUARD_MIGRATION_DSN': cfg['MCPGUARD_MIGRATION_DSN'],
                                     'MCPGUARD_PROVIDER_CONFIG': str(state / 'provider-private.json')}
    org = base.dev.ORG
    args = ['restore-check', org]
    check(not json.loads(invoke(state, 'mcpguard-catalog', args, env).stdout)['suspect'],
          'restore check verifies a matching recovery point')
    query(cfg['raPgAdmins'][0], 'mcpguard_business', f"SELECT mcpguard_provision.advance_fence('{org}');")
    before = v2_digest(cfg)
    rejected = invoke(state, 'mcpguard-catalog', args, env, expected=1)
    check(json.loads(rejected.stdout)['suspect'] and v2_digest(cfg) == before,
          'restore check flags an older metadata generation without changing state')
    accepted = invoke(state, 'mcpguard-catalog', ['restore-accept', org, base.dev.IDS['visitor']], env)
    check(not json.loads(accepted.stdout)['suspect'], 'explicit operator acceptance records the verified recovery point')


def configure_fixture(regression):
    STATE_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    base.BUILD = BUILD
    base.POINTER = ROOT / ('.dev/ra17-regression-latest' if regression else '.dev/ra17-latest')
    base.Path = lambda *parts: STATE_ROOT if parts == ('/tmp',) else Path(*parts)

    def build():
        BUILD.mkdir(parents=True, exist_ok=True)
        container_runner(['go', 'build', '-p', '2', '-o', str(BUILD.relative_to(ROOT)), *('./cmd/' + n for n in BINARIES)])

    def runner(args, state=None, env=None, **kwargs):
        if state and env:
            cfg = json.loads((state / 'env.json').read_text())
            env = env | {
                'MCPGUARD_TEST_CATALOG_BINARY': '/work/' + (BUILD / 'mcpguard-catalog').relative_to(ROOT).as_posix(),
                'MCPGUARD_RA_PACKAGE_KEYS': '/fixture/package-keys.json',
                'MCPGUARD_RA_PROVISION_CONFIG': '/fixture/provider-private.json',
                'MCPGUARD_TEST_PROVISIONER_DSN': cfg['provisionerDsn'],
                'MCPGUARD_TEST_CREDENTIAL_DSN': cfg['credentialDsn'],
                'MCPGUARD_TEST_DELIVERY_DSN': cfg['deliveryDsn'],
                'MCPGUARD_TEST_CONTEXT_DSN': cfg['contextDsn'],
            }
        return container_runner(args, state, env, **kwargs)

    base.build, base.runner = build, runner


def main(regression):
    configure_fixture(regression)
    if regression:
        read.prepare()
        read.test()
        return

    base.prepare = functools.partial(base.prepare, before_catalog=before_catalog)
    read.prepare()
    state, cfg = base.selected()
    before = json.loads((state / 'ra17-baseline.json').read_text())
    env = base.dev.runtime_env(cfg) | {'MCPGUARD_MIGRATION_DSN': cfg['MCPGUARD_MIGRATION_DSN']}
    plan = json.loads(invoke(state, 'mcpguard-catalog', ['migrate', '--dry-run'], env).stdout)
    check(plan['currentVersion'] == 13 and plan['pendingVersions'] == [], 'incremental catalog migration reaches v13 and reruns cleanly')
    check(ledger(cfg, before['action']) == before['ledger'] and demo_digest(cfg) == before['demo'], 'catalog migration preserves seeded legacy data and ledger byte for byte')
    provider_upgrade(state, cfg)

    wire_env = {f'MCPGUARD_TEST_{kind}_BINARY': str(BUILD / name) for kind, name in (
        ('PROVISION', 'mcpguard-provision'), ('CREDENTIAL', 'mcpguard-credentials'), ('SESSION', 'mcpguard-session'))}
    wire_env.update(MCPGUARD_TEST_STATE=str(state), MCPGUARD_TEST_BINARY=str(BUILD / 'mcpguard'), MCPGUARD_TEST_READ_ROLLOUT='1')
    result = subprocess.run([sys.executable, '-B', 'tests/context_e2e.py'], cwd=ROOT,
                            env=base.dev.runtime_env(cfg) | wire_env, capture_output=True, text=True, timeout=150)
    base.dev.write_private(state / 'ra17-read-rollout.log', result.stdout + result.stderr)
    check(result.returncode == 0, 'protected read-only rollout through the gateway and both PostgreSQL resources')
    verify_rollback(state, cfg)
    verify_restore_check(state, cfg)
    read_checks = json.loads((state / 'context-e2e-results.json').read_text())['passed']
    base.dev.write_private(state / 'ra17-results.json', json.dumps({'checks': CHECKS, 'readChecks': read_checks}, indent=2))
    print(json.dumps({'runId': cfg['runId'], 'exitCode': 0, 'migrationAndRollbackChecks': len(CHECKS),
                      'readRolloutChecks': read_checks, 'privateEvidence': str(state / 'ra17-results.json')}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--regression', action='store_true', help='run the previous Go race suite on a separate fixture')
    try:
        main(parser.parse_args().regression)
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        raise SystemExit(f'RA-17 isolated subprocess failed ({type(error).__name__}); inspect the private fixture state.')
