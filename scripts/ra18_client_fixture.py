#!/usr/bin/env python3
"""Run the installed Codex app-server against fresh RA-18 loopback fixtures."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import queue
import shlex
import subprocess
import sys
import threading
import time
from urllib.parse import urlsplit, urlunsplit
from uuid import UUID, uuid4

import migration_fixture as fixture


ROOT = fixture.ROOT
BUILD = ROOT / '.dev/ra18-build'
ITEM = 'item_demo_1'
SELF_SERVICE = '/console/api/v2/self-service/'
CONTEXT_HEADER = 'X-MCPGuard-Credential-Context'
HOST_LAUNCHER = (
    'import json,os,sys; '
    'token=json.load(open(sys.argv[1]))["alice_token"]; '
    'os.execve(sys.argv[2],sys.argv[2:],dict(os.environ,MCPGUARD_TOKEN=token))'
)


def configure(entry):
    fixture.STATE_ROOT = Path.home() / '.local/state/mcpguard-ra18'
    fixture.BUILD = BUILD
    fixture.BINARIES += ('mcpguard-http-helper',)
    fixture.configure_fixture(False)
    fixture.base.POINTER = ROOT / f'.dev/ra18-{entry}-latest'


def prepare():
    os.environ['MCPGUARD_FIXTURE_WRITE'] = '1'
    fixture.read.prepare()
    state, cfg = fixture.base.selected()
    env = os.environ | {'MCPGUARD_MIGRATION_DSN': cfg['MCPGUARD_MIGRATION_DSN']}
    for role in ('mcpguard_app', 'mcpguard_contexts'):
        fixture.invoke(state, 'mcpguard-catalog', ['grant-write-runtime', role], env)


class Codex:
    """Drive the product's own MCP connection and tool UI RPCs."""

    def __init__(self, executable, server_config, env, log):
        self.override = 'mcp_servers={mcpguard_ra18={' + server_config + '}}'
        self.process = subprocess.Popen(
            [executable, '-c', self.override, 'app-server', '--stdio'], cwd=ROOT,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, env=env,
            text=True, encoding='utf-8', bufsize=1,
        )
        self.sequence = 0
        self.messages = queue.Queue()
        threading.Thread(target=self.collect, daemon=True).start()

    def collect(self):
        for line in self.process.stdout:
            try:
                message = json.loads(line)
            except ValueError:
                continue
            if 'id' in message:
                self.messages.put(message)
        self.messages.put(None)

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            self.process.wait(timeout=8)

    def send(self, method, params=None):
        self.sequence += 1
        self.process.stdin.write(json.dumps({'id': self.sequence, 'method': method, 'params': params or {}}) + '\n')
        self.process.stdin.flush()
        return self.sequence

    def receive(self, request_id, timeout=60):
        message = self.messages.get(timeout=timeout)
        if not message or message.get('id') != request_id or 'result' not in message:
            raise AssertionError(f'Codex RPC {request_id} did not complete normally')
        return message['result']

    def rpc(self, method, params=None):
        return self.receive(self.send(method, params))

    def start(self):
        result = self.rpc('initialize', {'clientInfo': {'name': 'mcpguard-ra18-verifier', 'version': '1'},
                                         'capabilities': {'experimentalApi': True}})
        self.process.stdin.write('{"method":"initialized","params":{}}\n')
        self.process.stdin.flush()
        self.thread_id = self.rpc('thread/start', {'cwd': str(ROOT), 'ephemeral': True, 'approvalPolicy': 'never'})['thread']['id']
        return result

    def tools(self):
        result = self.rpc('mcpServerStatus/list', {'threadId': self.thread_id, 'serverName': 'mcpguard_ra18',
                                                   'detail': 'toolsAndAuthOnly'})
        return {tool['name'] for server in result['data'] if server['name'] == 'mcpguard_ra18'
                for tool in server['tools'].values()}

    def call(self, tool, args, wait=True):
        ident = self.send('mcpServer/tool/call', {'threadId': self.thread_id, 'server': 'mcpguard_ra18',
                                                'tool': tool, 'arguments': args})
        return self.receive(ident) if wait else ident


