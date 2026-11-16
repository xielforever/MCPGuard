#!/usr/bin/env python3
"""RA-12 controlled write scenes on a dedicated metadata database."""
import argparse, importlib.util, json, os, subprocess
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('read_base', ROOT / 'scripts/read_fixture.py')
read = importlib.util.module_from_spec(spec)
spec.loader.exec_module(read)
base = read.base


def prepare():
    os.environ['MCPGUARD_FIXTURE_METADATA_DB'] = '1'
    os.environ['MCPGUARD_FIXTURE_WRITE'] = '1'
    read.prepare()
    state, cfg = base.selected()
    name = cfg['fixtureMetadataDb']

    def swap(dsn):
        parts = urlsplit(dsn)
        return urlunsplit((parts.scheme, parts.netloc, '/' + name, parts.query, parts.fragment))

    for key in ('credentialDsn', 'deliveryDsn', 'contextDsn', 'provisionerDsn'):
        if cfg.get(key):
            cfg[key] = swap(cfg[key])
    base.admin(cfg, 'postgres', 'GRANT CONNECT ON DATABASE %s TO mcpguard_app,mcpguard_contexts,mcpguard_credentials,mcpguard_downloads,mcpguard_provisioner;' % name)
    # The isolated write scene does not run the legacy HTTP/Redis regression.
    cfg.pop('raRedis', None)
    cfg['raWritePhase'] = True
    base.dev.write_private(state / 'env.json', json.dumps(cfg))
    print(json.dumps({'phase': 'RA-12', 'runId': cfg['runId'], 'privateState': str(state),
                      'metadataDb': name, 'writeProfiles': True, 'legacyScenesChanged': False}))


def test():
    os.environ['MCPGUARD_TEST_RUN'] = 'TestControlledWrite'
    read.test()


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['prepare', 'test'])
    try:
        {'prepare': prepare, 'test': test}[parser.parse_args().command]()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f'RA-12 isolated command failed (exit {error.returncode}); private failure scene retained.')
