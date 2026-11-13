#!/usr/bin/env python3
"""RA-08 fresh download/private-delivery scenes. Preserve every older scene."""
import argparse,importlib.util,json,os,secrets,subprocess,sys,time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('credential_base',ROOT/'scripts/credential_fixture.py')
credential=importlib.util.module_from_spec(spec);spec.loader.exec_module(credential);base=credential.base
STATE_ROOT=Path.home()/'.local/state/mcpguard-ra08';STATE_ROOT.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(STATE_ROOT,0o700)
base.BUILD=ROOT/'.dev/ra08-build';base.POINTER=ROOT/'.dev/ra08-latest';base.Path=lambda *parts:STATE_ROOT if parts==('/tmp',) else Path(*parts)
def runner(args,state=None,env=None,**kwargs):
    if state and env:
        cfg=json.loads((state/'env.json').read_text())
        env=env|{'MCPGUARD_TEST_CREDENTIAL_DSN':cfg['credentialDsn'],'MCPGUARD_RA_PACKAGE_KEYS':'/fixture/package-keys.json','MCPGUARD_TEST_CATALOG_BINARY':'/work/.dev/ra08-build/mcpguard-catalog','MCPGUARD_TEST_DELIVERY_DSN':cfg['deliveryDsn']}
    return credential.previous_runner(args,state,env,**kwargs)
def build():
    base.BUILD.mkdir(parents=True,exist_ok=True);commands=['python3 scripts/generate_v02_contracts.py --check']
    for name in ('mcpguard','mcpguard-catalog','mcpguard-connect','mcpguard-demo-http','mcpguard-provision','mcpguard-credentials','mcpguard-download'):
        commands.append(f'go build -p 2 -o .dev/ra08-build/{name} ./cmd/{name}')
    commands.append('go test -c -o .dev/ra08-build/resources.test ./internal/resources')
    credential.recovery.provision.original_runner(['sh','-c',' && '.join(commands)])
base.build=build;base.runner=runner
def prepare():
    credential.prepare();state,cfg=base.selected()
    from urllib.parse import urlsplit
    cfg['delivery_password']=secrets.token_hex(24)
    base.admin(cfg,'postgres',f"CREATE ROLE mcpguard_downloads LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg['delivery_password']}';GRANT CONNECT ON DATABASE mcpguard_meta TO mcpguard_downloads;")
    address=urlsplit(cfg['MCPGUARD_METADATA_DSN']).netloc.rsplit('@',1)[1]
    cfg['deliveryDsn']=f"postgres://mcpguard_downloads:{cfg['delivery_password']}@{address}/mcpguard_meta?sslmode=disable"
    env=os.environ|{'MCPGUARD_MIGRATION_DSN':cfg['MCPGUARD_MIGRATION_DSN']}
    for args in (['grant-delivery-runtime','mcpguard_downloads'],['grant-delivery-enrollment','mcpguard_credentials']):
        subprocess.run([str(base.BUILD/'mcpguard-catalog'),*args],env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    cfg['raDownloadPhase']=True;base.dev.write_private(state/'env.json',json.dumps(cfg))
    print(json.dumps({'phase':'RA-08','runId':cfg['runId'],'privateState':str(state),'separateDeliveryRuntime':True,'legacyScenesChanged':False}))
def smoke():
    state,cfg=base.selected();env=base.dev.runtime_env(cfg)
    env|={'MCPGUARD_TEST_STATE':str(state),'MCPGUARD_TEST_BINARY':str(base.BUILD/'mcpguard'),'MCPGUARD_TEST_CONNECT_BINARY':str(base.BUILD/'mcpguard-connect'),'MCPGUARD_TEST_PROVISION_BINARY':str(base.BUILD/'mcpguard-provision'),'MCPGUARD_TEST_CREDENTIAL_BINARY':str(base.BUILD/'mcpguard-credentials'),'MCPGUARD_TEST_DOWNLOAD_BINARY':str(base.BUILD/'mcpguard-download')}
    r=subprocess.run([sys.executable,'tests/download_e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    log=state/('download-wire-'+str(time.time_ns())+'.log');base.dev.write_private(log,r.stdout)
    if r.returncode:raise SystemExit('RA-08 process verification failed; private evidence: '+str(log))
    report=json.loads((state/'download-e2e-results.json').read_text());print(json.dumps({'exitCode':0,'checks':report['passed'],'evidence':str(state/'download-e2e-results.json')}))
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('command',choices=['prepare','test','wire','access-smoke','provision-smoke','credential-smoke','recovery-smoke','smoke','contracts']);a=p.parse_args()
    try:{'prepare':prepare,'test':base.test,'wire':base.wire,'access-smoke':credential.recovery.provision.access.smoke,'provision-smoke':credential.recovery.provision.smoke,'credential-smoke':credential.smoke,'recovery-smoke':credential.recovery.smoke,'smoke':smoke,'contracts':base.contracts}[a.command]()
    except subprocess.CalledProcessError as e:raise SystemExit(f'RA-08 isolated command failed (exit {e.returncode}); private failure scene retained.')
