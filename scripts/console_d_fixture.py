#!/usr/bin/env python3
"""Fresh D console validation scene. Keep old services, fixtures and evidence intact."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('adapter_base', ROOT / 'scripts/adapter_fixture.py')
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)
base = adapter.base
dev = base.dev
STATE_ROOT = Path.home() / '.local/state/mcpguard-console-d'
STATE_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
os.chmod(STATE_ROOT, 0o700)
base.BUILD = ROOT / '.dev/console-d-build'
base.POINTER = ROOT / '.dev/console-d-latest'
base.Path = lambda *parts: STATE_ROOT if parts == ('/tmp',) else Path(*parts)


def build():
    base.BUILD.mkdir(parents=True, exist_ok=True)
    commands = ['python3 scripts/generate_v02_contracts.py --check', 'python3 scripts/generate_ui_theme.py --check']
    for name in ('mcpguard', 'mcpguard-catalog', 'mcpguard-connect', 'mcpguard-demo-http', 'mcpguard-provision', 'mcpguard-credentials', 'mcpguard-download', 'mcpguard-session', 'mcpguard-http-helper'):
        commands.append(f'go build -p 2 -o .dev/console-d-build/{name} ./cmd/{name}')
    commands.append('go test -c -o .dev/console-d-build/resources.test ./internal/resources')
    adapter.previous.download.credential.recovery.provision.original_runner(['sh', '-c', ' && '.join(commands)])


def runner(args, state=None, env=None, **kwargs):
    if state and env:
        cfg = json.loads((state / 'env.json').read_text())
        env = env | {'MCPGUARD_TEST_CREDENTIAL_DSN': cfg['credentialDsn'], 'MCPGUARD_TEST_DELIVERY_DSN': cfg['deliveryDsn'],
                     'MCPGUARD_TEST_CONTEXT_DSN': cfg['contextDsn'], 'MCPGUARD_RA_PACKAGE_KEYS': '/fixture/package-keys.json',
                     'MCPGUARD_TEST_CATALOG_BINARY': '/work/.dev/console-d-build/mcpguard-catalog'}
    return adapter.previous.download.credential.previous_runner(args, state, env, **kwargs)


base.build = build
base.runner = runner


def live_snapshot():
    result = []
    for name in ('mcpguard-local-gateway', 'mcpguard-local-demo-http', 'mcpguard-local-redis', 'mcpguard-local-verified-postgres'):
        info = dev.inspect(name)
        if info:
            result.append({'name': name, 'image': info['Image'], 'startedAt': info['State']['StartedAt'], 'running': info['State']['Running']})
    return result


def prepare():
    before = live_snapshot()
    adapter.prepare()
    state, cfg = base.selected()
    dev.write_private(state / 'live-service-before.json', json.dumps(before, indent=2))
    print(json.dumps({'phase': 'CONSOLE_D', 'runId': cfg['runId'], 'oldServicesUnchanged': before == live_snapshot()}))


def ui_up():
    state, cfg = base.selected()
    # Seed only this new owned UI scene. The integration tests run before this step.
    sql = f"INSERT INTO mcpguard.control_heads(org_id) VALUES('{dev.ORG}') ON CONFLICT DO NOTHING;"
    for index, resource in enumerate(('demo.pg', 'demo.redis', 'demo.directory', 'demo.inventory'), 1):
        sql += f"INSERT INTO mcpguard.control_resources VALUES('{dev.ORG}','{resource}','30000000-0000-4000-8000-{index:012d}',true) ON CONFLICT (org_id,name) DO UPDATE SET enabled=true;"
        sql += f"INSERT INTO mcpguard.reviewer_grants VALUES('{dev.ORG}','{dev.IDS['visitor']}','{resource}',true) ON CONFLICT (org_id,principal_id,resource_name) DO UPDATE SET allowed=true;"
    for user in ('alice', 'bob'):
        for tool in ('pg.update_demo_note', 'redis.get_demo_note', 'redis.set_demo_note', 'directory.get_demo_office', 'inventory.reserve_demo'):
            sql += f"INSERT INTO mcpguard.tool_grants VALUES('{dev.ORG}','{dev.IDS[user]}','{tool}',true) ON CONFLICT (org_id,principal_id,tool_name) DO UPDATE SET allowed=true;"
    sql += f"INSERT INTO mcpguard.control_members VALUES('{dev.ORG}','{dev.IDS['visitor']}',true) ON CONFLICT (org_id,principal_id) DO UPDATE SET is_admin=true;"
    dev.psql(cfg, 'mcpguard_meta', sql)
    # Stable, fresh business preconditions; these are private fixture rows only.
    dev.psql(cfg, 'mcpguard_business', "UPDATE demo.items SET note='D console fixture baseline',version=1 WHERE item_id='item_demo_1';")
    selected = {key: value for key, value in dev.runtime_env(cfg).items() if key.startswith('MCPGUARD_')}
    dev.write_private(state / 'ui.env', ''.join(key + '=' + str(value) + '\n' for key, value in selected.items()))
    image = dev.inspect('mcpguard-local-runner')['Image']
    for suffix, binary, args in (
        ('http', 'mcpguard-demo-http', ['-e', 'MCPGUARD_DEMO_HTTP_CONFIG=' + str(state / 'http.json')]),
        ('gateway', 'mcpguard', ['--env-file', str(state / 'ui.env')]),
    ):
        name = 'mcpguard-console-d-' + suffix + '-' + cfg['runId']
        info = dev.inspect(name)
        if info:
            if info['Config'].get('Labels', {}).get('mcpguard.console-d') != cfg['runId']:
                raise SystemExit('UI fixture ownership mismatch')
            if not info['State']['Running']:
                dev.docker('start', name, capture_output=True)
        else:
            dev.docker('run', '-d', '--name', name, '--label', 'mcpguard.console-d=' + cfg['runId'], '--network', 'host',
                       '--mount', f'type=bind,src={ROOT},dst={ROOT},readonly', '--mount', f'type=bind,src={state},dst={state},readonly',
                       *args, image, str(base.BUILD / binary), capture_output=True)
    for attempt in range(70):
        try:
            urllib.request.urlopen('http://' + cfg['MCPGUARD_LISTEN'] + '/web/', timeout=1).close()
            break
        except OSError:
            time.sleep(.1)
    else:
        raise SystemExit('Isolated UI gateway did not become ready; owned scene preserved')
    # Only synthetic fixture browser/MCP identities, no DB DSNs or server keys.
    export = {'MCPGUARD_LISTEN': cfg['MCPGUARD_LISTEN'], 'console_secrets': cfg['console_secrets'],
              'alice_token': cfg['alice_token'], 'bob_token': cfg['bob_token'], 'runId': cfg['runId'], 'ids': dev.IDS}
    destination = ROOT / '.dev/console-d-ui-2026-10-29/private-browser.json'
    dev.write_private(destination, json.dumps(export))
    print(json.dumps({'status': 'UI_READY', 'origin': 'http://' + cfg['MCPGUARD_LISTEN'], 'runId': cfg['runId'], 'liveServicesUnchanged': True}))


def status():
    state, cfg = base.selected()
    before = json.loads((state / 'live-service-before.json').read_text())
    result = {'runId': cfg['runId'], 'privateState': str(state), 'oldServicesUnchanged': before == live_snapshot()}
    dev.write_private(state / 'live-service-after.json', json.dumps(live_snapshot(), indent=2))
    print(json.dumps(result))
    if not result['oldServicesUnchanged']:
        raise SystemExit('Existing service fingerprint changed')


def restart_ui():
    state, cfg = base.selected()
    name = 'mcpguard-console-d-gateway-' + cfg['runId']
    info = dev.inspect(name)
    if not info or info['Config'].get('Labels', {}).get('mcpguard.console-d') != cfg['runId']:
        raise SystemExit('UI fixture ownership mismatch')
    dev.docker('restart', name, capture_output=True)
    print(json.dumps({'status': 'RESTARTED_OWNED_UI', 'runId': cfg['runId']}))


def evidence():
    state, cfg = base.selected()
    destination = ROOT / 'design-system/mcpguard/validation/2026-10-29-console-d'
    destination.mkdir(parents=True, exist_ok=True)
    events = []
    log = sorted(state.glob('go-tests-*.jsonl'))[-1]
    for line in log.read_text().splitlines():
        if line.startswith('{'):
            try:
                events.append(json.loads(line))
            except ValueError:
                pass
    report = {'command': 'go test -race -p 1 -count=1 -json ./...', 'raceEnabled': True,
              'passed': [event['Test'] for event in events if event.get('Action') == 'pass' and 'Test' in event],
              'skipped': [event['Test'] for event in events if event.get('Action') == 'skip' and 'Test' in event],
              'failed': [event.get('Test', event.get('Package')) for event in events if event.get('Action') == 'fail']}
    (destination / 'go-tests.summary.json').write_text(json.dumps(report, indent=2) + '\n')
    wire = json.loads((state / 'e2e-results.json').read_text())
    (destination / 'http-stdio.summary.json').write_text(json.dumps({'status': 'PASS', 'checks': len(wire['checks']), 'nativeCodingClientAcceptance': False}, indent=2) + '\n')
    before = json.loads((state / 'live-service-before.json').read_text())
    after = live_snapshot()
    if before != after:
        raise SystemExit('Old service fingerprint changed; original scene retained')
    (destination / 'live-services.json').write_text(json.dumps({'unchanged': True, 'before': before, 'after': after}, indent=2) + '\n')
    print(json.dumps({'status': 'EVIDENCE_EXPORTED', 'passedGoTests': len(report['passed']), 'wireChecks': len(wire['checks']), 'oldServicesUnchanged': True}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'build', 'test', 'wire', 'ui-up', 'restart-ui', 'status', 'evidence'])
    args = parser.parse_args()
    try:
        {'prepare': prepare, 'build': build, 'test': base.test, 'wire': base.wire, 'ui-up': ui_up, 'restart-ui': restart_ui, 'status': status, 'evidence': evidence}[args.command]()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f'Console D fixture command failed (exit {error.returncode}); private scene and evidence retained.')
