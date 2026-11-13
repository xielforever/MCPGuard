#!/usr/bin/env python3
"""RA-06 fresh owned scenes. Preserve RA-05 scenes, scripts and evidence."""
import argparse,importlib.util,json,os,subprocess,sys,time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('provision_base',ROOT/'scripts/provision_fixture.py')
provision=importlib.util.module_from_spec(spec);spec.loader.exec_module(provision);base=provision.base
STATE_ROOT=Path.home()/'.local/state/mcpguard-ra06';STATE_ROOT.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(STATE_ROOT,0o700)
base.BUILD=ROOT/'.dev/ra06-build';base.POINTER=ROOT/'.dev/ra06-latest';base.Path=lambda *parts:STATE_ROOT if parts==('/tmp',) else Path(*parts)
def build():
    base.BUILD.mkdir(parents=True,exist_ok=True)
    commands=['python3 scripts/generate_v02_contracts.py --check']
    for name in ('mcpguard','mcpguard-catalog','mcpguard-connect','mcpguard-demo-http','mcpguard-provision'):
        commands.append(f'go build -p 2 -o .dev/ra06-build/{name} ./cmd/{name}')
    commands.append('go test -c -o .dev/ra06-build/resources.test ./internal/resources')
    provision.original_runner(['sh','-c',' && '.join(commands)])
base.build=build
def prepare():
    provision.prepare();state,cfg=base.selected();cfg['raRecoveryPhase']=True;base.dev.write_private(state/'env.json',json.dumps(cfg))
    print(json.dumps({'phase':'RA-06','runId':cfg['runId'],'privateState':str(state),'legacyScenesChanged':False}))
def smoke():
    state,cfg=base.selected();env=base.dev.runtime_env(cfg)
    env|={'MCPGUARD_TEST_STATE':str(state),'MCPGUARD_TEST_BINARY':str(base.BUILD/'mcpguard'),'MCPGUARD_TEST_CONNECT_BINARY':str(base.BUILD/'mcpguard-connect'),'MCPGUARD_TEST_PROVISION_BINARY':str(base.BUILD/'mcpguard-provision'),'MCPGUARD_TEST_RECOVERY_PROBE':str(base.BUILD/'resources.test')}
    result=subprocess.run([sys.executable,'tests/recovery_e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    log=state/('recovery-wire-'+str(time.time_ns())+'.log');base.dev.write_private(log,result.stdout)
    if result.returncode:raise SystemExit('RA-06 process verification failed; private evidence: '+str(log))
    report=json.loads((state/'recovery-e2e-results.json').read_text());print(json.dumps({'exitCode':0,'checks':report['passed'],'evidence':str(state/'recovery-e2e-results.json')}))
if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['prepare','test','wire','access-smoke','provision-smoke','smoke','contracts']);args=parser.parse_args()
    try:{'prepare':prepare,'test':base.test,'wire':base.wire,'access-smoke':provision.access.smoke,'provision-smoke':provision.smoke,'smoke':smoke,'contracts':base.contracts}[args.command]()
    except subprocess.CalledProcessError as error:raise SystemExit(f'RA-06 isolated command failed (exit {error.returncode}); private failure scene retained.')
