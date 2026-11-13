#!/usr/bin/env python3
"""RA-04 fresh fixtures; old RA-01/03 scenes, scripts and evidence stay intact."""
import argparse,hashlib,importlib.util,json,os,secrets,subprocess,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('ra_base',ROOT/'scripts/resource_fixture.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base)
base.BUILD=ROOT/'.dev/ra04-build';base.POINTER=ROOT/'.dev/ra04-latest'
# systemd may clear /tmp after a WSL restart. Preserve the private state on the
# Linux filesystem, while retaining the base fixture's strict parent checks.
STATE_ROOT=Path.home()/'.local/state/mcpguard-ra04'
STATE_ROOT.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(STATE_ROOT,0o700)
base.Path=lambda *parts:STATE_ROOT if parts==('/tmp',) else Path(*parts)

def build():
    base.BUILD.mkdir(parents=True,exist_ok=True)
    commands=['python3 scripts/generate_v02_contracts.py --check']
    for name in ('mcpguard','mcpguard-catalog','mcpguard-connect','mcpguard-demo-http'):
        commands.append(f'go build -p 2 -o .dev/ra04-build/{name} ./cmd/{name}')
    base.runner(['sh','-c',' && '.join(commands)])
base.build=build

def prepare():
    base.prepare()
    state,cfg=base.selected();catalog=json.loads((state/'catalog.json').read_text())
    policies=[];heads=[];reviewers=[]
    for profile in catalog['profiles']:
        policy={k:profile[k] for k in ('orgId','resourceId','profileId','approvalPolicyRef')}
        policy.update({'resourceVersion':'1','profileVersion':profile['version'],'version':'1','maxGrantTtlSeconds':86400,'reviewTtlSeconds':3600})
        policies.append(policy)
        heads.append({k:v for k,v in policy.items() if k not in ('approvalPolicyRef','maxGrantTtlSeconds','reviewTtlSeconds')}|{'revision':'1','enabled':True})
        for user in ('alice','visitor'):
            reviewers.append({'orgId':base.dev.ORG,'resourceId':profile['resourceId'],'principalId':base.dev.IDS[user],'canApprove':True})
    config={'schemaVersion':'0.2','policies':policies,'heads':heads,'reviewers':reviewers}
    base.dev.write_private(state/'access.json',json.dumps(config))
    cfg['console_secrets']={name:secrets.token_urlsafe(32) for name in base.dev.IDS}
    logins=[{'Username':name,'OrgID':base.dev.ORG,'PrincipalID':identity,'PasswordHash':hashlib.sha256(cfg['console_secrets'][name].encode()).hexdigest()} for name,identity in base.dev.IDS.items()]
    base.dev.write_private(state/'access-console.json',json.dumps(logins))
    cfg['MCPGUARD_CONSOLE_CONFIG']=str(state/'access-console.json')
    env=os.environ.copy();env['MCPGUARD_MIGRATION_DSN']=cfg['MCPGUARD_MIGRATION_DSN']
    for args in (['grant-access-runtime','mcpguard_app'],['import-access',str(state/'access.json')],['import-access',str(state/'access.json')]):
        subprocess.run([str(base.BUILD/'mcpguard-catalog'),*args],env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    cfg['raAccessPhase']=True;base.dev.write_private(state/'env.json',json.dumps(cfg))
    print(json.dumps({'phase':'RA-04','runId':cfg['runId'],'policies':len(policies),'independentBrowserLogins':len(logins),'existingServiceChanged':False}))

def wire():base.wire()
def smoke():
    state,cfg=base.selected();env=base.dev.runtime_env(cfg)
    env.update({'MCPGUARD_TEST_STATE':str(state),'MCPGUARD_TEST_BINARY':str(base.BUILD/'mcpguard'),'MCPGUARD_TEST_CONNECT_BINARY':str(base.BUILD/'mcpguard-connect')})
    result=subprocess.run([sys.executable,'tests/access_e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    log=state/('access-wire-'+str(__import__('time').time_ns())+'.log');base.dev.write_private(log,result.stdout)
    if result.returncode:raise SystemExit('RA-04 process checks failed; private evidence: '+str(log))
    report=json.loads((state/'access-e2e-results.json').read_text());print(json.dumps({'exitCode':0,'checks':report['passed'],'evidence':str(state/'access-e2e-results.json')}))

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['prepare','test','wire','smoke','contracts']);args=parser.parse_args()
    try:{'prepare':prepare,'test':base.test,'wire':wire,'smoke':smoke,'contracts':base.contracts}[args.command]()
    except subprocess.CalledProcessError:raise SystemExit('RA-04 isolated command failed; original private scene retained, sensitive details withheld.')
