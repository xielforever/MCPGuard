#!/usr/bin/env python3
"""RA-07 server-only package fixtures. Preserve all earlier scenes/history."""
import argparse,base64,hashlib,importlib.util,json,os,secrets,subprocess,sys,time,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('recovery_base',ROOT/'scripts/recovery_fixture.py')
recovery=importlib.util.module_from_spec(spec);spec.loader.exec_module(recovery);base=recovery.base
STATE_ROOT=Path.home()/'.local/state/mcpguard-ra07';STATE_ROOT.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(STATE_ROOT,0o700)
base.BUILD=ROOT/'.dev/ra07-build';base.POINTER=ROOT/'.dev/ra07-latest';base.Path=lambda *parts:STATE_ROOT if parts==('/tmp',) else Path(*parts)
previous_runner=base.runner
def runner(args,state=None,env=None,**kwargs):
    if state and env:
        cfg=json.loads((state/'env.json').read_text())
        env=env|{'MCPGUARD_TEST_CREDENTIAL_DSN':cfg['credentialDsn'],'MCPGUARD_RA_PACKAGE_KEYS':'/fixture/package-keys.json','MCPGUARD_TEST_CATALOG_BINARY':'/work/.dev/ra07-build/mcpguard-catalog'}
    return previous_runner(args,state,env,**kwargs)
def build():
    base.BUILD.mkdir(parents=True,exist_ok=True);commands=['python3 scripts/generate_v02_contracts.py --check']
    for name in ('mcpguard','mcpguard-catalog','mcpguard-connect','mcpguard-demo-http','mcpguard-provision','mcpguard-credentials'):
        commands.append(f'go build -p 2 -o .dev/ra07-build/{name} ./cmd/{name}')
    commands.append('go test -c -o .dev/ra07-build/resources.test ./internal/resources')
    recovery.provision.original_runner(['sh','-c',' && '.join(commands)])
base.build=build;base.runner=runner
def prepare():
    recovery.prepare();state,cfg=base.selected();catalog=json.loads((state/'catalog.json').read_text())
    from urllib.parse import urlsplit
    cfg['credential_password']=secrets.token_hex(24)
    base.admin(cfg,'postgres',f"CREATE ROLE mcpguard_credentials LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg['credential_password']}';GRANT CONNECT ON DATABASE mcpguard_meta TO mcpguard_credentials;")
    address=urlsplit(cfg['MCPGUARD_METADATA_DSN']).netloc.rsplit('@',1)[1]
    cfg['credentialDsn']=f"postgres://mcpguard_credentials:{cfg['credential_password']}@{address}/mcpguard_meta?sslmode=disable"
    key=secrets.token_bytes(32);issuer={'orgId':base.dev.ORG,'issuer':'mcpguard.local.ra07','issuerEpoch':str(uuid.uuid4()),'keyId':'reference.v1','keyFingerprint':hashlib.sha256(key).hexdigest(),'version':'1','enabled':True}
    private={'schemaVersion':'0.2','keys':[{'orgId':issuer['orgId'],'issuer':issuer['issuer'],'keyId':issuer['keyId'],'key':base64.b64encode(key).decode()}]}
    policies=[{k:p[k] for k in ('orgId','resourceId','profileId')}|{'resourceVersion':'1','profileVersion':p['version'],'version':'1','maxTtlSeconds':600,'enabled':True} for p in catalog['profiles']]
    base.dev.write_private(state/'package-keys.json',json.dumps(private));base.dev.write_private(state/'credentials.json',json.dumps({'schemaVersion':'0.2','issuer':issuer,'policies':policies}))
    env=os.environ.copy()|{'MCPGUARD_MIGRATION_DSN':cfg['MCPGUARD_MIGRATION_DSN']}
    for args in (['grant-credential-runtime','mcpguard_credentials'],['grant-credential-status-runtime','mcpguard_app'],['import-credentials',str(state/'credentials.json')],['import-credentials',str(state/'credentials.json')]):
        subprocess.run([str(base.BUILD/'mcpguard-catalog'),*args],env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    cfg['raCredentialPhase']=True;base.dev.write_private(state/'env.json',json.dumps(cfg))
    print(json.dumps({'phase':'RA-07','runId':cfg['runId'],'privateState':str(state),'separatePackageKeys':True,'legacyScenesChanged':False}))
def smoke():
    state,cfg=base.selected();env=base.dev.runtime_env(cfg)
    env|={'MCPGUARD_TEST_STATE':str(state),'MCPGUARD_TEST_BINARY':str(base.BUILD/'mcpguard'),'MCPGUARD_TEST_CONNECT_BINARY':str(base.BUILD/'mcpguard-connect'),'MCPGUARD_TEST_PROVISION_BINARY':str(base.BUILD/'mcpguard-provision'),'MCPGUARD_TEST_CREDENTIAL_BINARY':str(base.BUILD/'mcpguard-credentials')}
    r=subprocess.run([sys.executable,'tests/credential_e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    log=state/('credential-wire-'+str(time.time_ns())+'.log');base.dev.write_private(log,r.stdout)
    if r.returncode:raise SystemExit('RA-07 process verification failed; private evidence: '+str(log))
    report=json.loads((state/'credential-e2e-results.json').read_text());print(json.dumps({'exitCode':0,'checks':report['passed'],'evidence':str(state/'credential-e2e-results.json')}))
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('command',choices=['prepare','test','wire','access-smoke','provision-smoke','recovery-smoke','smoke','contracts']);a=p.parse_args()
    try:{'prepare':prepare,'test':base.test,'wire':base.wire,'access-smoke':recovery.provision.access.smoke,'provision-smoke':recovery.provision.smoke,'recovery-smoke':recovery.smoke,'smoke':smoke,'contracts':base.contracts}[a.command]()
    except subprocess.CalledProcessError as e:raise SystemExit(f'RA-07 isolated command failed (exit {e.returncode}); private failure scene retained.')
