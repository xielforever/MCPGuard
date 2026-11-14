#!/usr/bin/env python3
"""RA-11 planned read scenes. Earlier scenes and evidence are immutable."""
import argparse, importlib.util, json, os, subprocess
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('context_base', ROOT / 'scripts/context_fixture.py')
context = importlib.util.module_from_spec(spec)
spec.loader.exec_module(context)
base = context.base


def prepare():
    context.prepare()
    state, cfg = base.selected()
    env = os.environ | {'MCPGUARD_MIGRATION_DSN': cfg['MCPGUARD_MIGRATION_DSN']}
    subprocess.run([str(base.BUILD / 'mcpguard-catalog'), 'grant-read-runtime', 'mcpguard_contexts'],
                   env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    cfg['raReadPhase'] = True
    base.dev.write_private(state / 'env.json', json.dumps(cfg))
    print(json.dumps({'phase': 'RA-11', 'runId': cfg['runId'], 'privateState': str(state),
                      'plannedReads': True, 'legacyScenesChanged': False}))


def test():
    base.test()


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['prepare', 'test'])
    args = parser.parse_args()
    try:
        {'prepare': prepare, 'test': test}[args.command]()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f'RA-11 isolated command failed (exit {error.returncode}); private failure scene retained.')
