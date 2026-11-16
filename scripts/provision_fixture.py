#!/usr/bin/env python3
"""RA-05 owned fixtures; preserve all previous scenes and public evidence."""
import argparse,base64,hashlib,importlib.util,json,os,secrets,subprocess,sys,time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('access_base',ROOT/'scripts/access_fixture.py')
access=importlib.util.module_from_spec(spec);spec.loader.exec_module(access);base=access.base
STATE_ROOT=Path.home()/'.local/state/mcpguard-ra05';STATE_ROOT.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(STATE_ROOT,0o700)
base.BUILD=ROOT/'.dev/ra05-build';base.POINTER=ROOT/'.dev/ra05-latest';base.Path=lambda *parts:STATE_ROOT if parts==('/tmp',) else Path(*parts)
def build():
    base.BUILD.mkdir(parents=True,exist_ok=True)
    commands=['python3 scripts/generate_v02_contracts.py --check']
    for name in ('mcpguard','mcpguard-catalog','mcpguard-connect','mcpguard-demo-http','mcpguard-provision'):
        commands.append(f'go build -p 2 -o .dev/ra05-build/{name} ./cmd/{name}')
    original_runner(['sh','-c',' && '.join(commands)])
original_runner=base.runner
def runner(args,state=None,env=None,**kwargs):
    if state and env:
        cfg=json.loads((state/'env.json').read_text())
        env=env|{'MCPGUARD_RA_PROVISION_CONFIG':'/fixture/provider-private.json','MCPGUARD_TEST_PROVISIONER_DSN':cfg['provisionerDsn']}
    return original_runner(args,state,env,**kwargs)
