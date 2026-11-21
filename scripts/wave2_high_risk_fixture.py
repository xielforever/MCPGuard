#!/usr/bin/env python3
"""Run isolated Wave 2 high-risk writes through the installed DeepSeek Harness.

From the repository under WSL, run one fresh fixture per entry:
  python3 -B scripts/wave2_high_risk_fixture.py http --report PATH
  python3 -B scripts/wave2_high_risk_fixture.py stdio --report PATH
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from copy import deepcopy
from datetime import datetime, timezone
from hashlib import file_digest
import json
import os
from pathlib import Path
import subprocess
from tempfile import TemporaryFile
from threading import Thread
import time
from uuid import UUID

from dsh_client import DSHClient, EXECUTABLE, install_path, runtime_env, windows
from high_risk_notification_receiver import Receiver
import ra18_client_fixture as suite


ROOT = suite.ROOT
BUILD = Path(os.environ.get('MCPGUARD_WAVE2_BUILD_DIR', ROOT / '.dev/wave2-high-risk-build')).resolve()
NATIVE_FIXTURE = ROOT / 'scripts/fixtures/w2_high_risk_orders.sql'
RESOURCE = '40000000-0000-4000-8000-000000000001'
CODE_TTL_SECONDS = 180
CONTEXT_TTL_SECONDS = 600
OPERATIONS = (
    ('UPDATE_ORDER_AMOUNT', 'order_recalc_1', 1),
    ('CREATE_ORDERS_INDEX', 'orders_shipping_lookup_idx', 0),
    ('DELETE_CANCELLED_ORDER', 'order_cancelled_1', 1),
)
PRIVATE_RECEIPT_FIELDS = {
    'transport', 'harnessVersion', 'sdkVersion', 'actionId', 'requestId', 'outcome',
    'actionState', 'dispatchState', 'executionStatus', 'errorCode', 'rejected',
}


def startup_timeout(name, default):
    try:
        value = int(os.environ.get(name, default))
    except ValueError:
        value = 0
    if not 1 <= value <= 120:
        raise SystemExit(f'{name} must be a whole number of seconds from 1 to 120.')
    return value


def configure(entry):
    fixture = suite.fixture
    fixture.STATE_ROOT = Path.home() / '.local/state/mcpguard-wave2-high-risk'
    fixture.BUILD = suite.BUILD = BUILD
    if 'mcpguard-http-helper' not in fixture.BINARIES:
        fixture.BINARIES += ('mcpguard-http-helper',)
    fixture.configure_fixture(False)
    fixture.base.POINTER = ROOT / '.dev' / f'wave2-high-risk-{entry}-latest'


class Scenario(suite.Scenario):
    def __init__(self, state, config, entry, harness_install, receiver):
        super().__init__(state, config, entry, None)
        self.harness_install = harness_install
        self.receiver = receiver
        self.gateway_startup_seconds = startup_timeout('MCPGUARD_WAVE2_GATEWAY_STARTUP_SECONDS', 45)
        self.harness_startup_seconds = startup_timeout('MCPGUARD_WAVE2_HARNESS_STARTUP_SECONDS', 30)
        self.channel = 'WECOM' if entry == 'http' else 'FEISHU'
        origin = f'http://127.0.0.1:{receiver.server_port}'
        self.env.update({
            'MCPGUARD_CONTEXT_TTL_SECONDS': str(CONTEXT_TTL_SECONDS),
            'MCPGUARD_HIGH_RISK_WRITES': 'enabled',
            'MCPGUARD_HIGH_RISK_RESOURCE_ID': RESOURCE,
            'MCPGUARD_HIGH_RISK_REVIEWER_ID': self.report['reviewerId'],
            'MCPGUARD_HIGH_RISK_CHANNEL': self.channel,
            'MCPGUARD_HIGH_RISK_WECOM_URL': origin + '/wecom',
            'MCPGUARD_HIGH_RISK_FEISHU_URL': origin + '/feishu',
            'MCPGUARD_HIGH_RISK_CODE_TTL': f'{CODE_TTL_SECONDS}s',
        })
        self.report.update(clientSurface='DeepSeek Harness native MCP executor and private SDK handoff',
                           channel=self.channel, operations=[], codeTTLSeconds=CODE_TTL_SECONDS,
                           contextTTLSeconds=CONTEXT_TTL_SECONDS,
                           gatewayStartupTimeoutSeconds=self.gateway_startup_seconds,
                           harnessStartupTimeoutSeconds=self.harness_startup_seconds)

    @contextmanager
    def harness_client(self):
        if self.entry == 'http':
            result = self.invoke('mcpguard-http-helper', [
                'headers', *self.tool_args, '--auth-file', str(self.auth), '--directory', str(self.profile),
            ], self.client_env | {'MCPGUARD_TOKEN': self.cfg['alice_token']})
            config = {'transport': 'streamable-http', 'url': self.endpoint, 'headers': json.loads(result.stdout)}
        else:
            config = {'transport': 'stdio', 'command': r'C:\Windows\System32\wsl.exe', 'args': [
                '-d', 'Ubuntu-24.04', '--exec', '/usr/bin/python3', '-B', '-c', suite.HOST_LAUNCHER,
                str(self.state / 'env.json'), str(BUILD / 'mcpguard-connect'), *self.tool_args,
                '--private-auth-file', str(self.auth), '--private-directory', str(self.profile),
            ]}
        self.transport = config
        registration = dict(config, startupTimeoutMs=self.harness_startup_seconds * 1000)
        with (self.state / 'wave2-harness.private.log').open('a', encoding='utf-8') as log, \
             DSHClient(registration, log, install=self.harness_install) as client:
            names = client.receive('ready', timeout=self.harness_startup_seconds + 12)['names']
            self.check(any('pg_plan_high_risk' in name for name in names), 'Harness discovered pg.plan_high_risk')
            yield client

    def install_native(self, access):
        identity = 'mg_' + UUID(access['accountId']).hex
        self.check(identity == access['identity'], 'native fixture uses the provisioned write identity')
        self.query(NATIVE_FIXTURE.read_text(encoding='utf-8'), pg=0)
        self.query(f'''GRANT USAGE ON SCHEMA mcpguard_w2_fixture TO {identity};
          GRANT EXECUTE ON FUNCTION mcpguard_w2_fixture.update_order_amount(text,bigint),
            mcpguard_w2_fixture.create_orders_index(text),
            mcpguard_w2_fixture.delete_cancelled_order(text,bigint) TO {identity};''', pg=0)

    def native_snapshot(self):
        return json.loads(self.query('''SELECT jsonb_build_object(
          'orders',(SELECT jsonb_agg(to_jsonb(o) ORDER BY tenant_id,order_id) FROM mcpguard_w2_fixture.orders o),
          'index',(SELECT jsonb_build_object('definition',pg_get_indexdef(i.indexrelid),'valid',i.indisvalid AND i.indisready)
            FROM pg_index i WHERE i.indexrelid=to_regclass('mcpguard_w2_fixture.orders_shipping_lookup_idx')
              AND i.indrelid=to_regclass('mcpguard_w2_fixture.orders')))''', pg=0))

    def plan(self, client, access, operation, target, version):
        plan = self.tool(client, 'pg.plan_high_risk', {
            'resourceId': access['resourceId'], 'credentialId': access['credentialId'],
            'operation': operation, 'target': target, 'expectedVersion': version,
        }, False)
        self.check(plan['kind'] == 'DEFERRED' and plan['state'] == 'PENDING_APPROVAL' and
                   plan['dispatchState'] == 'NOT_DISPATCHED', f'{operation} produces an undispatched plan')
        return str(UUID(plan['actionId']))

    def get(self, action, name, headers, expected=200):
        path = suite.SELF_SERVICE + f'write-requests/{action}/{name}'
        status, response_headers, body = self.wire.http(self.port, path, method='GET', headers=headers)
        if expected is not None:
            self.check(status == expected, f'{name} endpoint returned {expected}')
        return status, {key.lower(): value for key, value in response_headers.items()}, json.loads(body)

    def notice(self, action, event, actor, recipient):
        receipt = None

        def acknowledged():
            nonlocal receipt
            status, _, evidence = self.get(action, 'evidence', actor, None)
            if status != 200:
                raise AssertionError(f'action evidence returned HTTP {status}')
            receipt = next((item for item in evidence['notifications']
                            if item['event'] == event and item['state'] == 'ACKNOWLEDGED'), None)
            return receipt is not None

        self.wire.until(acknowledged, 12)
        with self.receiver.lock:
            received = self.receiver.attempts.get(receipt['notificationId'])
        attempts = [(attempt['number'], attempt['outcome']) for attempt in receipt['attemptLog']]
        self.check(receipt['channel'] == self.channel and receipt['recipientId'] == recipient and
                   receipt['actionId'] == action and receipt['acknowledgedAt'] and receipt['attempts'] == received == 2 and
                   attempts == [(1, 'RECEIVER_REJECTED'), (2, 'ACKNOWLEDGED')],
                   f'{event} receiver agrees with the durable failure and acknowledgement records')
        return receipt

    def claim(self, action, applicant, approval_receipt):
        _, headers, reply = self.get(action, 'authorization-code', applicant)
        code = reply['authorizationCode']
        self.secrets.append(code)
        self.check('no-store' in headers.get('cache-control', '') and len(code) == 22 and
                   parse_time(reply['expiresAt']) == parse_time(approval_receipt['expiresAt']),
                   'private claim shares notice expiry and uses no-store')
        return code, reply['expiresAt']

    def private_resume(self, action, code, accepted, transport=None):
        env = runtime_env(self.harness_install)
        command = [str(self.harness_install / EXECUTABLE), '--expose-internals',
                   windows(ROOT / 'scripts/dsh-private-resume.mjs'), action, '--private-pipe']
        payload = (json.dumps(self.transport if transport is None else transport) + '\n' + code + '\n').encode('utf-8')
        # Only sanitized outputs use temporary descriptors. The code stays in
        # the private stdin pipe, and inherited Windows handles cannot hold
        # stdout or stderr pipe drains open after the one-shot client exits.
        with TemporaryFile() as output, TemporaryFile() as diagnostics:
            try:
                result = subprocess.run(command, env=env, input=payload, stdout=output, stderr=diagnostics, timeout=40)
                returncode = result.returncode
            except subprocess.TimeoutExpired:
                returncode = None
            output.seek(0)
            diagnostics.seek(0)
            stdout, stderr = output.read(), diagnostics.read()
        self.check(no_secrets(stdout + stderr, self.secrets), 'private handoff output omits secrets')
        try:
            safe = json.loads(stderr.strip().splitlines()[-1])
        except (IndexError, ValueError):
            safe = {}
        if returncode == 1:
            reason = ' with unconfirmed action outcome' if safe.get('outcomeUnknown') else ''
            raise AssertionError(f"Harness private handoff stopped at {safe.get('failure', 'unknown phase')}{reason}")
        try:
            receipt = json.loads(stdout.strip().splitlines()[-1])
        except (IndexError, ValueError):
            receipt = {}
        desired_transport = self.transport if transport is None else transport
        well_formed = (isinstance(receipt, dict) and receipt.keys() == PRIVATE_RECEIPT_FIELDS and receipt['actionId'] == action and
                       receipt['transport'] == desired_transport['transport'] and isinstance(receipt['rejected'], bool) and
                       any(receipt[key] for key in ('outcome', 'actionState', 'errorCode')))
        if returncode is None:
            if not well_formed:
                raise AssertionError('Harness private handoff timed out without a receipt; reconcile the action in audit without resubmitting')
            returncode = 2 if receipt['rejected'] else 0
            receipt['cleanup'] = 'HOST_EXIT_UNCONFIRMED'
        elif safe.get('cleanup') == 'UNCONFIRMED':
            receipt['cleanup'] = 'UNCONFIRMED'
        expected = {0, 2} if accepted is None else {0 if accepted else 2}
        self.check(returncode in expected, 'private completion reports the expected approval result')
        self.visible_results.append(receipt)
        self.check(well_formed and receipt['rejected'] == (returncode == 2),
                   'private completion correlates action and decision')
        return receipt

    def other_applicant_transport(self):
        if self.entry == 'http':
            return {'transport': 'streamable-http', 'url': self.endpoint,
                    'headers': {'Authorization': 'Bearer ' + self.cfg['bob_token']}}
        return {'transport': 'stdio', 'command': self.transport['command'], 'args': [
            '-d', 'Ubuntu-24.04', '--exec', '/usr/bin/python3', '-B', '-c',
            suite.HOST_LAUNCHER.replace('alice_token', 'bob_token'), str(self.state / 'env.json'),
            str(BUILD / 'mcpguard-connect'), *self.tool_args,
        ]}

    def prepare_action(self, client, access, operation, target, version, reviewer, applicant, check_pending=False):
        before = self.native_snapshot()
        action = self.plan(client, access, operation, target, version)
        if check_pending:
            self.approve(reviewer, action, expected=409)
            self.check(self.native_snapshot() == before, 'reviewer cannot approve before the notification is acknowledged')
        self.notice(action, 'APPROVAL_REQUIRED', reviewer, self.report['reviewerId'])
        self.get(action, 'authorization-code', applicant, 403)
        if check_pending:
            self.private_resume(action, 'A' * 22, False)
        self.approve(applicant, action, expected=403)
        self.approve(reviewer, action)
        self.get(action, 'authorization-code', reviewer, 403)
        approval_receipt = self.notice(action, 'APPROVED', applicant, self.report['principalId'])
        self.check(self.native_snapshot() == before, f'{operation} approval and notification have no native effect')
        return before, action, approval_receipt

    def successful_action(self, client, access, operation, target, version, reviewer, applicant):
        first_operation = operation == OPERATIONS[0][0]
        if first_operation:
            other_op, other_target, other_version = OPERATIONS[1]
            _, other_action, other_receipt = self.prepare_action(client, access, other_op, other_target, other_version, reviewer, applicant)
            other_code, other_expiry = self.claim(other_action, applicant, other_receipt)
        before, action, approval_receipt = self.prepare_action(client, access, operation, target, version, reviewer, applicant)
        self.tool(client, 'action.resume', {'actionId': action}, True)
        code, _ = self.claim(action, applicant, approval_receipt)
        if first_operation:
            wrong = ('B' if code[0] == 'A' else 'A') + code[1:]
            self.private_resume(action, wrong, False)
            self.check(datetime.now(timezone.utc) < parse_time(other_expiry), 'other action code remains within its own validity window')
            self.private_resume(action, other_code, False)
            self.private_resume(action, code, False, self.other_applicant_transport())
            other_evidence = self.get(other_action, 'evidence', applicant)[2]
            self.check(not any(event['kind'] in ('CODE_CONSUMED', 'DISPATCH_INTENT') for event in other_evidence['events']),
                       'cross-action submission did not consume or dispatch the action that owns the code')
            self.report['unusedOtherAction'] = other_evidence
        self.check(self.native_snapshot() == before, 'a missing or wrong code does not change PostgreSQL')
        if first_operation:
            with ThreadPoolExecutor(max_workers=2) as pool:
                concurrent_results = list(pool.map(lambda _: self.private_resume(action, code, None), range(2)))
            winners = [result for result in concurrent_results if not result['rejected']]
            self.check(len(winners) == 1, 'two concurrent submissions of one valid code yield exactly one accepted receipt')
            self.report['concurrentCodeReceipts'] = concurrent_results
            private = winners[0]
        else:
            private = self.private_resume(action, code, True)
        self.check(private['actionState'] == 'SUCCEEDED' and private['dispatchState'] == 'DISPATCHED' and
                   private['executionStatus'] == 'SUCCEEDED', f'{operation} completed through the private Harness input')
        after = self.native_snapshot()
        self.check(after == expected_snapshot(before, operation, target), f'{operation} PostgreSQL effect is exactly the approved scope')
        self.private_resume(action, code, False)
        self.get(action, 'authorization-code', applicant, 403)
        self.check(self.native_snapshot() == after, f'{operation} code replay preserved PostgreSQL state')
        evidence = self.get(action, 'evidence', applicant)[2]
        kinds = [event['kind'] for event in evidence['events']]
        self.check(evidence['actionId'] == action and evidence['state'] == 'SUCCEEDED' and
                   all(kinds.count(kind) == 1 for kind in ('PLANNED', 'APPROVED', 'CODE_ISSUED', 'CODE_CONSUMED', 'DISPATCH_INTENT', 'SUCCEEDED')),
                   f'{operation} audit contains one approval, one consumption and one dispatch')
        event_details = [event['detail'] for event in evidence['events'] if event['kind'] == 'CODE_DENIED']
        reasons = {detail.get('reason') for detail in event_details}
        expected = {'MISSING_CODE', 'ALREADY_CONSUMED'}
        if first_operation:
            expected |= {'INVALID_CODE', 'WRONG_APPLICANT'}
            self.check(sum(detail.get('reason') == 'INVALID_CODE' for detail in event_details) == 2,
                       'audit records invalid-code denials for both the invented and cross-action codes')
        self.check(expected <= reasons, f'{operation} audit explains the blocked submissions')
        self.report['operations'].append({'operation': operation, 'toolReceipt': private, 'audit': evidence,
                                          'beforePostgreSQL': before, 'afterPostgreSQL': after})

    def prepare_expiring_action(self, client, access, reviewer, applicant):
        operation, target, version = OPERATIONS[0]
        _, action, receipt = self.prepare_action(client, access, operation, target, version, reviewer, applicant, check_pending=True)
        code, expiry = self.claim(action, applicant, receipt)
        return action, code, expiry

    def submit_expired_action(self, action, code, expiry, applicant):
        delay = (parse_time(expiry) - datetime.now(timezone.utc)).total_seconds()
        if delay > 0:
            time.sleep(delay + 0.1)
        before = self.native_snapshot()
        private = self.private_resume(action, code, False)
        evidence = self.get(action, 'evidence', applicant)[2]
        reasons = {event['detail'].get('reason') for event in evidence['events'] if event['kind'] == 'CODE_DENIED'}
        self.check({'NOT_APPROVED', 'CODE_EXPIRED'} <= reasons and
                   all(event['kind'] != 'DISPATCH_INTENT' for event in evidence['events']) and self.native_snapshot() == before,
                   'expired code has an audit denial, no dispatch and no native effect')
        self.report['expiredCode'] = {'toolReceipt': private, 'audit': evidence}

    def run(self):
        self.check(self.query("SELECT to_regnamespace('mcpguard_w2_fixture') IS NULL", pg=0) == 't',
                   'the isolated orders database has no Wave 2 changes before this run')
        self.enroll()
        with self.gateway(startup_timeout=self.gateway_startup_seconds), self.harness_client() as client:
            reviewer, applicant = self.login('visitor'), self.login('alice')
            access = self.provision(client, reviewer, 1, write=True)
            self.install_native(access)
            before = self.native_snapshot()
            self.tool(client, 'pg.plan_high_risk', {'resourceId': access['resourceId'], 'credentialId': access['credentialId'],
                                                  'operation': OPERATIONS[0][0], 'target': OPERATIONS[0][1], 'expectedVersion': 1,
                                                  'note': 'cannot change the frozen operation'}, True)
            self.check(self.native_snapshot() == before and self.query("SELECT count(*) FROM mcpguard_ra.high_risk_writes") == '0',
                       'an extra tool argument is rejected before creating an action or changing native state')
            expiry_action, expiring_code, expiry = self.prepare_expiring_action(client, access, reviewer, applicant)
            for op, target, version in OPERATIONS:
                self.successful_action(client, access, op, target, version, reviewer, applicant)
            self.submit_expired_action(expiry_action, expiring_code, expiry, applicant)
        visible = json.dumps([self.report, self.visible_results]).encode('utf-8')
        logs = (self.state / 'ra18-gateway.log', self.state / 'wave2-harness.private.log')
        self.check(all(no_secrets(data, self.secrets) for data in (visible, *(path.read_bytes() for path in logs))),
                   'model-visible results, evidence and logs omit credentials and codes')


def parse_time(value):
    return datetime.fromisoformat(value.replace('Z', '+00:00'))


def no_secrets(data, secrets):
    return all(secret.encode(encoding) not in data for secret in secrets for encoding in ('utf-8', 'utf-16-le', 'utf-16-be'))


def expected_snapshot(before, operation, target):
    after = deepcopy(before)
    if operation == 'UPDATE_ORDER_AMOUNT':
        row = next(row for row in after['orders'] if (row['tenant_id'], row['order_id']) == ('tenant_a', target))
        row.update(amount=120, version=2)
    elif operation == 'CREATE_ORDERS_INDEX':
        after['index'] = {'definition': 'CREATE INDEX orders_shipping_lookup_idx ON mcpguard_w2_fixture.orders USING btree (shipping_code)',
                          'valid': True}
    elif operation == 'DELETE_CANCELLED_ORDER':
        after['orders'] = [row for row in after['orders'] if (row['tenant_id'], row['order_id']) != ('tenant_a', target)]
    return after


def fingerprints():
    paths = tuple(BUILD / name for name in ('mcpguard', 'mcpguard-connect', 'mcpguard-http-helper')) + (NATIVE_FIXTURE,)
    paths += tuple(ROOT / 'scripts' / name for name in (
        'dsh-private-resume.mjs', 'dsh-tool-host.mjs', 'dsh_client.py', 'high_risk_notification_receiver.py',
        'ra18_client_fixture.py', 'wave2_high_risk_fixture.py',
    ))
    result = {}
    for path in paths:
        with path.open('rb') as evidence_file:
            result[path.name] = file_digest(evidence_file, 'sha256').hexdigest()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('entry', choices=('http', 'stdio'))
    parser.add_argument('--report', type=Path)
    parser.add_argument('--prepare-only', action='store_true')
    parser.add_argument('--use-prepared', action='store_true')
    build = parser.add_mutually_exclusive_group()
    build.add_argument('--rebuild', action='store_true', help='rebuild binaries when running a prepared database')
    build.add_argument('--reuse-build', action='store_true', help='prepare a new fixture using the existing candidate binaries')
    args = parser.parse_args()
    if not BUILD.is_relative_to(ROOT) and (args.rebuild or not (args.use_prepared or args.reuse_build)):
        parser.error('An external candidate build directory must be used with --use-prepared or --reuse-build without rebuilding.')
    configure(args.entry)
    if not args.use_prepared:
        if args.reuse_build:
            if not all((BUILD / program).is_file() for program in suite.fixture.BINARIES):
                raise SystemExit('A complete candidate build is required before reusing its binaries.')
            suite.fixture.base.build = lambda: None
        suite.prepare()
    elif args.rebuild:
        suite.fixture.base.build()
    if args.prepare_only:
        return
    harness = install_path()
    if not (harness / EXECUTABLE).is_file():
        raise SystemExit('DeepSeek Harness installation unavailable; set MCPGUARD_DSH_INSTALL to its WSL path.')
    state, config = suite.fixture.base.selected()
    suite.fixture.base.dev.STATE = state
    candidate = fingerprints()

    with Receiver(0, fail_first=True) as receiver:
        worker = Thread(target=receiver.serve_forever, daemon=True)
        worker.start()
        try:
            scenario = Scenario(state, config, args.entry, harness, receiver)
            scenario.run()
        finally:
            receiver.shutdown()
            worker.join(3)
    scenario.report['checkedAt'] = datetime.now(timezone.utc).isoformat()
    scenario.check(fingerprints() == candidate, 'candidate binaries and Harness adapters stayed fixed during the run')
    scenario.report['sha256'] = candidate
    scenario.report['status'] = 'PASS'
    if args.report:
        args.report.write_text(json.dumps(scenario.report, indent=2) + '\n', encoding='utf-8')
    print(json.dumps({'entry': args.entry, 'status': 'PASS', 'positiveActions': len(scenario.report['operations']),
                      'checks': len(scenario.report['checks']), 'fixture': config['runId']}), flush=True)


if __name__ == '__main__':
    main()