class Scenario:
    def __init__(self, state, cfg, entry, executable):
        self.state, self.cfg, self.entry, self.executable = state, cfg, entry, executable
        self.wire = fixture.wire_module(state)
        self.dev = self.wire.dev
        self.port = self.dev.free_port()
        self.origin = f'http://127.0.0.1:{self.port}'
        self.endpoint = self.origin + '/mcp'
        self.provider = state / 'provider-private.json'
        self.org = self.dev.ORG
        snapshot = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        self.report = {'fixture': cfg['runId'], 'entry': entry, 'orgId': self.org,
                       'sourceSnapshot': snapshot, 'postgresImage': self.dev.IMAGE,
                       'principalId': self.dev.IDS['alice'], 'reviewerId': self.dev.IDS['visitor'],
                       'checks': [], 'access': [], 'mcpProtocol': self.wire.PROFILE,
                       'clientSurface': 'Codex app-server mcpServer/tool/call', 'operatorSurface': 'console HTTP session',
                       'acceptanceLayer': 'automated native client', 'modelTurnStarted': False}
        self.env = {k: v for k, v in self.dev.runtime_env(cfg).items() if not k.startswith('MCPGUARD_TEST_')}
        self.env.pop('MCPGUARD_NATIVE_CONFIG', None)
        self.env |= {
            'MCPGUARD_LISTEN': f'127.0.0.1:{self.port}', 'MCPGUARD_LEGACY_DEMO': 'disabled',
            'MCPGUARD_RESOURCE_CATALOG': 'enabled', 'MCPGUARD_RESOURCE_ACCESS': 'enabled',
            'MCPGUARD_RESOURCE_CREDENTIALS': 'enabled', 'MCPGUARD_CREDENTIAL_KEYS': str(state / 'package-keys.json'),
            'MCPGUARD_RESOURCE_DOWNLOADS': 'enabled', 'MCPGUARD_DELIVERY_DSN': cfg['deliveryDsn'],
            'MCPGUARD_DOWNLOAD_TTL_SECONDS': '60', 'MCPGUARD_DOWNLOAD_MAX_READY': '10',
            'MCPGUARD_RESOURCE_CONTEXTS': 'enabled', 'MCPGUARD_CONTEXT_DSN': cfg['contextDsn'],
            'MCPGUARD_CONTEXT_TTL_SECONDS': '20', 'MCPGUARD_CONTEXT_LIFETIME_SECONDS': '600',
            'MCPGUARD_CONTEXT_MAX_ACTIVATIONS': '10', 'MCPGUARD_SYNC_PAGE_LIMIT': '20',
            'MCPGUARD_RESOURCE_READS': 'enabled', 'MCPGUARD_RESOURCE_WRITES': 'enabled',
            'MCPGUARD_READ_SECRETS': str(state / 'ra-secrets.json'), 'MCPGUARD_PROVIDER_CONFIG': str(self.provider),
            'MCPGUARD_RECOVERY_FENCE_DSN': cfg['MCPGUARD_MIGRATION_DSN'],
            'MCPGUARD_CONSOLE_REVOCATION_DSN': cfg['MCPGUARD_MIGRATION_DSN'],
        }
        self.client_env = {k: v for k, v in os.environ.items() if not k.startswith('MCPGUARD_')}
        self.secrets = [cfg[name + '_token'] for name in ('alice', 'bob', 'visitor')]
        self.visible_results = []
        self.auth = state / f'ra18-host-{uuid4()}.json'
        self.profile = state / f'ra18-cache-{uuid4()}'
        self.tool_args = ['--gateway', self.endpoint, '--allow-loopback-http']

    def check(self, value, name):
        if not value:
            raise AssertionError(name)
        self.report['checks'].append(name)

    def invoke(self, name, args, env=None, expected=0):
        return fixture.invoke(self.state, name, args, env or self.env, expected)

    def query(self, sql, pg=None, database=None):
        default = 'mcpguard_meta' if pg is None else 'mcpguard_business'
        return fixture.query(self.cfg if pg is None else self.cfg['raPgAdmins'][pg], database or default, sql)

    @contextmanager
    def gateway(self, restored=None, *, startup_timeout=12):
        env = self.env.copy()
        if restored:
            for name in ('MCPGUARD_METADATA_DSN', 'MCPGUARD_DELIVERY_DSN', 'MCPGUARD_CONTEXT_DSN',
                         'MCPGUARD_RECOVERY_FENCE_DSN', 'MCPGUARD_CONSOLE_REVOCATION_DSN'):
                parts = urlsplit(env[name])
                env[name] = urlunsplit(parts._replace(path='/' + restored))
        with (self.state / 'ra18-gateway.log').open('a') as log:
            process = subprocess.Popen([str(BUILD / 'mcpguard')], env=env, stdout=log, stderr=log)
            try:
                def ready():
                    if process.poll() is not None:
                        raise AssertionError('RA-18 gateway exited during startup')
                    try:
                        return self.wire.http(self.port, '/healthz', method='GET')[0] == 200
                    except OSError:
                        return False
                self.wire.until(ready, startup_timeout)
                yield
            finally:
                process.terminate()
                process.wait(timeout=12)

    def enroll(self):
        self.invoke('mcpguard-credentials', ['enroll-client', '1800', str(self.auth)],
                    self.env | {'MCPGUARD_METADATA_DSN': self.cfg['credentialDsn'],
                                'MCPGUARD_TOKEN': self.cfg['alice_token']})
        self.secrets.append(json.loads(self.auth.read_text())['deliveryToken'])

    def cached_proof(self):
        data = json.loads((self.profile / 'session.json').read_text())
        context = data['context']
        if context['contextToken'] not in self.secrets:
            self.secrets.append(context['contextToken'])
        return context['context'], context['context']['contextId'] + '.' + context['contextToken']

    def cache(self):
        return self.cached_proof()[0]

    def package(self, cred):
        return self.profile / 'files' / f'mcpguard-{cred}.mgcred'

    def worker(self, expected=0):
        args = ['worker', *self.tool_args, '--auth-file', str(self.auth), '--directory', str(self.profile), '--once']
        self.invoke('mcpguard-http-helper', args, self.client_env | {'MCPGUARD_TOKEN': self.cfg['alice_token']}, expected)

    @contextmanager
    def codex(self, url_only=False, cached_header=None):
        string = json.dumps
        user = 'alice' if not isinstance(url_only, str) else url_only
        env = self.client_env | {'MCPGUARD_TOKEN': self.cfg[user + '_token']}
        if url_only:
            config = f'url={string(self.endpoint)},bearer_token_env_var="MCPGUARD_TOKEN"'
            if cached_header:
                env['RA18_TEST_CACHED_CONTEXT'] = cached_header
                config += f',env_http_headers={{{string(CONTEXT_HEADER)}="RA18_TEST_CACHED_CONTEXT"}}'
        elif self.entry == 'http':
            # The host helper starts with a filtered environment. Load the
            # fixture identity only inside this trusted child process.
            helper = [sys.executable, '-c', HOST_LAUNCHER, str(self.state / 'env.json'),
                      str(BUILD / 'mcpguard-http-helper'), 'headers', *self.tool_args,
                      '--auth-file', str(self.auth), '--directory', str(self.profile)]
            config = f'url={string(self.endpoint)},http_headers_helper={string(shlex.join(helper))}'
        else:
            args = [*self.tool_args, '--private-auth-file', str(self.auth), '--private-directory', str(self.profile)]
            config = (f'command={string(str(BUILD / "mcpguard-connect"))},args={string(args)},'
                      'env_vars=["MCPGUARD_TOKEN"]')
        config += ',startup_timeout_sec=20,tool_timeout_sec=12'
        with (self.state / 'ra18-codex.log').open('a') as log:
            client = Codex(self.executable, config, env, log)
            try:
                client.start()
                yield client
            finally:
                client.close()

    def tool(self, client, name, args, error=None):
        frame = client.call(name, args)
        self.visible_results.append(frame)
        data = frame.get('structuredContent')
        if data is None:
            content = next((x['text'] for x in frame['content'] if x['type'] == 'text'), None)
            try:
                data = json.loads(content) if content else {}
            except ValueError:
                data = {}
        if error is not None:
            code = (data.get('error') or {}).get('code', 'none')
            if bool(frame.get('isError')) != error:
                raise AssertionError(f'{name} returned unexpected tool status (code={code})')
            self.check(True, f'{name} has expected tool error={error}')
        return data

    def login(self, user):
        status, headers, body = self.wire.http(self.port, '/console/login',
                                              body={'username': user, 'password': self.cfg['console_secrets'][user]},
                                              headers={'Origin': self.origin})
        self.check(status == 200, f'{user} has an independent console session')
        cookie = next(v for k, v in headers.items() if k.lower() == 'set-cookie').split(';', 1)[0]
        return {'Cookie': cookie, 'Origin': self.origin, 'X-CSRF-Token': json.loads(body)['csrf']}

    def operator(self, path, headers, body, expected=200):
        status, _, reply = self.wire.http(self.port, path, body=body, headers=headers)
        self.check(status == expected, f'console {path.split("/")[-1]} returned HTTP {expected}')
        return json.loads(reply)

    def provision(self, client, reviewer, index, write=False):
        resource = f'40000000-0000-4000-8000-{index:012d}'
        profile_index = index + 10 if write else index
        request = {'resourceId': resource, 'profileId': f'50000000-0000-4000-8000-{profile_index:012d}',
                   'requestedExpiry': (datetime.now(timezone.utc) + timedelta(minutes=25)).isoformat().replace('+00:00', 'Z'),
                   'reason': 'RA-18 isolated coding client', 'requestKey': str(uuid4())}
        received = self.tool(client, 'access.request', request, False)
        req_id = str(UUID(received['requestId']))
        decision = {'requestHash': received['requestHash'], 'expectedRevision': received['revision'], 'decision': 'APPROVE',
                    'approvedExpiry': received['requestedExpiry'], 'reason': 'RA-18 fixture reviewer', 'requestKey': str(uuid4())}
        result = self.operator(f'/console/api/v2/access-requests/{req_id}/decisions',
                               reviewer | {'If-Match': '"' + received['revision'] + '"', 'Idempotency-Key': decision['requestKey']}, decision)
        grant = str(UUID(result['grant']['grantId']))
        blocked = self.tool(client, 'credential.prepare_download', {'grantId': grant, 'requestKey': str(uuid4())}, True)
        self.check(result['grant']['state'] == 'PROVISIONING' and bool(blocked), 'PROVISIONING cannot issue a package')
        provision_env = self.env | {'MCPGUARD_METADATA_DSN': self.cfg['provisionerDsn']}
        self.invoke('mcpguard-provision', ['run', self.org, grant], provision_env)
        sql = f"""
          SELECT json_build_object('accountId',a.account_id,'identity',a.native_identity,'nativeVerified',j.state,
                    'jobId', j.job_id,'roleOid',o.role_oid,'grantState',h.state,'grantVersion',h.version::text)::text
          FROM mcpguard_ra.provisioning_jobs j JOIN mcpguard_ra.account_claims a USING(org_id,account_id)
          JOIN mcpguard_ra.native_observations o USING(org_id,job_id,account_id)
          JOIN mcpguard_ra.access_grant_heads h USING(org_id,grant_id)
          WHERE j.grant_id='{grant}'
        """
        verified = json.loads(self.query(sql))
        self.check(verified['nativeVerified'] == 'VERIFIED' and verified['grantState'] == 'ACTIVE', 'native account verified before ACTIVE')
        identity = 'mg_' + UUID(verified['accountId']).hex
        self.check(verified['identity'] == identity, 'account is specific to this granted resource')
        self.query(f"INSERT INTO resource_demo.items VALUES('{identity}','{ITEM}','RA-18 isolated item',1)", pg=index - 1)
        prepared = self.tool(client, 'credential.prepare_download', {'grantId': grant, 'requestKey': str(uuid4())}, False)
        if self.entry == 'http':
            self.check(not self.package(prepared['credentialId']).exists(), 'native HTTP tool response does not deliver private bytes')
            self.worker()
        cred = str(UUID(prepared['credentialId']))
        package = json.loads(self.package(cred).read_text())
        for k in ('nonce', 'ciphertext', 'authenticationTag'):
            self.secrets.append(package[k])
        ctx = self.cache()['contextId']
        actual = self.query(f"SELECT state FROM mcpguard_ra.context_activation_versions WHERE context_id='{ctx}' AND credential_id='{cred}' ORDER BY revision DESC LIMIT 1")
        self.check(actual == 'ACTIVE', 'the real client delivered and activated a private package')
        evidence = verified | {'resourceId': resource, 'resourceVersion': '1', 'requestId': req_id,
                               'decisionId': result['decision']['decisionId'], 'grantId': grant,
                               'credentialId': cred, 'leaseId': prepared['leaseId'], 'downloadId': prepared['downloadId'],
                               'contextId': ctx, 'profile': 'write' if write else 'read'}
        self.report['access'].append(evidence)
        return evidence

    def read(self, client, access, error=False):
        frame = self.tool(client, 'pg.get_item', {'resourceId': access['resourceId'], 'credentialId': access['credentialId'],
                                                 'itemId': ITEM}, error)
        if not error:
            self.check(frame['requestStatus'] == 'SUCCEEDED' and frame['data']['nativeIdentity'] == access['identity'],
                       'real Codex tool read uses the expected PostgreSQL identity')
            access.setdefault('reads', []).append(frame['data']['planId'])
        return frame

    def write_plan(self, client, access, version):
        planned = self.tool(client, 'pg.update_note', {'resourceId': access['resourceId'], 'credentialId': access['credentialId'],
                                                       'itemId': ITEM, 'note': 'RA-18 approved item', 'expectedVersion': str(version)}, False)
        self.check(planned['kind'] == 'DEFERRED' and planned['dispatchState'] == 'NOT_DISPATCHED', 'resource approval does not dispatch writes')
        return str(UUID(planned['actionId']))

    def approve(self, actor, action_id, expected=200):
        return self.operator(SELF_SERVICE + f'write-requests/{action_id}/decisions', actor,
                             {'approve': True, 'reason': 'RA-18 scoped write decision'}, expected)

    def version(self, access):
        return self.query(f"SELECT version FROM resource_demo.items WHERE owner_name='{access['identity']}' AND item_id='{ITEM}'", pg=0)

    @contextmanager
    def native_session(self, user, sql):
        with (self.state / 'ra18-native-session.log').open('a') as log:
            process = subprocess.Popen(['docker', 'exec', '-i', self.cfg['raPgAdmins'][0]['container'], 'psql',
                                        '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-U', user, '-d', 'mcpguard_business'],
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, text=True)
            try:
                process.stdin.write(sql + '\nSELECT pg_backend_pid();\n')
                process.stdin.flush()
                while True:
                    line = process.stdout.readline().strip()
                    if line.isdigit():
                        break
                    if not line:
                        raise AssertionError('fixture PostgreSQL session did not establish')
                yield int(line)
            finally:
                process.stdin.close()
                process.wait(timeout=8)

    def cleanup(self, access, want_status, want_receipt=None):
        admin = self.env | {'MCPGUARD_MIGRATION_DSN': self.cfg['MCPGUARD_MIGRATION_DSN'],
                            'MCPGUARD_METADATA_DSN': self.cfg['MCPGUARD_MIGRATION_DSN']}
        args = ['revocation-cleanup', 'retry', self.org, str(self.provider)]
        retry = json.loads(self.invoke('mcpguard-catalog', args, admin).stdout)
        recorded = json.loads(self.invoke('mcpguard-catalog', ['revocation-cleanup', 'status', self.org], admin).stdout)
        row = next(row for row in recorded['rows'] if row['accountId'] == access['accountId'])
        receipt = row.get('nativeReceipt', {})
        self.check(row['state'] == want_status and receipt.get('status') == want_receipt,
                   f'cleanup ledger records {want_status} with {want_receipt or "no provider receipt"}')
        self.check(retry['cleaned'] == (want_status == 'CLEANED') and retry['failed'] == (want_status == 'FAILED'),
                   'retry counts agree with the persisted attempt')
        self.report.setdefault('cleanup', []).append(row)
        return receipt

    def in_flight(self, client, reviewer, write, second):
        queued = self.write_plan(client, write, 2)
        self.approve(reviewer, queued)
        action = self.write_plan(client, write, 2)
        self.approve(reviewer, action)
        args = {'actionId': action}
        identity = write['identity']
        find_pid = ("SELECT pid FROM pg_stat_activity WHERE state='active' AND wait_event_type='Lock' "
                    f"AND usename='{identity}' AND query LIKE 'UPDATE resource_demo.items %'")
        lock_row = f"BEGIN; DO $$ BEGIN PERFORM 1 FROM resource_demo.items WHERE owner_name='{identity}' FOR UPDATE; END $$;"
        with self.native_session('postgres', lock_row) as locker:
            request = client.call('action.resume', args, wait=False)
            started = time.monotonic()
            pid = ''
            while not pid and time.monotonic() - started < 2:
                pid = self.query(find_pid, pg=0)
            self.check(pid.isdigit(), 'PostgreSQL observed a real UPDATE waiting on the held row')
            self.check(self.query(f"SELECT {locker}=ANY(pg_blocking_pids({int(pid)}))", pg=0) == 't',
                       'the observed native PID is blocked by the independent fixture transaction')
            revoke = self.operator(SELF_SERVICE + 'revocations', reviewer,
                                   {'kind': 'GRANT', 'targetId': write['grantId'], 'reason': 'RA-18 in-flight exercise'})['revocation']
            self.check(revoke['writeUnknown'] == 1 and revoke['cleanupPending'] == 1, 'revocation commits UNKNOWN and enqueues the correct account')
            result = client.receive(request)
            self.visible_results.append(result)
            control = result['structuredContent']
            self.check(control['action']['state'] == 'OUTCOME_UNKNOWN' and control['result'] is None,
                       'Codex receives unknown outcome with no replay')
            # PostgreSQL's own cancellation message names the observed backend
            # PID. Only a redacted status is published, never the SQL log line.
            container = self.cfg['raPgAdmins'][0]['container']
            log = self.dev.docker('logs', '--tail', '160', container, capture_output=True, text=True)
            cancelled = any(f'[{pid}]' in line and 'canceling statement due to user request' in line
                            for line in (log.stdout + log.stderr).splitlines())
            self.check(cancelled and not self.query(find_pid, pg=0) and self.version(write) == '2',
                       'native cancellation was confirmed while the blocker still held the row')
        repeat = self.tool(client, 'action.resume', args, False)
        self.check(repeat['outcome'] == 'NOT_RESUMABLE' and self.version(write) == '2', 'unknown write reserves a single dispatch and never replays')
        self.tool(client, 'action.resume', {'actionId': queued}, True)
        self.check(self.query(f"SELECT count(*) FROM mcpguard_ra.write_audit WHERE request_id='{queued}' AND event_type='DISPATCH_INTENT'") == '0',
                   'an approved action which had not claimed dispatch is refused after revocation')
        audit = self.query(f"SELECT string_agg(event_type,',' ORDER BY seq) FROM mcpguard_ra.write_audit WHERE request_id='{action}'")
        self.check(audit == 'PLANNED,APPROVED,DISPATCH_INTENT,OUTCOME_UNKNOWN', 'unknown write has a durable single-intent audit')
        self.tool(client, 'history.get_result', {'actionId': write['completedAction']}, True)
        self.read(client, second)
        self.report['inFlight'] = {'actionId': action, 'nativePid': int(pid), 'accountId': write['accountId'],
                                   'roleOid': write['roleOid'], 'pgResult': 'QUERY_CANCELED_BY_REQUEST',
                                   'rowVersionBefore': '2', 'rowVersionAfter': '2', 'ledger': audit.split(','),
                                   'actionState': control['action']['state'], 'revocationEpoch': revoke['epoch'],
                                   'approvedActionDeniedBeforeDispatch': queued}

    def expiry_and_file_recovery(self, client, access):
        path = self.package(access['credentialId'])
        original = path.read_bytes()
        path.unlink()
        if self.entry == 'http':
            self.worker()
            self.read(client, access)
        else:
            self.read(client, access)
        self.check(path.read_bytes() == original, 'missing encrypted file is recovered with exactly the original bytes')
        original_context = self.cache()
        ctx = original_context['contextId']
        if self.entry == 'http':
            self.worker()
        self.read(client, access)
        renewed = self.cache()
        self.check(renewed['contextId'] == ctx and renewed['expiresAt'] > original_context['expiresAt'],
                   'same Codex connection keeps working after private pre-expiry renewal')
        self.wire.until(lambda: self.query(f"SELECT expires_at<=clock_timestamp() FROM mcpguard_ra.credential_contexts WHERE context_id='{ctx}'") == 't', 30)
        if self.entry == 'http':
            denied = self.read(client, access, error=True)
            self.check(denied['dispatchState'] == 'NOT_DISPATCHED', 'cached HTTP context is refused after natural expiry')
            self.worker(expected=1)
            with self.codex() as fresh:
                self.read(fresh, access)
        else:
            self.read(client, access)
        rebuilt = self.cache()['contextId']
        self.check(rebuilt != ctx and path.read_bytes() == original, 'renewed Codex connection validates retained package in a new context')
        self.report['contextRecovery'] = {'before': ctx, 'after': rebuilt, 'naturalExpiry': True,
                                          'codexReconnectRequired': self.entry == 'http'}

    def take_backup(self):
        saved = self.dev.docker('exec', self.cfg['container'], 'pg_dump', '-U', 'postgres', '-d', 'mcpguard_meta', '-Fc', capture_output=True).stdout
        self.check(saved.startswith(b'PGDMP'), 'captured a real PostgreSQL metadata dump before revocation')
        return saved

    def restore(self, dump, access, saved_proof, revoked):
        database = 'ra18_restore_' + uuid4().hex[:10]
        self.query(f'CREATE DATABASE {database}', database='postgres')
        self.dev.docker('exec', '-i', self.cfg['container'], 'pg_restore', '-U', 'postgres', '-d', database,
                        '--exit-on-error', input=dump, capture_output=True)
        parts = urlsplit(self.cfg['MCPGUARD_MIGRATION_DSN'])
        admin = urlunsplit(parts._replace(path='/' + database))
        summary = self.invoke('mcpguard-catalog', ['restore-check', self.org],
                              self.env | {'MCPGUARD_MIGRATION_DSN': admin}, expected=1)
        fence = json.loads(summary.stdout)
        self.check(fence['suspect'] and fence['native'] > fence['expected'], 'real restored metadata is behind its native recovery fence')
        with self.gateway(database):
            # The read target's native role still works. Its denial here proves
            # the fence rather than an earlier account cleanup.
            ctx, header = saved_proof
            now = self.query(f"SELECT state='ACTIVE' AND expires_at>clock_timestamp() FROM mcpguard_ra.credential_contexts WHERE context_id='{ctx['contextId']}'", database=database)
            self.check(now == 't', 'restored context is still unexpired before checking the fence')
            with self.codex(url_only='alice', cached_header=header) as client:
                denied = self.read(client, access, error=True)
                self.check(denied['dispatchState'] == 'NOT_DISPATCHED', 'restored metadata cannot dispatch a previously valid package')
                self.tool(client, 'credential.prepare_download', {'grantId': access['grantId'], 'requestKey': str(uuid4())}, True)
            request = {'resourceId': access['resourceId'], 'credentialId': access['credentialId'], 'requestKey': str(uuid4()),
                       'credentialPackage': json.loads(self.package(access['credentialId']).read_text())}
            status, _, _ = self.wire.http(self.port, f"/api/v2/credential-contexts/{ctx['contextId']}/activations",
                                          self.cfg['alice_token'], request, {CONTEXT_HEADER: header})
            self.check(status == 403, 'restored metadata cannot activate the earlier encrypted file')
            frozen = self.query(f"SELECT state FROM mcpguard_ra.credential_leases WHERE credential_id='{access['credentialId']}'", database=database)
            self.check(frozen == 'ACTIVE', 'recovery protection blocks access without rewriting an unaffected recorded lease')
            disabled = self.query(f"SELECT NOT rolcanlogin FROM pg_roles WHERE oid={revoked['roleOid']} AND rolname='{revoked['identity']}'", pg=0)
            self.check(disabled == 't' and self.version(revoked) == '2', 'native account and row still match the original revocation evidence')
            reviewer = self.login('visitor')
            applied = self.operator(SELF_SERVICE + 'revocations', reviewer,
                                    {'kind': 'GRANT', 'targetId': revoked['grantId'], 'reason': 'Reconcile the recorded fixture revocation'})['revocation']
            self.check(applied['grants'] == 1, 'operator reapplies the single recorded missing revocation')
            admin_env = self.env | {'MCPGUARD_MIGRATION_DSN': admin}
            pending = self.invoke('mcpguard-catalog', ['restore-check', self.org], admin_env, expected=1)
            self.check(json.loads(pending.stdout) == fence, 'ordinary revocation leaves the old recovery point fenced')
            with self.codex(url_only='alice', cached_header=header) as client:
                denied = self.read(client, access, error=True)
                self.check(denied['dispatchState'] == 'NOT_DISPATCHED', 'replayed revocation alone does not reopen dispatch')
            accepted = self.invoke('mcpguard-catalog', ['restore-accept', self.org, self.dev.IDS['visitor']], admin_env)
            confirmed = json.loads(accepted.stdout)
            self.check(not confirmed['suspect'] and confirmed['expected'] == fence['native'],
                       'administrative acceptance records the reconciled native generation')
            with self.codex() as client:
                self.read(client, access)
                self.tool(client, 'credential.prepare_download', {'grantId': revoked['grantId'], 'requestKey': str(uuid4())}, True)
        self.report['restoreFence'] = fence | {'source': 'pg_dump/pg_restore', 'readDenied': True, 'issueDenied': True,
                                               'activateDenied': True, 'reconciledGrantId': revoked['grantId'],
                                               'ordinaryRevocationStayedFenced': True, 'acceptedGeneration': confirmed['expected'],
                                               'unaffectedReadRecovered': True, 'revokedGrantStayedDenied': True}

    def run(self):
        self.report['clientVersion'] = subprocess.check_output([self.executable, '--version'], text=True).strip()
        self.enroll()
        with self.gateway():
            with self.codex() as client:
                self.check({'resource.list', 'access.request', 'credential.prepare_download', 'pg.get_item', 'pg.update_note',
                            'action.status', 'action.resume'} <= client.tools(), 'official Codex runtime discovers the protected tools')
                resources = self.tool(client, 'resource.list', {}, False)
                self.check({'orders', 'customers'} <= {r['alias'] for r in resources['resources']}, 'one Codex profile discovers both business endpoints')
                reviewer = self.login('visitor')
                first = self.provision(client, reviewer, 1)
                second = self.provision(client, reviewer, 2)
                self.read(client, first)
                self.read(client, second)
                for owner in ('alice', 'bob'):
                    with self.codex(url_only=owner) as bare:
                        denied = self.read(bare, first, error=True)
                        self.check(denied['dispatchState'] == 'NOT_DISPATCHED', f'{owner} cannot dispatch with a bare credential ID')
                with self.codex(url_only='bob', cached_header=self.cached_proof()[1]) as copy:
                    denied = self.read(copy, first, error=True)
                    self.check(denied['dispatchState'] == 'NOT_DISPATCHED', 'Bob cannot dispatch with Alice’s copied context header')
                revoke = self.operator(SELF_SERVICE + 'revocations', reviewer,
                                       {'kind': 'CREDENTIAL', 'targetId': first['credentialId'], 'reason': 'RA-18 one-file revocation'})['revocation']
                self.check(revoke['credentials'] == 1 and revoke['grants'] == 0 and revoke['cleanupPending'] == 0,
                           'revoking one file leaves its grant and native account outside cleanup')
                self.read(client, first, error=True)
                self.check(self.package(first['credentialId']).exists(), 'retaining the revoked encrypted file does not restore access')
                self.read(client, second)
                self.report['fileRevocation'] = {'credentialId': first['credentialId'], 'epoch': revoke['epoch'], 'cleanupPending': 0}
                self.expiry_and_file_recovery(client, second)
        self.env['MCPGUARD_CONTEXT_TTL_SECONDS'] = '120'
        with self.gateway():
            with self.codex() as client:
                reviewer = self.login('visitor')
                own_console = self.login('alice')
                write = self.provision(client, reviewer, 1, write=True)
                action = self.write_plan(client, write, 1)
                pending = self.tool(client, 'action.resume', {'actionId': action}, False)
                self.check(pending['outcome'] == 'APPROVAL_PENDING' and self.version(write) == '1', 'resume before independent approval cannot dispatch')
                self.approve(own_console, action, expected=403)
                self.approve(reviewer, action)
                self.check(self.version(write) == '1', 'operator approval does not run the write')
                resumed = self.tool(client, 'action.resume', {'actionId': action}, False)
                self.check(resumed['action']['state'] == 'SUCCEEDED' and resumed['result']['sideEffectStatus'] == 'APPLIED'
                           and self.version(write) == '2', 'explicit resume from real client applies one PostgreSQL update')
                write['completedAction'] = action
                history = self.tool(client, 'history.get_result', {'actionId': action}, False)
                self.check(history['result']['sideEffectStatus'] == 'APPLIED', 'the live context can read the stored result before revocation')
                repeat = self.tool(client, 'action.resume', {'actionId': action}, False)
                self.check(repeat['outcome'] == 'NOT_RESUMABLE' and self.version(write) == '2', 'resume never reapplies a completed write')
                saved_proof = self.cached_proof()
                backup = self.take_backup()
                with self.native_session(write['identity'], 'SELECT current_user;') as idle:
                    self.in_flight(client, reviewer, write, second)
                    try:
                        self.query('ALTER ROLE mcpguard_account_manager NOLOGIN', pg=0)
                        self.cleanup(write, 'FAILED')
                    finally:
                        self.query('ALTER ROLE mcpguard_account_manager LOGIN', pg=0)
                    receipt = self.cleanup(write, 'FAILED', 'SESSIONS_REMAIN')
                    self.check(receipt['activeSessions'] >= 1 and self.query(f"SELECT count(*) FROM pg_stat_activity WHERE pid={idle}", pg=0) == '1',
                               'NOLOGIN still leaves a real pre-existing session and cleanup remains unconfirmed')
        # Gateway shutdown drains its native pools. The separate retained
        # session has also disconnected, so the provider can now prove zero.
        receipt = self.cleanup(write, 'CLEANED', 'CLEANED')
        self.check(receipt['activeSessions'] == 0 and receipt['roleOid'] == str(write['roleOid']), 'retry proves the exact PostgreSQL account reached zero sessions')
        with self.gateway(), self.codex() as client:
            self.read(client, second)
            denied = self.tool(client, 'credential.prepare_download', {'grantId': write['grantId'], 'requestKey': str(uuid4())}, True)
            self.check(bool(denied) and self.package(write['credentialId']).exists(), 'restart does not restore the revoked grant even with the saved file')
        self.restore(backup, second, saved_proof, write)
        observed = json.dumps(self.visible_results) + (self.state / 'ra18-gateway.log').read_text() + (self.state / 'ra18-codex.log').read_text()
        self.check(all(value not in observed for value in self.secrets), 'tool results and process logs contain no fixture tokens or encrypted package fields')


def main():
    cli = argparse.ArgumentParser()
    cli.add_argument('entry', choices=('http', 'stdio'))
    cli.add_argument('--prepare', action='store_true')
    cli.add_argument('--codex')
    cli.add_argument('--report', type=Path)
    opts = cli.parse_args()
    configure(opts.entry)
    if opts.prepare:
        prepare()
    if not opts.codex:
        if opts.prepare:
            return
        cli.error('--codex is required to run a client')
    state, cfg = fixture.base.selected()
    scenario = Scenario(state, cfg, opts.entry, opts.codex)
    try:
        scenario.run()
    finally:
        fixture.base.dev.write_private(state / 'ra18-observed.json', json.dumps(scenario.report, indent=2) + '\n')
    scenario.report['passed'] = len(scenario.report['checks'])
    scenario.report['recordedAt'] = datetime.now(timezone.utc).isoformat()
    if opts.report:
        opts.report.write_text(json.dumps(scenario.report, indent=2) + '\n', encoding='utf-8')
    print(json.dumps({'entry': opts.entry, 'status': 'PASS', 'checks': scenario.report['passed']}))


if __name__ == '__main__':
    main()
