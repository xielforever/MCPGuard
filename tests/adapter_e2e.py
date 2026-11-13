#!/usr/bin/env python3
"""Independent native-HTTP host-hook and real stdio subprocess verification.
Private host-header bytes are never copied to this script's public output.
"""
import importlib.util,json,os,queue,signal,subprocess,threading,time,uuid
from datetime import datetime,timedelta,timezone
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('wire',ROOT/'tests/e2e.py');wire=importlib.util.module_from_spec(spec);spec.loader.exec_module(wire)
CFG=wire.CFG;STATE=wire.dev.STATE;CHECKS=[];CAP='io.mcpguard/private-client'
def check(v,label):
    if not v:raise AssertionError(label)
    CHECKS.append(label)
def key():return str(uuid.uuid4())
def scalar(sql):return wire.dev.docker('exec','-i',CFG['container'],'psql','-X','-A','-t','-v','ON_ERROR_STOP=1','-U','postgres','-d','mcpguard_meta',input=sql,capture_output=True,text=True).stdout.strip()
class Native(wire.HTTPMCP):
    def __init__(self,port,private):
        self.port=port;self.token=None;self.headers=private|{'MCP-Protocol-Version':wire.PROFILE}
        code,h,body=wire.http(port,'/mcp',body={'jsonrpc':'2.0','id':'init','method':'initialize','params':{'protocolVersion':wire.PROFILE,'capabilities':{},'clientInfo':{'name':'private-native-host-fixture','version':'1'}}},headers=self.headers)
        check(code==200,'native HTTP initialize uses private host headers');data=json.loads(body)['result'];check(data['protocolVersion']==wire.PROFILE,'native HTTP profile remains explicit')
        self.headers['Mcp-Session-Id']=next(v for k,v in h.items() if k.lower()=='mcp-session-id')
        check(wire.http(port,'/mcp',body={'jsonrpc':'2.0','method':'notifications/initialized'},headers=self.headers)[0]==202,'native HTTP initialized')
class Stdio(wire.StdioMCP):
    def __init__(self,endpoint,token,auth,profile):
        env={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}|{'MCPGUARD_TOKEN':token}
        self.p=subprocess.Popen([os.environ['MCPGUARD_TEST_CONNECT_BINARY'],'-gateway',endpoint,'-allow-loopback-http','-private-auth-file',str(auth),'-private-directory',str(profile)],cwd=ROOT,env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,bufsize=1)
        self.q,self.pending,self.lines=queue.Queue(),{},[];self.thread=threading.Thread(target=self._reader,daemon=True);self.thread.start();self.send('initialize',{'protocolVersion':wire.PROFILE,'capabilities':{},'clientInfo':{'name':'stdio-private-host-fixture','version':'1'}},'init');result=self.receive('init',timeout=20)['result'];self.capability=result['capabilities']['experimental'][CAP];self.send('notifications/initialized',{})
        check(self.capability['entry']=='STDIO_PRIVATE' and self.capability['headerRefresh']=='PER_REMOTE_REQUEST','stdio initialize declares only implemented private capability')
    def raw(self,method,params=None):
        ident=key();self.send(method,params or {},ident);return self.receive(ident,timeout=20)
    def finish(self):
        self.p.stdin.close()
        try:self.p.wait(timeout=8)
        except subprocess.TimeoutExpired:self.p.kill();self.p.wait();raise AssertionError('private stdio did not exit on EOF')
        self.thread.join(timeout=1);check(self.p.returncode==0,'private stdio exits on EOF');check(all(json.loads(x).get('jsonrpc')=='2.0' for x in self.lines),'private stdio stdout contains MCP frames only');return ''.join(self.lines)+self.p.stderr.read()