base.build=build;base.runner=runner
def prepare():
    access.prepare();state,cfg=base.selected();catalog=json.loads((state/'catalog.json').read_text())
    from urllib.parse import urlsplit,urlunsplit
    cfg['provisioner_password']=secrets.token_hex(24)
    base.admin(cfg,'postgres',f"CREATE ROLE mcpguard_provisioner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg['provisioner_password']}';GRANT CONNECT ON DATABASE mcpguard_meta TO mcpguard_provisioner;")
    address=urlsplit(cfg['MCPGUARD_METADATA_DSN']).netloc.rsplit('@',1)[1]
    cfg['provisionerDsn']=f"postgres://mcpguard_provisioner:{cfg['provisioner_password']}@{address}/mcpguard_meta?sslmode=disable"
    public={'schemaVersion':'0.2','targets':[],'existingAccounts':[]}
    native=json.loads((state/'ra-secrets.json').read_text());private={'endpoints':native['endpoints'],'managers':{},'existingSecrets':{}}
    admin_env=os.environ.copy();admin_env['MCPGUARD_MIGRATION_DSN']=cfg['MCPGUARD_MIGRATION_DSN']
    subprocess.run([str(base.BUILD/'mcpguard-catalog'),'grant-provision-runtime','mcpguard_provisioner'],env=admin_env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    for index,(resource,profile,pg) in enumerate(zip(catalog['resources'],catalog['profiles'],cfg['raPgAdmins']),1):
        password=secrets.token_hex(24)
        base.admin({'container':pg['container']},'postgres',f"CREATE ROLE mcpguard_account_manager LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{password}';GRANT CONNECT ON DATABASE mcpguard_business TO mcpguard_account_manager;")
        parts=urlsplit(pg['dsn']);address=parts.netloc.rsplit('@',1)[1]
        private['managers'][resource['endpointConfigRef']]=f"postgres://mcpguard_account_manager:{password}@{address}/mcpguard_business?sslmode=disable"
        target={k:resource[k] for k in ('orgId','resourceId','endpointConfigRef','targetFingerprint')}
        target|={'resourceVersion':resource['version'],'profileId':profile['profileId'],'profileVersion':profile['version'],'targetId':f'90000000-0000-4000-8000-{index:012d}','template':'PG_OWNER_SELECT_V1','version':'1','enabled':True}
        public['targets'].append(target)
        install={'schemaVersion':'0.2','targets':[target],'existingAccounts':[]}
        base.dev.write_private(state/f'pg-provider-{index}.json',json.dumps(install))
        env=os.environ.copy()|{'MCPGUARD_PROVIDER_ADMIN_DSN':pg['dsn'],'MCPGUARD_PROVIDER_MANAGER_ROLE':'mcpguard_account_manager'}
        for _ in range(2):subprocess.run([str(base.BUILD/'mcpguard-provision'),'install-provider',str(state/f'pg-provider-{index}.json')],env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    if os.environ.get('MCPGUARD_FIXTURE_WRITE')=='1':
        write_profiles=[p for p in catalog['profiles'] if p.get('name')=='isolated-write']
        for index,profile in enumerate(write_profiles,1):
            resource=next(r for r in catalog['resources'] if r['resourceId']==profile['resourceId'])
            pg=cfg['raPgAdmins'][index-1]
            target={k:resource[k] for k in ('orgId','resourceId','endpointConfigRef','targetFingerprint')}
            target|={'resourceVersion':resource['version'],'profileId':profile['profileId'],'profileVersion':profile['version'],'targetId':f'91000000-0000-4000-8000-{index:012d}','template':'PG_OWNER_UPDATE_V1','version':'1','enabled':True}
            public['targets'].append(target)
            install={'schemaVersion':'0.2','targets':[target],'existingAccounts':[]}
            base.dev.write_private(state/f'pg-write-provider-{index}.json',json.dumps(install))
            env=os.environ.copy()|{'MCPGUARD_PROVIDER_ADMIN_DSN':pg['dsn'],'MCPGUARD_PROVIDER_MANAGER_ROLE':'mcpguard_account_manager'}
            for _ in range(2):subprocess.run([str(base.BUILD/'mcpguard-provision'),'install-provider',str(state/f'pg-write-provider-{index}.json')],env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    base.dev.write_private(state/'provisioning.json',json.dumps(public));base.dev.write_private(state/'provider-private.json',json.dumps(private))
    for _ in range(2):subprocess.run([str(base.BUILD/'mcpguard-catalog'),'import-provisioning',str(state/'provisioning.json')],env=admin_env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    cfg['raProvisionPhase']=True;base.dev.write_private(state/'env.json',json.dumps(cfg))
    print(json.dumps({'phase':'RA-05','runId':cfg['runId'],'targets':len(public['targets']),'separateManagementIdentities':True,'privateState':str(state),'existingServiceChanged':False}))
def smoke():
    state,cfg=base.selected();env=base.dev.runtime_env(cfg)
    env|={'MCPGUARD_TEST_STATE':str(state),'MCPGUARD_TEST_BINARY':str(base.BUILD/'mcpguard'),'MCPGUARD_TEST_CONNECT_BINARY':str(base.BUILD/'mcpguard-connect'),'MCPGUARD_TEST_PROVISION_BINARY':str(base.BUILD/'mcpguard-provision')}
    result=subprocess.run([sys.executable,'tests/provision_e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    log=state/('provision-wire-'+str(time.time_ns())+'.log');base.dev.write_private(log,result.stdout)
    if result.returncode:raise SystemExit('RA-05 process verification failed; private evidence: '+str(log))
    report=json.loads((state/'provision-e2e-results.json').read_text());print(json.dumps({'exitCode':0,'checks':report['passed'],'evidence':str(state/'provision-e2e-results.json')}))
if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['prepare','test','wire','access-smoke','smoke','contracts']);args=parser.parse_args()
    try:{'prepare':prepare,'test':base.test,'wire':base.wire,'access-smoke':access.smoke,'smoke':smoke,'contracts':base.contracts}[args.command]()
    except subprocess.CalledProcessError as error:raise SystemExit(f'RA-05 isolated command failed (exit {error.returncode}); sensitive output retained privately, original scenes preserved.')
