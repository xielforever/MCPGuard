#!/usr/bin/env python3
"""RA-09 private cache component + real gateway, metadata and two native PGs.
These are local component/protocol proofs, not desktop adapter acceptance.
"""
import hashlib,importlib.util,json,os,secrets,signal,subprocess,time,uuid
from datetime import datetime,timedelta,timezone
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('legacy_wire',ROOT/'tests/e2e.py');wire=importlib.util.module_from_spec(spec);spec.loader.exec_module(wire)
CFG=wire.CFG;STATE=wire.dev.STATE;CHECKS=[]
def check(value,label):
    if not value:raise AssertionError(label)
    CHECKS.append(label)
def key():return str(uuid.uuid4())
def scalar(sql):return wire.dev.docker('exec','-i',CFG['container'],'psql','-X','-A','-t','-v','ON_ERROR_STOP=1','-U','postgres','-d','mcpguard_meta',input=sql,capture_output=True,text=True).stdout.strip()

def verify_read_rollout(alice, pending, rpc):
    for index, ticket in enumerate(pending):
        pg = CFG['raPgAdmins'][index]
        resource = str(uuid.UUID(ticket['resourceId']))
        identity = scalar(f"SELECT native_identity FROM mcpguard_ra.native_accounts WHERE resource_id='{resource}' AND subject_id='{wire.dev.IDS['alice']}';")
        check(len(identity) == 35 and identity == 'mg_' + uuid.UUID(hex=identity.removeprefix('mg_')).hex,
              'provisioned resource has a dedicated native account')
        note = f'RA-17 read rollout {index + 1}'
        wire.dev.psql(pg, 'mcpguard_business', f"INSERT INTO resource_demo.items VALUES ('{identity}','item_demo_1','{note}',1);")
        args = {'resourceId': resource, 'credentialId': ticket['credentialId'], 'itemId': 'item_demo_1'}
        result = rpc(alice, 'pg.get_item', args)['structuredContent']
        check(result['requestStatus'] == 'SUCCEEDED' and result['sideEffectStatus'] == 'NONE',
              'protected read completes without a native write')
        data = result['data']
        check(data['nativeIdentity'] == identity and data['items'][0]['note'] == note,
              'credential selects the intended PostgreSQL database and account')
        plan = str(uuid.UUID(data['planId']))
        check(scalar(f"SELECT count(*) FROM mcpguard_ra.read_audit WHERE plan_id='{plan}';") == '2',
              'read intent and result have correlated audit evidence')
        bare = rpc(wire.HTTPMCP(alice.port, 'alice'), 'pg.get_item', args)['structuredContent']
        check(bare['dispatchState'] == 'NOT_DISPATCHED' and bare['error']['code'] == 'ACCESS_DENIED',
              'even the owner needs a private Context activation to dispatch')
        denied = rpc(wire.HTTPMCP(alice.port, 'bob'), 'pg.get_item', args)['structuredContent']
        check(denied['dispatchState'] == 'NOT_DISPATCHED' and denied['error']['code'] == 'ACCESS_DENIED',
              'another user cannot dispatch with the activated credential')