def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}';endpoint=origin+'/mcp'
    env=wire.dev.runtime_env(CFG)|{'MCPGUARD_LISTEN':f'127.0.0.1:{port}','MCPGUARD_RESOURCE_CATALOG':'enabled','MCPGUARD_RESOURCE_ACCESS':'enabled','MCPGUARD_RESOURCE_CREDENTIALS':'enabled','MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json'),'MCPGUARD_RESOURCE_DOWNLOADS':'enabled','MCPGUARD_DELIVERY_DSN':CFG['deliveryDsn'],'MCPGUARD_DOWNLOAD_TTL_SECONDS':'60','MCPGUARD_DOWNLOAD_MAX_READY':'30','MCPGUARD_RESOURCE_CONTEXTS':'enabled','MCPGUARD_CONTEXT_DSN':CFG['contextDsn'],'MCPGUARD_CONTEXT_TTL_SECONDS':'120','MCPGUARD_CONTEXT_LIFETIME_SECONDS':'300','MCPGUARD_CONTEXT_MAX_ACTIVATIONS':'10','MCPGUARD_SYNC_PAGE_LIMIT':'20'}
    env.pop('MCPGUARD_NATIVE_CONFIG',None);clean={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}
    trusted=clean|{k:CFG[k] for k in ('MCPGUARD_ENCRYPTION_KEY','MCPGUARD_PLAN_KEY')}|{'MCPGUARD_METADATA_DSN':CFG['credentialDsn'],'MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json')};worker=trusted|{'MCPGUARD_METADATA_DSN':CFG['provisionerDsn'],'MCPGUARD_PROVIDER_CONFIG':str(STATE/'provider-private.json')}
    auths={};profiles={};owners={};private_values=[CFG['alice_token'],CFG['bob_token']];safe_outputs=[];model_outputs=[];bridges=[];workers=[];process=None;grants=[];tasks=[]
    def start():
        with open(STATE/'adapter-process.log','a') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log)
        def ready():
            if p.poll() is not None:raise AssertionError('adapter gateway failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop():
        if process and process.poll() is None:process.send_signal(signal.SIGTERM);process.wait(timeout=10)
    def enroll(label,name='alice'):
        path=STATE/('adapter-bootstrap-'+label+'-'+key()+'.json');r=subprocess.run([os.environ['MCPGUARD_TEST_CREDENTIAL_BINARY'],'enroll-client','600',str(path)],env=trusted|{'MCPGUARD_TOKEN':CFG[name+'_token']},capture_output=True,text=True,timeout=25);safe_outputs.append(r.stdout+r.stderr);check(r.returncode==0,'trusted enrollment '+label);auths[label]=path;profiles[label]=STATE/('adapter-cache-'+label+'-'+key());owners[label]=name;private_values.append(json.loads(path.read_text())['deliveryToken'])
    def helper(label,command,extra=None,at=None,token=None):
        args=[os.environ['MCPGUARD_TEST_HELPER_BINARY'],command,'-gateway',at or endpoint,'-auth-file',str(auths[label]),'-directory',str(profiles[label]),'-allow-loopback-http']+(extra or [])
        r=subprocess.run(args,env=clean|{'MCPGUARD_TOKEN':token or CFG[owners[label]+'_token']},capture_output=True,text=True,timeout=55)
        if command=='headers' and r.returncode==0:
            parsed=json.loads(r.stdout);check(set(parsed)=={'Authorization','X-MCPGuard-Credential-Context'},'native host sink contains only required private headers');private_values.append(parsed['X-MCPGuard-Credential-Context']);private_values.append(parsed['X-MCPGuard-Credential-Context'].split('.',1)[1]);safe_outputs.append(r.stderr)
        else:safe_outputs.append(r.stdout+r.stderr)
        return r
    def host_headers(label):
        r=helper(label,'headers');check(r.returncode==0,'native host privately obtains headers');return json.loads(r.stdout)
    def cache(label):return json.loads((profiles[label]/'session.json').read_text())
    def status(label):
        r=subprocess.run([os.environ['MCPGUARD_TEST_SESSION_BINARY'],'status','-gateway',origin,'-auth-file',str(auths[label]),'-directory',str(profiles[label]),'-allow-loopback-http'],env=clean|{'MCPGUARD_TOKEN':CFG[owners[label]+'_token']},capture_output=True,text=True,timeout=25);safe_outputs.append(r.stdout+r.stderr);check(r.returncode==0,'safe private status '+label);return json.loads(r.stdout)
    def active(label,cid):return any(a['credentialId']==cid and a['state']=='ACTIVE' for a in status(label)['activations'])
    def file(label,cid):return profiles[label]/'files'/('mcpguard-'+cid+'.mgcred')
    def rpc(client,name,args):
        out=client.call('tools/call',{'name':name,'arguments':args});model_outputs.append(json.dumps(out));return out
    def prepare(client,grant):
        out=rpc(client,'credential.prepare_download',{'grantId':grant,'requestKey':key()});check(not out.get('isError',False),'MCP preparation returns safe metadata');return out
    def private_command(label,operation,extra=None):
        args=[os.environ['MCPGUARD_TEST_SESSION_BINARY'],operation,'-gateway',origin,'-auth-file',str(auths[label]),'-directory',str(profiles[label]),'-allow-loopback-http']+(extra or [])
        r=subprocess.run(args,env=clean|{'MCPGUARD_TOKEN':CFG[owners[label]+'_token']},capture_output=True,text=True,timeout=25);safe_outputs.append(r.stdout+r.stderr);return r
    def inventory():return json.loads(scalar("SELECT jsonb_build_object('accounts',(SELECT count(*) FROM mcpguard_ra.native_accounts WHERE org_id='"+wire.dev.ORG+"'),'jobs',(SELECT count(*) FROM mcpguard_ra.provisioning_jobs WHERE org_id='"+wire.dev.ORG+"'),'grants',(SELECT count(*) FROM mcpguard_ra.access_grant_versions WHERE org_id='"+wire.dev.ORG+"'),'packages',(SELECT count(*) FROM mcpguard_ra.credential_packages WHERE org_id='"+wire.dev.ORG+"'));"))
    def wait_expired(label):
        expiry=datetime.fromisoformat(cache(label)['context']['context']['expiresAt'].replace('Z','+00:00'));delay=(expiry-datetime.now(timezone.utc)).total_seconds()+.12;check(delay<4,'short fixture expiry remains bounded');time.sleep(max(0,delay))
    try:
        for entry in ('NATIVE_HTTP_HELPER','STDIO_PRIVATE','URL_ONLY'):
            r=subprocess.run([os.environ['MCPGUARD_TEST_HELPER_BINARY'],'capabilities','-entry',entry],capture_output=True,text=True);safe_outputs.append(r.stdout+r.stderr);caps=json.loads(r.stdout);check(caps['entry']==entry and caps['modelVisibleSecrets']=='FORBIDDEN' and not caps['nativeDatabaseConnection'],'safe capability declaration '+entry)
            if entry=='URL_ONLY':check(not caps['privateDelivery'] and caps['syncDriver']=='UNAVAILABLE','URL-only does not claim automatic private delivery')
        for label,name in [('http','alice'),('stdio','alice'),('bob','bob')]:enroll(label,name)
        process=start();native=Native(port,host_headers('http'));bridge=Stdio(endpoint,CFG['alice_token'],auths['stdio'],profiles['stdio']);bridges.append(bridge);bob=Stdio(endpoint,CFG['bob_token'],auths['bob'],profiles['bob']);bridges.append(bob)
        check(not {'sampling','elicitation','tasks'}&set(bridge.capability),'private extension does not invent optional MCP services')
        code,h,body=wire.http(port,'/console/login',body={'username':'visitor','password':CFG['console_secrets']['visitor']},headers={'Origin':origin});check(code==200,'independent reviewer login');cookie=next(v for k,v in h.items() if k.lower()=='set-cookie').split(';',1)[0];csrf=json.loads(body)['csrf']
        for index in (1,2):
            request={'resourceId':f'40000000-0000-4000-8000-{index:012d}','profileId':f'50000000-0000-4000-8000-{index:012d}','requestedExpiry':(datetime.now(timezone.utc)+timedelta(minutes=20)).isoformat().replace('+00:00','Z'),'reason':'RA-10 independent adapter account','requestKey':key()}
            submitted=rpc(native,'access.request',request)['structuredContent'];decision={'requestHash':submitted['requestHash'],'expectedRevision':submitted['revision'],'decision':'APPROVE','approvedExpiry':submitted['requestedExpiry'],'reason':'bounded personal native account','requestKey':key()}
            code,_,body=wire.http(port,'/console/api/v2/access-requests/'+submitted['requestId']+'/decisions',body=decision,headers={'Cookie':cookie,'Origin':origin,'X-CSRF-Token':csrf,'If-Match':'"'+submitted['revision']+'"','Idempotency-Key':decision['requestKey']});check(code==200,'approval is independent of helper')
            grant=json.loads(body)['grant']['grantId'];grants.append(grant);r=subprocess.run([os.environ['MCPGUARD_TEST_PROVISION_BINARY'],'run',submitted['orgId'],grant],env=worker,capture_output=True,text=True,timeout=35);safe_outputs.append(r.stdout+r.stderr);check(r.returncode==0,'real PG native account verified')
            tasks.append(prepare(native,grant)['structuredContent'])
        before=inventory();check(not list((profiles['http']/'files').glob('*.mgcred')),'native model result itself does not receive or save private bytes')
        p=subprocess.Popen([os.environ['MCPGUARD_TEST_HELPER_BINARY'],'worker','-gateway',endpoint,'-auth-file',str(auths['http']),'-directory',str(profiles['http']),'-allow-loopback-http','-interval-ms','250','-max-runtime-seconds','30'],env=clean|{'MCPGUARD_TOKEN':CFG['alice_token']},stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True);workers.append(p)
        wire.until(lambda:len(list((profiles['http']/'files').glob('*.mgcred')))==2,12)
        # Wait for completion receipts as well as file publication, without racing a profile lock.
        wire.until(lambda:len(cache('http')['selected'])==2 and all(x['state']=='ACTIVE' for x in cache('http')['entries'].values()),12)
        p.send_signal(signal.SIGTERM);p.wait(timeout=8);safe_outputs.extend(p.communicate());workers.remove(p)
        check(all(active('http',t['credentialId']) for t in tasks),'native HTTP worker privately activates two resources')
        for grant,task in zip(grants,tasks):
            out=prepare(bridge,grant);notice=out['_meta'][CAP];check(notice['state']=='ACTIVE' and not notice['businessRequestReplayed'],'stdio privately delivers and activates before reporting local state');check(file('stdio',task['credentialId']).read_bytes()==file('http',task['credentialId']).read_bytes(),'both entries use same authorized encrypted reference')
        denied=rpc(bob,'credential.prepare_download',{'grantId':grants[0],'requestKey':key()});check(denied.get('isError',False),'stdio Bob cannot prepare Alice grant');check(not status('bob')['activations'],'Bob context does not inherit Alice activation');check(inventory()==before,'two adapter entries create no duplicate account grant or package')
        for label,driver in [('http',lambda:helper('http','worker',['-once'])),('stdio',lambda:bridge.call('tools/list'))]:
            path=file(label,tasks[0]['credentialId']);raw=path.read_bytes();path.unlink();driver();check(path.read_bytes()==raw and active(label,tasks[0]['credentialId']),label+' missing file recovers same package')
        # A consumed response lost to the stdio adapter must not turn the
        # already completed preparation into an automatic business retry.
        spec2=importlib.util.spec_from_file_location('loss',ROOT/'tests/download_e2e.py');loss=importlib.util.module_from_spec(spec2);spec2.loader.exec_module(loss);enroll('loss');proxy=loss.LossProxy(port,'after')
        original_forward=proxy.server.RequestHandlerClass.forward
        def once_forward(request):
            if request.command=='POST' and request.path.endswith('/claim') and proxy.posts>=1:
                body=request.rfile.read(int(request.headers.get('Content-Length','0')));headers={k:v for k,v in request.headers.items() if k.lower() not in ('host','connection','content-length')};code,h,data=wire.http(port,request.path,body=body,headers=headers);proxy.posts+=1;request.send_response(code)
                for k,v in h.items():
                    if k.lower() not in ('server','date','connection','transfer-encoding'):request.send_header(k,v)
                request.end_headers();request.wfile.write(data)
            else:original_forward(request)
        proxy.server.RequestHandlerClass.forward=once_forward
        try:
            lossy=Stdio(proxy.origin+'/mcp',CFG['alice_token'],auths['loss'],profiles['loss']);bridges.append(lossy);out=prepare(lossy,grants[0]);check(out['_meta'][CAP]['state']=='PENDING_PRIVATE_SYNC','lost private claim keeps original safe preparation result')
            check(proxy.posts==1,'lost response is not automatically claimed twice');lossy.call('tools/list');lossy.call('tools/list');check(file('loss',tasks[0]['credentialId']).exists(),'next private sync reconciles consumed response without replaying preparation')
            model_outputs.append(lossy.finish());bridges.remove(lossy)
        finally:proxy.close()
        check(inventory()==before,'interrupted private synchronization preserves native inventory')
        # Renewals of active Contexts keep cached native headers usable.
        current=cache('http')['context'];check(private_command('http','renew-context').returncode==0,'private context renews without token change');check(cache('http')['context']['contextToken']==current['contextToken'],'native cached Context token stays stable during renewal')
        # New short Contexts prove expiry recovery separately for both entries.
        for b in list(bridges):model_outputs.append(b.finish());bridges.remove(b)
        stop();env['MCPGUARD_CONTEXT_TTL_SECONDS']='2';env['MCPGUARD_CONTEXT_LIFETIME_SECONDS']='4';process=start()
        enroll('renewing-http');renew_headers=host_headers('renewing-http');renew_native=Native(port,renew_headers);initial=cache('renewing-http')['context']
        keeper=subprocess.Popen([os.environ['MCPGUARD_TEST_HELPER_BINARY'],'worker','-gateway',endpoint,'-auth-file',str(auths['renewing-http']),'-directory',str(profiles['renewing-http']),'-allow-loopback-http','-interval-ms','200','-max-runtime-seconds','4'],env=clean|{'MCPGUARD_TOKEN':CFG['alice_token']},stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True);workers.append(keeper)
        time.sleep(2.2);prepare(renew_native,grants[1]);now=cache('renewing-http')['context'];check(now['contextToken']==initial['contextToken'] and now['context']['expiresAt']>initial['context']['expiresAt'],'native worker renews same cached header beyond its initial short expiry')
        keeper.send_signal(signal.SIGTERM);keeper.wait(timeout=8);safe_outputs.extend(keeper.communicate());workers.remove(keeper)
        enroll('short-http');headers=host_headers('short-http');short_native=Native(port,headers);prepare(short_native,grants[1]);check(helper('short-http','worker',['-once']).returncode==0,'short native Context activates file');wait_expired('short-http')
        denied=rpc(short_native,'credential.prepare_download',{'grantId':grants[1],'requestKey':key()});check(denied.get('isError',False),'cached native header expires closed');check(helper('short-http','worker',['-once']).returncode!=0,'native worker cannot rotate expired cached header silently')
        old=cache('short-http')['context']['context']['contextId'];new_headers=host_headers('short-http');check(cache('short-http')['context']['context']['contextId']!=old,'explicit native reconnect creates new Context');short_native=Native(port,new_headers);check(active('short-http',tasks[1]['credentialId']),'native reconnect revalidates retained file')
        enroll('short-stdio');short_bridge=Stdio(endpoint,CFG['alice_token'],auths['short-stdio'],profiles['short-stdio']);bridges.append(short_bridge);prepare(short_bridge,grants[1]);old=cache('short-stdio')['context']['context']['contextId'];wait_expired('short-stdio');short_bridge.call('tools/list');check(cache('short-stdio')['context']['context']['contextId']!=old and active('short-stdio',tasks[1]['credentialId']),'stdio next call rebuilds expired Context and resubmits valid file')
        for label,client in [('short-http',short_native),('short-stdio',short_bridge)]:
            check(private_command(label,'init',['-replace-context']).returncode==0,'fresh active Context before explicit close '+label)
            check(private_command(label,'close').returncode==0,'explicit private Context close '+label)
            check(status(label)['context']['state']=='REVOKED','close test reaches REVOKED rather than natural expiry '+label)
            if label=='short-http':check(helper(label,'headers').returncode!=0,'native helper never silently recreates revoked Context')
            else:check('error' in client.raw('tools/list'),'stdio never silently recreates revoked Context')
            check(private_command(label,'init',['-replace-context']).returncode==0,'explicit private repair '+label)
            if label=='short-http':short_native=Native(port,host_headers(label))
            else:client.call('tools/list')
        # Return to a practical TTL while new main connections revalidate all files.
        model_outputs.append(short_bridge.finish());bridges.remove(short_bridge);stop();env['MCPGUARD_CONTEXT_TTL_SECONDS']='120';env['MCPGUARD_CONTEXT_LIFETIME_SECONDS']='300';process=start()
        native=Native(port,host_headers('http'));bridge=Stdio(endpoint,CFG['alice_token'],auths['stdio'],profiles['stdio']);bridges.append(bridge)
        cid=tasks[0]['credentialId'];r=subprocess.run([os.environ['MCPGUARD_TEST_CREDENTIAL_BINARY'],'revoke',cid,'1',key(),'RA-10 reference revoked'],env=trusted|{'MCPGUARD_TOKEN':CFG['alice_token']},capture_output=True,text=True,timeout=20);safe_outputs.append(r.stdout+r.stderr);check(r.returncode==0,'reference revoked independently of adapter')
        check(helper('http','worker',['-once']).returncode==0,'native worker remains available for other resources');bridge.call('tools/list')
        for label in ('http','stdio'):check(not active(label,cid) and active(label,tasks[1]['credentialId']),label+' revoked file stays unusable while second resource remains active')
        for client in (native,bridge):check(rpc(client,'credential.prepare_download',{'grantId':grants[0],'requestKey':key()}).get('isError',False),'adapter cannot restore revoked package by re-preparing')
        check(helper('http','headers',token=CFG['bob_token']).returncode!=0,'native hook refuses changed user with pinned profile')
        bad=subprocess.run([os.environ['MCPGUARD_TEST_CONNECT_BINARY'],'-gateway',endpoint,'-allow-loopback-http','-private-auth-file',str(auths['stdio']),'-private-directory',str(profiles['stdio'])],env=clean|{'MCPGUARD_TOKEN':CFG['bob_token']},input='',capture_output=True,text=True,timeout=15);safe_outputs.append(bad.stdout+bad.stderr);check(bad.returncode!=0 and not bad.stdout,'stdio refuses changed identity before any protocol output')
        check(inventory()==before,'recovery and revocation preserve accounts jobs grants and packages')
        for b in list(bridges):model_outputs.append(b.finish());bridges.remove(b)
        for label in profiles:
            if not (profiles[label]/'session.json').exists():continue
            private_values.append(cache(label)['context']['contextToken'])
            for path in (profiles[label]/'files').glob('*.mgcred'):
                packet=json.loads(path.read_text());private_values.extend(packet[k] for k in ('nonce','ciphertext','authenticationTag'))
        text='\n'.join(safe_outputs+model_outputs)+(STATE/'adapter-process.log').read_text();audit=scalar("SELECT jsonb_agg(to_jsonb(t))::text FROM mcpguard_ra.context_audit t;")
        check(all(v not in text+audit for v in private_values),'model protocol stderr worker outputs logs and audit exclude actual headers tokens and packet bytes')
        check(not any('"contextToken"' in x or '"ciphertext"' in x for x in model_outputs),'private lifecycle responses never become MCP content')
    finally:
        for p in workers:
            if p.poll() is None:p.terminate();p.wait(timeout=8)
        for b in bridges:
            if b.p.poll() is None:b.p.terminate();b.p.wait(timeout=8)
        stop();wire.dev.write_private(STATE/('adapter-partial-'+str(time.time_ns())+'.json'),json.dumps({'passedBeforeExit':len(CHECKS),'checks':CHECKS},indent=2)+'\n')
    report={'phase':'RA-10','passed':len(CHECKS),'checks':CHECKS,'nativeHTTPHostHook':True,'stdioPrivateAdapter':True,'nativeHookStdoutIsPrivateHostChannel':True,'nativeCachedHeaderRotationNeedsReconnect':True,'modelVisibleOutputsRedacted':True,'nativeInventory':before,'URLOnlyPrivateDelivery':False,'actualNamedDesktopClient':False,'sameOSUserArbitraryFileAccessIsolation':False,'existingServiceChanged':False}
    wire.dev.write_private(STATE/'adapter-e2e-results.json',json.dumps(report,indent=2)+'\n');print('RA-10 adapter checks passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
