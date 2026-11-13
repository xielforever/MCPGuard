#!/usr/bin/env python3
"""RA-07 server operations + real HTTP/stdio status, never a download proof."""
import importlib.util,json,os,signal,subprocess,uuid
from datetime import datetime,timedelta,timezone
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('legacy_wire',ROOT/'tests/e2e.py')
wire=importlib.util.module_from_spec(spec);spec.loader.exec_module(wire)
CFG=wire.CFG;STATE=wire.dev.STATE;CHECKS=[]
def check(value,label):
    if not value:raise AssertionError(label)
    CHECKS.append(label)
def key():return str(uuid.uuid4())
def scalar(sql):
    return wire.dev.docker('exec','-i',CFG['container'],'psql','-X','-A','-t','-v','ON_ERROR_STOP=1','-U','postgres','-d','mcpguard_meta',input=sql,capture_output=True,text=True).stdout.strip()

def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}'
    env=wire.dev.runtime_env(CFG)|{'MCPGUARD_LISTEN':f'127.0.0.1:{port}','MCPGUARD_RESOURCE_CATALOG':'enabled','MCPGUARD_RESOURCE_ACCESS':'enabled','MCPGUARD_RESOURCE_CREDENTIALS':'enabled','MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json')}
    env.pop('MCPGUARD_NATIVE_CONFIG',None)
    private_keys=json.loads((STATE/'package-keys.json').read_text())
    forbidden=[CFG['credential_password'],CFG['provisioner_password'],CFG['MCPGUARD_ENCRYPTION_KEY'],CFG['MCPGUARD_PLAN_KEY'],*[k['key'] for k in private_keys['keys']]]
    server_env={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}
    server_env|={k:CFG[k] for k in ('MCPGUARD_ENCRYPTION_KEY','MCPGUARD_PLAN_KEY')}
    server_env|={'MCPGUARD_METADATA_DSN':CFG['credentialDsn'],'MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json')}
    worker_env=server_env|{'MCPGUARD_METADATA_DSN':CFG['provisionerDsn'],'MCPGUARD_PROVIDER_CONFIG':str(STATE/'provider-private.json')}
    process=None;bridge=None;receipts=[]
    def start():
        with open(STATE/'credential-process.log','a') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log)
        def ready():
            if p.poll() is not None:raise AssertionError('credential gateway startup failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop():
        if process and process.poll() is None:process.send_signal(signal.SIGTERM);process.wait(timeout=10)
    def operation(name,args,override=None):
        r=subprocess.run([os.environ['MCPGUARD_TEST_CREDENTIAL_BINARY'],*args],cwd=ROOT,env=server_env|{'MCPGUARD_TOKEN':CFG[name+'_token']}|(override or {}),capture_output=True,text=True,timeout=25)
        text=r.stdout+r.stderr
        check(all(v not in text for v in forbidden),'server operation output excludes keys and database secrets')
        check(not any(v in text for v in ('ciphertext','authenticationTag','issuerEpoch','secretRef','nativeIdentity','postgres://')),'server operation returns only safe status')
        return r,json.loads(r.stdout) if r.stdout.strip() else None
    def status(client,cid):return client.call('tools/call',{'name':'credential.status','arguments':{'credentialId':cid}})
    def approve(index,alice,cookie,csrf):
        request={'resourceId':f'40000000-0000-4000-8000-{index:012d}','profileId':f'50000000-0000-4000-8000-{index:012d}','requestedExpiry':(datetime.now(timezone.utc)+timedelta(minutes=20)).isoformat().replace('+00:00','Z'),'reason':'RA-07 isolated server reference proof','requestKey':key()}
        submitted=alice.call('tools/call',{'name':'access.request','arguments':request})['structuredContent']
        decision={'requestHash':submitted['requestHash'],'expectedRevision':submitted['revision'],'decision':'APPROVE','approvedExpiry':submitted['requestedExpiry'],'reason':'human approved bounded reference fixture','requestKey':key()}
        h={'Cookie':cookie,'Origin':origin,'X-CSRF-Token':csrf,'If-Match':'"'+submitted['revision']+'"','Idempotency-Key':decision['requestKey']}
        code,_,body=wire.http(port,'/console/api/v2/access-requests/'+submitted['requestId']+'/decisions',body=decision,headers=h)
        result=json.loads(body);check(code==200 and result['grant']['state']=='PROVISIONING','human approval still requires actual native provisioning')
        return result
    try:
        process=start();alice=wire.HTTPMCP(port,'alice');bob=wire.HTTPMCP(port,'bob');bridge=wire.StdioMCP(port,'alice')
        names={t['name'] for t in alice.call('tools/list')['tools']}
        check({n for n in names if n.startswith('credential.')}=={'credential.status'},'only credential.status is published in RA-07')
        check(not {'credential.decrypt','credential.issue','credential.prepare_download','credential.revoke','pg.get_item','context.activate'}&names,'future download/context/execution and privileged operations remain unpublished')
        code,headers,body=wire.http(port,'/console/login',body={'username':'visitor','password':CFG['console_secrets']['visitor']},headers={'Origin':origin})
        check(code==200,'independent human reviewer session')
        cookie=next(v for k,v in headers.items() if k.lower()=='set-cookie').split(';',1)[0];csrf=json.loads(body)['csrf']
        for index in (1,2):
            approved=approve(index,alice,cookie,csrf);grant=approved['grant'];req_key=key()
            denied,_=operation('alice',['issue',grant['grantId'],req_key]);check(denied.returncode!=0,'PROVISIONING grant cannot issue a reference')
            r=subprocess.run([os.environ['MCPGUARD_TEST_PROVISION_BINARY'],'run',grant['orgId'],grant['grantId']],env=worker_env,capture_output=True,text=True,timeout=35)
            check(r.returncode==0 and json.loads(r.stdout)['state']=='VERIFIED','separate native worker verifies actual PG account')
            r,issued=operation('alice',['issue',grant['grantId'],req_key]);check(r.returncode==0 and issued['state']=='ACTIVE' and issued['revision']=='1','server commits package and bounded active lease')
            r,replay=operation('alice',['issue',grant['grantId'],req_key]);check(r.returncode==0 and replay==issued,'another process replays one issued package')
            r,_=operation('bob',['issue',grant['grantId'],key()]);check(r.returncode!=0,'another user cannot issue from copied grant id')
            cid=issued['credentialId'];rid=grant['resourceId']
            # Administrative test extraction stays in the private server fixture.
            # No client-facing download route or connector file support exists.
            raw=scalar(f"SELECT encode(envelope,'hex') FROM mcpguard_ra.credential_blobs WHERE credential_id='{uuid.UUID(cid)}';")
            blob=bytes.fromhex(raw.strip());path=STATE/('server-reference-'+cid+'.json');wire.dev.write_private(path,blob.decode())
            check(path.stat().st_mode&0o077==0,'test-only envelope extraction is server-private')
            r,validated=operation('alice',['check',rid,cid,str(path)]);check(r.returncode==0 and validated==issued,'separate server process validates bound reference')
            r,_=operation('bob',['check',rid,cid,str(path)]);check(r.returncode!=0,'copied encrypted bytes do not authenticate another user')
            wrong=f'40000000-0000-4000-8000-{3-index:012d}';r,_=operation('alice',['check',wrong,cid,str(path)]);check(r.returncode!=0,'package cannot select another PG resource')
            broken=STATE/('tampered-reference-'+cid+'.json');packet=json.loads(blob);packet['authenticationTag']=('A' if packet['authenticationTag'][0]!='A' else 'B')+packet['authenticationTag'][1:];wire.dev.write_private(broken,json.dumps(packet,separators=(',',':')))
            r,_=operation('alice',['check',rid,cid,str(broken)]);check(r.returncode!=0,'tampered authenticated package is rejected')
            check(status(alice,cid)['structuredContent']==issued,'HTTP MCP status agrees with server receipt')
            check(status(bridge,cid)['structuredContent']==issued,'stdio status agrees without envelope bytes')
            check(status(bob,cid).get('isError',False),'MCP status hides another user credential')
            code,h,body=wire.http(port,'/api/v2/credentials/'+cid,CFG['alice_token'],method='GET');check(code==200 and json.loads(body)==issued and h.get('Cache-Control')=='no-store','authenticated HTTP status is non-cacheable and identical')
            code,_,_=wire.http(port,'/api/v2/credentials/'+cid,CFG['bob_token'],method='GET');check(code in (403,404),'HTTP status enforces current owner')
            code,_,_=wire.http(port,'/api/v2/credentials/'+cid,method='GET');check(code==401,'HTTP status requires authentication')
            for suffix in ('/blob','/decrypt','/download'):
                code,_,_=wire.http(port,'/api/v2/credentials/'+cid+suffix,CFG['alice_token'],method='GET');check(code==404,'no public '+suffix[1:]+' endpoint')
            r,_=operation('alice',['issue',grant['grantId'],key()],{'MCPGUARD_METADATA_DSN':CFG['MCPGUARD_METADATA_DSN']});check(r.returncode!=0,'gateway database identity cannot issue packages')
            receipts.append({'resourceId':rid,'status':issued,'privatePath':str(path)})
        bridge.close();bridge=None;stop();process=start();alice=wire.HTTPMCP(port,'alice')
        for receipt in receipts:check(status(alice,receipt['status']['credentialId'])['structuredContent']==receipt['status'],'gateway restart preserves authoritative package status')
        first=receipts[0];cid=first['status']['credentialId'];rev_key=key();args=['revoke',cid,'1',rev_key,'RA-07 server-side file revocation']
        r,closed=operation('alice',args);check(r.returncode==0 and closed['state']=='REVOKED' and closed['revision']=='2','server revocation atomically closes one lease')
        r,replay=operation('alice',args);check(r.returncode==0 and replay==closed,'revocation is idempotent across server processes')
        r,_=operation('alice',['check',first['resourceId'],cid,first['privatePath']]);check(r.returncode!=0,'retained encrypted file fails after revocation')
        check(status(alice,cid)['structuredContent']==closed,'gateway immediately observes independent process revocation')
        second=receipts[1];r,checked=operation('alice',['check',second['resourceId'],second['status']['credentialId'],second['privatePath']]);check(r.returncode==0 and checked==second['status'],'other resource package remains valid')
        stop();process=start();alice=wire.HTTPMCP(port,'alice');check(status(alice,cid)['structuredContent']==closed,'restart cannot revive revoked lease')
        # Re-running earlier role grants must not expose new crypto storage.
        catalog=os.environ['MCPGUARD_TEST_BINARY'].rsplit('/',1)[0]+'/mcpguard-catalog'
        probe_role='credential_legacy_probe_'+key().replace('-','')[:10]
        wire.dev.psql(CFG,'mcpguard_meta','CREATE ROLE '+probe_role+' NOLOGIN;')
        for command in ('grant-runtime','grant-access-runtime','grant-provision-runtime'):
            r=subprocess.run([catalog,command,probe_role],env=os.environ|{'MCPGUARD_MIGRATION_DSN':CFG['MCPGUARD_MIGRATION_DSN']},capture_output=True,text=True,timeout=20);check(r.returncode==0,'legacy grant command remains compatible')
        facts=scalar("SELECT has_table_privilege('"+probe_role+"','mcpguard_ra.credential_blobs','SELECT') OR has_table_privilege('"+probe_role+"','mcpguard_ra.credential_nonce_reservations','SELECT');")
        check(facts.strip()=='f','legacy grant reruns cannot reveal package bytes or nonce reservations')
    finally:
        if bridge:bridge.close()
        stop()
    report={'phase':'RA-07','passed':len(CHECKS),'checks':CHECKS,'realDesktopClient':False,'actualNativeProvisioning':True,'clientDownloadImplemented':False,'existingServiceChanged':False,'receipts':[{k:v for k,v in r.items() if k!='privatePath'} for r in receipts]}
    wire.dev.write_private(STATE/'credential-e2e-results.json',json.dumps(report,indent=2)+'\n')
    print('RA-07 server/process verification passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