def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}'
    env=wire.dev.runtime_env(CFG)|{'MCPGUARD_LISTEN':f'127.0.0.1:{port}','MCPGUARD_RESOURCE_CATALOG':'enabled','MCPGUARD_RESOURCE_ACCESS':'enabled','MCPGUARD_RESOURCE_CREDENTIALS':'enabled','MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json'),'MCPGUARD_RESOURCE_DOWNLOADS':'enabled','MCPGUARD_DELIVERY_DSN':CFG['deliveryDsn'],'MCPGUARD_DOWNLOAD_TTL_SECONDS':'60','MCPGUARD_DOWNLOAD_MAX_READY':'20','MCPGUARD_RESOURCE_CONTEXTS':'enabled','MCPGUARD_CONTEXT_DSN':CFG['contextDsn'],'MCPGUARD_CONTEXT_TTL_SECONDS':'120','MCPGUARD_CONTEXT_LIFETIME_SECONDS':'300','MCPGUARD_CONTEXT_MAX_ACTIVATIONS':'10','MCPGUARD_SYNC_PAGE_LIMIT':'2'}
    env.pop('MCPGUARD_NATIVE_CONFIG',None)
    read_rollout = os.getenv('MCPGUARD_TEST_READ_ROLLOUT') == '1'
    if read_rollout:
        env.update(MCPGUARD_LEGACY_DEMO='disabled', MCPGUARD_RESOURCE_READS='enabled',
                   MCPGUARD_READ_SECRETS=str(STATE/'ra-secrets.json'))
        env.pop('MCPGUARD_BUSINESS_DSN', None)
    clean={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}
    trusted=clean|{k:CFG[k] for k in ('MCPGUARD_ENCRYPTION_KEY','MCPGUARD_PLAN_KEY')}|{'MCPGUARD_METADATA_DSN':CFG['credentialDsn'],'MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json')}
    worker=trusted|{'MCPGUARD_METADATA_DSN':CFG['provisionerDsn'],'MCPGUARD_PROVIDER_CONFIG':str(STATE/'provider-private.json')}
    private_values=[];safe_outputs=[];model_outputs=[];auths={};profiles={};process=None;grants=[];receipts=[]
    def start():
        with open(STATE/'context-process.log','a') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log)
        def ready():
            if p.poll() is not None:raise AssertionError('Context gateway startup failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop():
        if process and process.poll() is None:process.send_signal(signal.SIGTERM);process.wait(timeout=10)
    def run_server(name,args,override=None):
        r=subprocess.run([os.environ['MCPGUARD_TEST_CREDENTIAL_BINARY'],*args],env=trusted|{'MCPGUARD_TOKEN':CFG[name+'_token']}|(override or {}),capture_output=True,text=True,timeout=25);safe_outputs.append(r.stdout+r.stderr);return r
    def enroll(name,label):
        path=STATE/('context-bootstrap-'+label+'-'+key()+'.json');r=run_server(name,['enroll-client','600',str(path)]);check(r.returncode==0,'trusted server enrolls private client '+label)
        auths[label]=path;profiles[label]=STATE/('context-cache-'+label+'-'+key());data=json.loads(path.read_text());private_values.append(data['deliveryToken']);check('deliveryToken' not in r.stdout,'enrollment prints safe metadata only');return data
    def component(name,label,command,extra=None,override=None):
        args=[os.environ['MCPGUARD_TEST_SESSION_BINARY'],command,'-gateway',origin,'-auth-file',str(auths[label]),'-directory',str(profiles[label]),'-allow-loopback-http']+(extra or [])
        r=subprocess.run(args,env=clean|{'MCPGUARD_TOKEN':CFG[name+'_token']}|(override or {}),capture_output=True,text=True,timeout=58);safe_outputs.append(r.stdout+r.stderr);return r
    def good(name,label,command,extra=None):
        r=component(name,label,command,extra);check(r.returncode==0,'private component '+label+' '+command);return json.loads(r.stdout)
    def cache(label):return json.loads((profiles[label]/'session.json').read_text())
    def proof(label):
        c=cache(label)['context'];private_values.append(c['contextToken']);return c['context']['contextId']+'.'+c['contextToken']
    def headers(label):return {'X-MCPGuard-Credential-Context':proof(label),'X-MCPGuard-Delivery-Token':json.loads(auths[label].read_text())['deliveryToken']}
    def private(label,path,body=None,name='alice',method=None,extra=None):return wire.http(port,path,CFG[name+'_token'],body=body,headers=headers(label)|(extra or {}),method=method or ('POST' if body is not None else 'GET'))
    def rpc(client,name,args):
        out=client.call('tools/call',{'name':name,'arguments':args});model_outputs.append(json.dumps(out));return out
    def prepare(client,grant):
        out=rpc(client,'credential.prepare_download',{'grantId':grant,'requestKey':key()});check(not out.get('isError',False),'context-aware MCP prepares bounded private task');return out['structuredContent']
    def inventory():
        return json.loads(scalar("SELECT jsonb_build_object('accounts',(SELECT count(*) FROM mcpguard_ra.native_accounts WHERE org_id='"+wire.dev.ORG+"'),'jobs',(SELECT count(*) FROM mcpguard_ra.provisioning_jobs WHERE org_id='"+wire.dev.ORG+"'),'grants',(SELECT count(*) FROM mcpguard_ra.access_grant_versions WHERE org_id='"+wire.dev.ORG+"'));"))
    def file_for(label,cid):return profiles[label]/'files'/('mcpguard-'+cid+'.mgcred')
    def record_files(label):
        for f in (profiles[label]/'files').glob('*.mgcred'):
            p=json.loads(f.read_text());private_values.extend(p[k] for k in ('nonce','ciphertext','authenticationTag'))
    try:
        one=enroll('alice','alice');two=enroll('bob','bob');other=enroll('alice','alice-other')
        denied=STATE/('context-enroll-denied-'+key()+'.json');r=run_server('alice',['enroll-client','600',str(denied)],{'MCPGUARD_METADATA_DSN':CFG['contextDsn']});check(r.returncode!=0 and not denied.exists(),'Context runtime cannot enroll clients')
        process=start()
        for name,label in (('alice','alice'),('bob','bob'),('alice','alice-other')):
            result=good(name,label,'init',['-page-size','1','-max-pages','1','-renew-before-seconds','20']);check(result['context']['ownerId']==one['client']['ownerId'] if name=='alice' else result['context']['ownerId']==two['client']['ownerId'],'Context owner comes from authenticated enrollment')
        alice=wire.HTTPMCP(port,'alice');alice.headers['X-MCPGuard-Credential-Context']=proof('alice');bob=wire.HTTPMCP(port,'bob');bob.headers['X-MCPGuard-Credential-Context']=proof('bob')
        names={t['name'] for t in alice.call('tools/list')['tools']}
        check(not {'context.activate','credential.claim','credential.decrypt'}&names,'private Context tools remain outside the model tool catalog')
        check(('pg.get_item' in names) == read_rollout, 'protected read is explicitly enabled')
        if read_rollout:
            check(not {'pg.get_demo_item', 'pg.update_demo_note', 'pg.update_note', 'action.resume'} & names,
                  'read rollout excludes legacy dispatch and controlled writes')
        code,h,body=wire.http(port,'/console/login',body={'username':'visitor','password':CFG['console_secrets']['visitor']},headers={'Origin':origin});check(code==200,'independent human reviewer login');cookie=next(v for k,v in h.items() if k.lower()=='set-cookie').split(';',1)[0];csrf=json.loads(body)['csrf']
        pending=[]
        for index in (1,2):
            request={'resourceId':f'40000000-0000-4000-8000-{index:012d}','profileId':f'50000000-0000-4000-8000-{index:012d}','requestedExpiry':(datetime.now(timezone.utc)+timedelta(minutes=20)).isoformat().replace('+00:00','Z'),'reason':'RA-09 isolated Context account','requestKey':key()}
            applied=rpc(alice,'access.request',request);check(not applied.get('isError',False),'resource application before private cache');submitted=applied['structuredContent']
            decision={'requestHash':submitted['requestHash'],'expectedRevision':submitted['revision'],'decision':'APPROVE','approvedExpiry':submitted['requestedExpiry'],'reason':'human approved individual Context account','requestKey':key()}
            code,_,body=wire.http(port,'/console/api/v2/access-requests/'+submitted['requestId']+'/decisions',body=decision,headers={'Cookie':cookie,'Origin':origin,'X-CSRF-Token':csrf,'If-Match':'"'+submitted['revision']+'"','Idempotency-Key':decision['requestKey']});check(code==200,'human approval stays separate from cache delivery')
            grant=json.loads(body)['grant']['grantId'];grants.append(grant)
            r=subprocess.run([os.environ['MCPGUARD_TEST_PROVISION_BINARY'],'run',submitted['orgId'],grant],env=worker,capture_output=True,text=True,timeout=35);safe_outputs.append(r.stdout+r.stderr);check(r.returncode==0 and json.loads(r.stdout)['state']=='VERIFIED','independent worker verifies real native account')
            task=prepare(alice,grant);pending.append(task);check(task['deliveryMode']=='ENROLLED_CLIENT' and task['clientInstanceId']==one['client']['clientInstanceId'],'download task binds enrolled client')
        initial_inventory=inventory()
        queued=private('alice','/api/v2/credential-downloads?limit=1',method='GET');check(queued[0]==200 and len(json.loads(queued[2])['tickets'])==1 and json.loads(queued[2])['nextAfter'],'signed queue is bounded')
        cursor=json.loads(queued[2])['nextAfter']
        code,_,body=private('alice-other','/api/v2/credential-downloads?limit=1&after='+cursor,method='GET');check(code==403,'cursor cannot cross enrolled clients of same person')
        code,_,body=private('bob','/api/v2/credential-downloads?limit=1',name='bob',method='GET');check(code==200 and not json.loads(body)['tickets'],'Bob queue excludes Alice tasks')
        code,_,body=private('alice-other','/api/v2/credential-downloads?limit=1',method='GET');check(code==200 and not json.loads(body)['tickets'],'second Alice client excludes first client tasks')
        did=pending[0]['downloadId'];code,_,_=wire.http(port,'/api/v2/credential-downloads/'+did+'/claim',CFG['alice_token'],body={},headers={'X-MCPGuard-Delivery-Token':one['deliveryToken']});check(code==403,'bound task cannot fall back to manual claim')
        code,_,_=private('alice-other','/api/v2/credential-downloads/'+did+'/claim',body={});check(code==403,'same person cannot claim another enrolled client task')
        for extra in ({'Cookie':cookie},{'Origin':origin}):
            code,_,_=private('alice','/api/v2/credential-downloads?limit=1',method='GET',extra=extra);check(code==403,'private queue rejects browser authority')
        code,_,_=private('alice','/api/v2/credential-downloads?limit=3',method='GET');check(code==400,'client cannot exceed server queue page bound')
        saved=good('alice','alice','sync');check(saved['pages']==1 and saved['tasks']==1 and saved['more'],'one sync invocation honors one-page profile bound')
        saved=good('alice','alice','sync');check(len([a for a in saved['activations'] if a['state']=='ACTIVE'])==2,'one Context activates two resource identities')
        if read_rollout:
            verify_read_rollout(alice, pending, rpc)
        again=good('alice','alice','sync');check(again['tasks']==0 and again['saved']==0,'immutable receipts exclude completed downloads')
        check(inventory()==initial_inventory,'bounded sync creates no additional accounts jobs or grants');record_files('alice')
        packet=json.loads(file_for('alice',pending[0]['credentialId']).read_text());payload={'credentialId':pending[0]['credentialId'],'resourceId':pending[0]['resourceId'],'credentialPackage':packet,'requestKey':key()}
        bob_id=cache('bob')['context']['context']['contextId'];code,_,_=private('bob','/api/v2/credential-contexts/'+bob_id+'/activations',name='bob',body=payload);check(code==403,'copied encrypted file cannot activate as another authenticated person')
        copied=cache('alice');own=cache('bob');wire.dev.write_private(profiles['bob']/'copied-alice-cache.json',json.dumps(copied));check(component('bob','alice','status').returncode!=0,'Bob cannot open Alice profile even with copied paths')
        code,_,_=wire.http(port,'/api/v2/credential-contexts/'+copied['context']['context']['contextId'],CFG['bob_token'],headers={'X-MCPGuard-Credential-Context':proof('alice')},method='GET');check(code==403,'copied Context token cannot cross authenticated people')
        old_context=copied['context']['context']['contextId'];good('alice','alice','renew-context');check(cache('alice')['context']['context']['contextId']==old_context,'Context renewal preserves identity')
        old_packet=pending[0]['credentialId'];renewed=good('alice','alice','renew-credential',['-credential-id',old_packet]);check(renewed['credentialId']!=old_packet and renewed['leaseId']!=pending[0]['leaseId'],'credential renewal appends package and lease')
        code,_,lost=private('alice','/api/v2/credential-downloads/'+renewed['downloadId']+'/claim',body={});check(code==200 and not file_for('alice',renewed['credentialId']).exists(),'lost private response leaves server claim consumed and local file absent')
        recovered=good('alice','alice','sync');check(recovered['replacements']==1,'bounded sync reconciles unknown consumed task through replacement')
        recovered=good('alice','alice','sync');check(file_for('alice',renewed['credentialId']).read_bytes()==lost,'replacement recovers exact same encrypted package')
        check(inventory()==initial_inventory,'renewal and unknown-response reconciliation create no native account or grant');record_files('alice')
        before=cache('alice');cid=renewed['credentialId'];path=file_for('alice',cid);original=path.read_bytes();path.unlink()
        recovered=good('alice','alice','sync');check(path.read_bytes()==original and recovered['replacements']>=1,'missing cached file is re-delivered through current Context without new account')
        second=pending[1]['credentialId'];path2=file_for('alice',second);original2=path2.read_bytes();wire.dev.write_private(path2,'{"corrupt":true}')
        unavailable=good('alice','alice','sync');check(any(a['credentialId']==second and a['state']=='REVOKED' for a in unavailable['activations']) and path2.read_text()=='{"corrupt":true}','corrupt local file closes that Context activation and is not overwritten')
        wire.dev.write_private(path2,original2.decode());restored=good('alice','alice','sync');check(any(a['credentialId']==second and a['state']=='ACTIVE' for a in restored['activations']),'valid restored file requires fresh server validation')
        stop();process=start();alice=wire.HTTPMCP(port,'alice');alice.headers['X-MCPGuard-Credential-Context']=proof('alice');status=good('alice','alice','sync');check(status['tasks']==0 and len([a for a in status['activations'] if a['state']=='ACTIVE'])==2,'gateway and component restart preserve scope and completed queue')
        oldproof=proof('alice');good('alice','alice','init',['-replace-context']);rebuilt=good('alice','alice','status');check(not rebuilt['activations'] and rebuilt['context']['contextId']!=old_context,'explicit new Context does not inherit activations')
        alice.headers['X-MCPGuard-Credential-Context']=oldproof;bad=rpc(alice,'credential.prepare_download',{'grantId':grants[0],'requestKey':key()});check(bad.get('isError',False),'replaced Context cannot prepare tasks')
        alice.headers['X-MCPGuard-Credential-Context']='';bad=rpc(alice,'credential.prepare_download',{'grantId':grants[0],'requestKey':key()});check(bad.get('isError',False),'empty supplied Context never falls back to manual preparation')
        alive=good('alice','alice','sync');check(len([a for a in alive['activations'] if a['state']=='ACTIVE'])==2,'explicit sync resubmits selected local files against current authorization')
        r=run_server('alice',['revoke',cid,'1',key(),'RA-09 revoked private reference']);check(r.returncode==0,'server revokes renewed file lease')
        good('alice','alice','renew-context');denied=component('alice','alice','renew-credential',['-credential-id',cid]);check(denied.returncode!=0,'Context renewal does not permit revoked file renewal')
        state=good('alice','alice','sync');check(any(a['credentialId']==cid and a['state']=='REVOKED' for a in state['activations']) and any(a['credentialId']==second and a['state']=='ACTIVE' for a in state['activations']),'one revoked file cannot revive or invalidate another resource')
        good('alice','alice','init',['-replace-context']);state=good('alice','alice','sync');check(not any(a['credentialId']==cid and a['state']=='ACTIVE' for a in state['activations']) and any(a['credentialId']==second and a['state']=='ACTIVE' for a in state['activations']),'new Context and retained revoked file cannot restore revoked access')
        check(inventory()==initial_inventory,'all retickets restarts context replacements and revocations preserve native inventory')
        # Independently expire a Context while leaving the employee and file valid.
        stop();env['MCPGUARD_CONTEXT_TTL_SECONDS']='1';env['MCPGUARD_CONTEXT_LIFETIME_SECONDS']='2';process=start();enroll('alice','short');good('alice','short','init',['-renew-before-seconds','0']);time.sleep(1.1)
        short=good('alice','short','status');check(short['context']['state']=='EXPIRED','expired Context is persisted terminal')
        check(component('alice','short','renew-context').returncode!=0 and component('alice','short','sync').returncode!=0,'expired Context cannot auto-renew or sync')
        stop();env['MCPGUARD_CONTEXT_TTL_SECONDS']='120';env['MCPGUARD_CONTEXT_LIFETIME_SECONDS']='300';process=start();good('alice','short','init',['-replace-context']);check(not good('alice','short','status')['activations'],'rebuilding expired Context starts with zero activations')
        # Rotate a separate employee credential. No existing user's token or
        # permissions are changed, and the old credential is never restored.
        person=key();old=secrets.token_urlsafe(32);new=secrets.token_urlsafe(32);CFG['rotated_token']=old;private_values.extend([old,new]);wire.dev.write_private(STATE/'context-rotation-private.json',json.dumps({'subjectId':person,'old':old,'new':new}))
        scalar("INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,enabled,can_read,scope_id) VALUES('"+wire.dev.ORG+"','"+person+"','RA09 isolated rotation',decode('"+hashlib.sha256(old.encode()).hexdigest()+"','hex'),clock_timestamp()+interval '20 minutes',true,false,'tenant_a');")
        enroll('rotated','rotated');good('rotated','rotated','init');oldctx=cache('rotated')['context']['context']['contextId'];oldheader=proof('rotated')
        scalar("UPDATE mcpguard.principals SET token_hash=decode('"+hashlib.sha256(new.encode()).hexdigest()+"','hex') WHERE org_id='"+wire.dev.ORG+"' AND id='"+person+"';")
        check(component('rotated','rotated','sync').returncode!=0,'replaced primary token cannot sync cached Context')
        code,_,body=wire.http(port,'/api/v2/credential-contexts/'+oldctx,new,headers={'X-MCPGuard-Credential-Context':oldheader},method='GET');check(code==200 and json.loads(body)['context']['state']=='REVOKED','new primary token cannot revive Context bound to old credential')
        CFG['rotated_token']=new;check(component('rotated','rotated','init',['-replace-context']).returncode!=0,'changed primary credential cannot silently repurpose pinned cache')
        enroll('rotated','rotated-new');good('rotated','rotated-new','init');check(not good('rotated','rotated-new','status')['activations'],'fresh enrollment after rotation carries no previous authority')
        # Cache writers are exclusive across real component processes.
        lockproc=subprocess.Popen(['flock','--no-fork','-x',str(profiles['alice']/'.mcpguard-cache.lock'),'sleep','4'])
        try:time.sleep(.2);check(component('alice','alice','status').returncode!=0,'second process cannot read or mutate a locked cache profile')
        finally:lockproc.terminate();lockproc.wait(timeout=4)
        check(component('alice','alice','status').returncode==0,'process lock releases after writer exits')
        for label in profiles:
            if (profiles[label]/'session.json').exists():proof(label);record_files(label)
        text='\n'.join(safe_outputs+model_outputs)+(STATE/'context-process.log').read_text()
        audit=scalar("SELECT jsonb_agg(to_jsonb(t))::text FROM mcpguard_ra.context_audit t;")
        check(all(v not in text+audit for v in private_values),'MCP outputs component receipts server logs and audit exclude private tokens and packet bytes')
        check('CONTEXT_CREATED' in audit and 'TASK_REPLACED' in audit and 'CREDENTIAL_RENEWED' in audit,'typed audit records Context renewal task reconciliation and private package renewal')
        receipts=[{'resourceId':t['resourceId'],'credentialId':t['credentialId'],'leaseId':t['leaseId']} for t in pending]
    finally:stop()
    report={'phase':'RA-17' if read_rollout else 'RA-09','passed':len(CHECKS),'checks':CHECKS,'receipts':receipts,'nativeInventory':initial_inventory,'boundedLocalComponent':True,'realDesktopNativeClient':False,'formalNewResourceReadTool':read_rollout,'sameOSUserArbitraryFileAccessIsolationClaimed':False,'existingServiceChanged':False}
    wire.dev.write_private(STATE/'context-e2e-results.json',json.dumps(report,indent=2)+'\n');print('RA-09 local Context verification passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
