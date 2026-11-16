#!/usr/bin/env python3
"""RA-13 revocation scenes. Annexes the RA-09 context scene without changing it."""
import argparse, importlib.util, json, os, subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('read_base', ROOT / 'scripts/read_fixture.py')
read = importlib.util.module_from_spec(spec)
spec.loader.exec_module(read)
base = read.base


def prepare():
    read.prepare()
    state, cfg = base.selected()
    cfg['raRevokePhase'] = True
    base.dev.write_private(state / 'env.json', json.dumps(cfg))
    print(json.dumps({'phase': 'RA-13', 'runId': cfg['runId'], 'privateState': str(state),
                      'revocationPhase': True, 'legacyScenesChanged': False}))


def test():
    os.environ['MCPGUARD_TEST_RUN'] = 'TestRevocation'
    read.test()


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['prepare', 'test'])
    try:
        {'prepare': prepare, 'test': test}[parser.parse_args().command]()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f'RA-13 isolated command failed (exit {error.returncode}); private failure scene retained.')
