#!/usr/bin/env python3
"""RA-10 fresh helper/stdio scenes. Never reuses or resets earlier scenes."""
import argparse,importlib.util,json,os,subprocess,sys,time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('context_base',ROOT/'scripts/context_fixture.py');previous=importlib.util.module_from_spec(spec);spec.loader.exec_module(previous);base=previous.base
STATE_ROOT=Path.home()/'.local/state/mcpguard-ra10';STATE_ROOT.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(STATE_ROOT,0o700)
base.BUILD=ROOT/'.dev/ra10-build';base.POINTER=ROOT/'.dev/ra10-latest';base.Path=lambda *parts:STATE_ROOT if parts==('/tmp',) else Path(*parts)
def runner(args,state=None,env=None,**kwargs):
    if state and env:
        cfg=json.loads((state/'env.json').read_text());env=env|{'MCPGUARD_TEST_CREDENTIAL_DSN':cfg['credentialDsn'],'MCPGUARD_TEST_DELIVERY_DSN':cfg['deliveryDsn'],'MCPGUARD_TEST_CONTEXT_DSN':cfg['contextDsn'],'MCPGUARD_RA_PACKAGE_KEYS':'/fixture/package-keys.json','MCPGUARD_TEST_CATALOG_BINARY':'/work/.dev/ra10-build/mcpguard-catalog'}
    return previous.download.credential.previous_runner(args,state,env,**kwargs)
def build():
    base.BUILD.mkdir(parents=True,exist_ok=True);commands=['python3 scripts/generate_v02_contracts.py --check']
    for name in ('mcpguard','mcpguard-catalog','mcpguard-connect','mcpguard-demo-http','mcpguard-provision','mcpguard-credentials','mcpguard-download','mcpguard-session','mcpguard-http-helper'):commands.append(f'go build -p 2 -o .dev/ra10-build/{name} ./cmd/{name}')
    commands.append('go test -c -o .dev/ra10-build/resources.test ./internal/resources');previous.download.credential.recovery.provision.original_runner(['sh','-c',' && '.join(commands)])
base.build=build;base.runner=runner
def prepare():
    previous.prepare();state,cfg=base.selected();cfg['raAdapterPhase']=True;base.dev.write_private(state/'env.json',json.dumps(cfg));print(json.dumps({'phase':'RA-10','runId':cfg['runId'],'privateState':str(state),'legacyScenesChanged':False}))
def smoke():
    state,cfg=base.selected();env=base.dev.runtime_env(cfg)|{'MCPGUARD_TEST_STATE':str(state)}
    for name,key in [('mcpguard','BINARY'),('mcpguard-connect','CONNECT_BINARY'),('mcpguard-provision','PROVISION_BINARY'),('mcpguard-credentials','CREDENTIAL_BINARY'),('mcpguard-download','DOWNLOAD_BINARY'),('mcpguard-session','SESSION_BINARY'),('mcpguard-http-helper','HELPER_BINARY')]:env['MCPGUARD_TEST_'+key]=str(base.BUILD/name)
    r=subprocess.run([sys.executable,'tests/adapter_e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True);log=state/('adapter-wire-'+str(time.time_ns())+'.log');base.dev.write_private(log,r.stdout)
    if r.returncode:raise SystemExit('RA-10 process verification failed; private evidence: '+str(log))
    report=json.loads((state/'adapter-e2e-results.json').read_text());print(json.dumps({'exitCode':0,'checks':report['passed'],'evidence':str(state/'adapter-e2e-results.json')}))
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('command',choices=['prepare','test','wire','access-smoke','provision-smoke','credential-smoke','download-smoke','context-smoke','recovery-smoke','smoke','contracts']);a=p.parse_args()
    try:{'prepare':prepare,'test':base.test,'wire':base.wire,'access-smoke':previous.download.credential.recovery.provision.access.smoke,'provision-smoke':previous.download.credential.recovery.provision.smoke,'credential-smoke':previous.download.credential.smoke,'download-smoke':previous.download.smoke,'context-smoke':previous.smoke,'recovery-smoke':previous.download.credential.recovery.smoke,'smoke':smoke,'contracts':base.contracts}[a.command]()
    except subprocess.CalledProcessError as e:raise SystemExit(f'RA-10 command failed (exit {e.returncode}); private scene retained.')
