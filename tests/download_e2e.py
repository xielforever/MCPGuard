#!/usr/bin/env python3
"""RA-08 real private delivery, separate component, concurrent claim and wire loss."""
import hashlib,importlib.util,json,os,signal,socket,subprocess,threading,uuid
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime,timedelta,timezone
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('legacy_wire',ROOT/'tests/e2e.py');wire=importlib.util.module_from_spec(spec);spec.loader.exec_module(wire)
CFG=wire.CFG;STATE=wire.dev.STATE;CHECKS=[]
def check(value,label):
    if not value:raise AssertionError(label)
    CHECKS.append(label)
def key():return str(uuid.uuid4())
def scalar(sql,container=None,database='mcpguard_meta'):
    return wire.dev.docker('exec','-i',container or CFG['container'],'psql','-X','-A','-t','-v','ON_ERROR_STOP=1','-U','postgres','-d',database,input=sql,capture_output=True,text=True).stdout.strip()
class LossProxy:
    def __init__(self,port,mode):
        self.posts=0;parent=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):self.forward()
            def do_POST(self):self.forward()
            def forward(self):
                body=self.rfile.read(int(self.headers.get('Content-Length','0'))) if self.command=='POST' else None
                is_claim=self.command=='POST' and self.path.endswith('/claim')
                if is_claim:
                    parent.posts+=1
                    if mode=='before':self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','connection','content-length')}
                code,h,data=wire.http(port,self.path,body=body,headers=headers,method=self.command)
                self.send_response(code)
                for k,v in h.items():
                    if k.lower() not in ('server','date','connection','transfer-encoding'):self.send_header(k,v)
                self.end_headers()
                if is_claim and mode=='after' and code==200:
                    self.wfile.write(data[:len(data)//2]);self.wfile.flush();self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                self.wfile.write(data)
        self.server=ThreadingHTTPServer(('127.0.0.1',0),Handler);self.server.daemon_threads=True;self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
    @property
    def origin(self):return 'http://127.0.0.1:'+str(self.server.server_port)
    def close(self):self.server.shutdown();self.server.server_close();self.thread.join(timeout=3)

def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}'
    env=wire.dev.runtime_env(CFG)|{'MCPGUARD_LISTEN':f'127.0.0.1:{port}','MCPGUARD_RESOURCE_CATALOG':'enabled','MCPGUARD_RESOURCE_ACCESS':'enabled','MCPGUARD_RESOURCE_CREDENTIALS':'enabled','MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json'),'MCPGUARD_RESOURCE_DOWNLOADS':'enabled','MCPGUARD_DELIVERY_DSN':CFG['deliveryDsn'],'MCPGUARD_DOWNLOAD_TTL_SECONDS':'60','MCPGUARD_DOWNLOAD_MAX_READY':'5'}
    env.pop('MCPGUARD_NATIVE_CONFIG',None)
    clean={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}
    server_env=clean|{k:CFG[k] for k in ('MCPGUARD_ENCRYPTION_KEY','MCPGUARD_PLAN_KEY')}|{'MCPGUARD_METADATA_DSN':CFG['credentialDsn'],'MCPGUARD_CREDENTIAL_KEYS':str(STATE/'package-keys.json')}
    worker_env=server_env|{'MCPGUARD_METADATA_DSN':CFG['provisionerDsn'],'MCPGUARD_PROVIDER_CONFIG':str(STATE/'provider-private.json')}
    private_keys=json.loads((STATE/'package-keys.json').read_text())
    secrets=[CFG['credential_password'],CFG['delivery_password'],CFG['provisioner_password'],CFG['MCPGUARD_ENCRYPTION_KEY'],CFG['MCPGUARD_PLAN_KEY'],*[v['key'] for v in private_keys['keys']]]
    auth_files={};proofs={};safe_outputs=[];model_outputs=[];packets=[];process=None;bridge=None;receipts=[];network=[]
    def private_dir():
        path=STATE/('private-client-'+key());path.mkdir(mode=0o700);return path
    def start():
        with open(STATE/'download-process.log','a') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log)
        def ready():
            if p.poll() is not None:raise AssertionError('download gateway startup failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop():
        if process and process.poll() is None:process.send_signal(signal.SIGTERM);process.wait(timeout=10)
    def server_op(name,args,override=None):
        r=subprocess.run([os.environ['MCPGUARD_TEST_CREDENTIAL_BINARY'],*args],env=server_env|{'MCPGUARD_TOKEN':CFG[name+'_token']}|(override or {}),capture_output=True,text=True,timeout=25);safe_outputs.append(r.stdout+r.stderr);return r
    def component(name,command,did,directory=None,gateway=None):
        args=[os.environ['MCPGUARD_TEST_DOWNLOAD_BINARY'],command,'-gateway',gateway or origin,'-download-id',did,'-auth-file',str(auth_files[name]),'-allow-loopback-http']
        if directory is not None:args+=['-directory',str(directory)]
        r=subprocess.run(args,env=clean|{'MCPGUARD_TOKEN':CFG[name+'_token']},capture_output=True,text=True,timeout=25);safe_outputs.append(r.stdout+r.stderr);return r
    def rpc(client,name,args):
        out=client.call('tools/call',{'name':name,'arguments':args});model_outputs.append(json.dumps(out));return out
    def prepare(client,grant,request_key=None):
        out=rpc(client,'credential.prepare_download',{'grantId':grant,'requestKey':request_key or key()});check(not out.get('isError',False),'MCP prepares safe download metadata');return out['structuredContent']
    def claim(did,name='alice',proof=None,extra=None,body=None):
        headers={'X-MCPGuard-Delivery-Token':proof if proof is not None else proofs[name]};headers|=extra or {}
        return wire.http(port,'/api/v2/credential-downloads/'+did+'/claim',CFG[name+'_token'],body={} if body is None else body,headers=headers)
    def ticket(did):
        r=component('alice','status',did);check(r.returncode==0,'private component reads safe ticket state');return json.loads(r.stdout)
    def inventory(grant):
        org=str(uuid.UUID(wire.dev.ORG));gid=str(uuid.UUID(grant))
        meta=json.loads(scalar(f"SELECT jsonb_build_object('accounts',(SELECT count(*) FROM mcpguard_ra.native_accounts WHERE org_id='{org}'),'jobs',(SELECT count(*) FROM mcpguard_ra.provisioning_jobs WHERE org_id='{org}'),'grantVersions',(SELECT count(*) FROM mcpguard_ra.access_grant_versions WHERE org_id='{org}' AND grant_id='{gid}'),'packages',(SELECT count(*) FROM mcpguard_ra.credential_packages p JOIN mcpguard_ra.credential_leases l USING(org_id,credential_id) WHERE p.org_id='{org}' AND l.grant_id='{gid}'),'leases',(SELECT count(*) FROM mcpguard_ra.credential_leases WHERE org_id='{org}' AND grant_id='{gid}'));"))
        meta['nativeAccounts']=[int(scalar('SELECT count(*) FROM mcpguard_provision.accounts;',p['container'],'mcpguard_business')) for p in CFG['raPgAdmins']];return meta
    try:
        for name in ('alice','bob'):
            path=STATE/('delivery-auth-'+name+'-'+key()+'.json');r=server_op(name,['enroll-delivery','600',str(path)]);check(r.returncode==0,'server enrolls independent private delivery authorization')
            auth_files[name]=path;private=json.loads(path.read_text());proofs[name]=private['deliveryToken'];secrets.append(proofs[name]);check(path.stat().st_mode&0o077==0 and 'deliveryToken' not in r.stdout,'enrollment output excludes private token')
        denied_path=STATE/'denied-enrollment.json';r=server_op('alice',['enroll-delivery','600',str(denied_path)],{'MCPGUARD_METADATA_DSN':CFG['deliveryDsn']});check(r.returncode!=0 and not denied_path.exists(),'delivery runtime cannot enroll new private authority')
        process=start();alice=wire.HTTPMCP(port,'alice');bob=wire.HTTPMCP(port,'bob');bridge=wire.StdioMCP(port,'alice')
        names={t['name'] for t in alice.call('tools/list')['tools']};check({n for n in names if n.startswith('credential.')}=={'credential.status','credential.prepare_download'},'MCP publishes preparation and status without claim/decrypt tools')
        check(not {'credential.claim','credential.decrypt','credential.download','context.activate','pg.get_item'}&names,'private bytes and future execution stay out of tool catalog')
        for index in (1,2):
            # Gateway restart deliberately drops in-memory browser sessions.
            # Authenticate the human again; never reuse the old cookie/CSRF.
            code,h,body=wire.http(port,'/console/login',body={'username':'visitor','password':CFG['console_secrets']['visitor']},headers={'Origin':origin});check(code==200,'independent human reviewer session')
            cookie=next(v for k,v in h.items() if k.lower()=='set-cookie').split(';',1)[0];csrf=json.loads(body)['csrf']
            request={'resourceId':f'40000000-0000-4000-8000-{index:012d}','profileId':f'50000000-0000-4000-8000-{index:012d}','requestedExpiry':(datetime.now(timezone.utc)+timedelta(minutes=20)).isoformat().replace('+00:00','Z'),'reason':'RA-08 isolated private download','requestKey':key()}
            submitted=rpc(alice,'access.request',request)['structuredContent'];decision={'requestHash':submitted['requestHash'],'expectedRevision':submitted['revision'],'decision':'APPROVE','approvedExpiry':submitted['requestedExpiry'],'reason':'human approved bounded individual account','requestKey':key()}
            headers={'Cookie':cookie,'Origin':origin,'X-CSRF-Token':csrf,'If-Match':'"'+submitted['revision']+'"','Idempotency-Key':decision['requestKey']};code,_,body=wire.http(port,'/console/api/v2/access-requests/'+submitted['requestId']+'/decisions',body=decision,headers=headers)
            if code!=200:
                wire.dev.write_private(STATE/('download-review-failure-'+key()+'.json'),json.dumps({'status':code,'body':body.decode(errors='replace')}));raise AssertionError('reviewer HTTP status '+str(code)+'; private failure retained')
            approved=json.loads(body);check(approved['grant']['state']=='PROVISIONING','approval remains separate from native provisioning')
            grant=approved['grant']['grantId'];bad=rpc(alice,'credential.prepare_download',{'grantId':grant,'requestKey':key()});check(bad.get('isError',False),'unverified grant cannot prepare download')
            r=subprocess.run([os.environ['MCPGUARD_TEST_PROVISION_BINARY'],'run',submitted['orgId'],grant],env=worker_env,capture_output=True,text=True,timeout=35);safe_outputs.append(r.stdout+r.stderr);check(r.returncode==0 and json.loads(r.stdout)['state']=='VERIFIED','separate worker verifies real native account')
            req_key=key();one=prepare(alice,grant,req_key);check(one['state']=='READY' and one['deliveryMode']=='MANUAL_OWNER','preparation declares owner-only manual delivery')
            replay=prepare(bridge,grant,req_key);check(replay==one,'stdio preparation replays same ticket without private bytes')
            bad=rpc(bob,'credential.prepare_download',{'grantId':grant,'requestKey':key()});check(bad.get('isError',False),'another user cannot prepare copied grant')
            bad=rpc(alice,'credential.prepare_download',{'grantId':grant,'requestKey':key(),'clientInstanceId':key()});check(bad.get('isError',False),'self-reported client ID cannot enable automatic delivery')
            did=one['downloadId'];before=inventory(grant);check(before['packages']==before['leases']==1,'first preparation commits one package and lease')
            code,_,_=wire.http(port,'/api/v2/credential-downloads/'+did+'/claim',CFG['alice_token'],body={});check(code==403,'MCP bearer alone cannot claim private file')
            code,_,_=claim(did,proof=proofs['bob']);check(code==403,'wrong owner delivery authorization rejected')
            code,_,_=claim(did,name='bob');check(code==403,'another authenticated user cannot claim copied ticket')
            code,_,_=claim(did,extra={'Cookie':cookie});check(code==403,'browser cookie cannot substitute for private route authentication')
            code,_,_=claim(did,extra={'Origin':origin});check(code==403,'browser origin is rejected on private delivery route')
            code,_,_=claim(did,body={'ownerId':wire.dev.IDS['alice']});check(code==400,'claim rejects caller-controlled owner fields')
            code,_,_=wire.http(port,'/api/v2/credential-downloads/'+did+'/claim?token=redacted-test',CFG['alice_token'],body={},headers={'X-MCPGuard-Delivery-Token':proofs['alice']});check(code==403,'private authorization never accepted in query parameters')
            check(ticket(did)['state']=='READY','denied requests do not consume ticket')
            saved=private_dir();r=component('alice','claim',did,saved);check(r.returncode==0,'separate component privately receives and saves complete response');receipt=json.loads(r.stdout);path=saved/receipt['file'];blob=path.read_bytes();packet=json.loads(blob);packets.append(packet)
            check(receipt['state']=='SAVED' and path.stat().st_mode&0o077==0 and len(list(saved.iterdir()))==1,'private atomic file saved without temporary file residue')
            check(ticket(did)['state']=='CLAIMED','successful claim persists consumed ticket')
            code,_,body=claim(did);check(code==409 and blob not in body,'same ticket cannot return second file')
            replay=prepare(alice,grant,req_key);check(replay['downloadId']==did and replay['state']=='CLAIMED','idempotency replay returns safe consumed state')
            r=server_op('alice',['check',request['resourceId'],one['credentialId'],str(path)]);check(r.returncode==0,'server validates reference delivered to private component')
            concurrent=prepare(alice,grant);check(concurrent['credentialId']==one['credentialId'] and concurrent['leaseId']==one['leaseId'],'new ticket reuses original package and lease')
            with ThreadPoolExecutor(max_workers=8) as pool:results=list(pool.map(lambda _:claim(concurrent['downloadId']),range(8)))
            check(sum(code==200 for code,_,_ in results)==1 and sum(code==409 for code,_,_ in results)==7,'eight concurrent real HTTP claims produce exactly one file response')
            winner=next((headers,data) for code,headers,data in results if code==200);check(winner[1]==blob and winner[0].get('Cache-Control')=='no-store' and winner[0].get('Content-Type')=='application/octet-stream','winning private response is exact stored blob and non-cacheable')
            for mode in ('before','after'):
                pending=prepare(alice,grant);destination=private_dir();proxy=LossProxy(port,mode)
                try:r=component('alice','claim',pending['downloadId'],destination,proxy.origin)
                finally:proxy.close()
                check(r.returncode!=0 and not list(destination.iterdir()) and proxy.posts==1,'network '+mode+' interruption publishes no partial file and does not auto-retry')
                observed=ticket(pending['downloadId']);expected='READY' if mode=='before' else 'CLAIMED';check(observed['state']==expected,'network '+mode+' interruption preserves authoritative '+expected+' state')
                if mode=='before':retry=pending
                else:
                    code,_,_=claim(pending['downloadId']);check(code==409,'lost response cannot replay consumed ticket');retry=prepare(alice,grant);check(retry['downloadId']!=pending['downloadId'] and retry['credentialId']==pending['credentialId'],'replacement ticket delivers same package after lost response')
                recovered=private_dir();r=component('alice','claim',retry['downloadId'],recovered);check(r.returncode==0 and next(recovered.iterdir()).read_bytes()==blob,'explicit state-aware retry recovers exact original file')
                network.append({'resourceId':request['resourceId'],'mode':mode,'stateAfterLoss':observed['state'],'writeOutcome':observed['deliveryOutcome'],'samePackage':True,'partialFilePublished':False})
            waiting=prepare(alice,grant);model_outputs.extend(bridge.lines);bridge.close();bridge=None;stop();process=start();alice=wire.HTTPMCP(port,'alice');bob=wire.HTTPMCP(port,'bob');bridge=wire.StdioMCP(port,'alice')
            check(ticket(waiting['downloadId'])['state']=='READY','gateway restart preserves pending ticket');restart_dir=private_dir();r=component('alice','claim',waiting['downloadId'],restart_dir);check(r.returncode==0,'private authorization and claim survive gateway restart')
            after=inventory(grant);check(after==before,'all retickets/restarts/network retries preserve native accounts, grants, packages and leases')
            receipts.append({'resourceId':request['resourceId'],'credentialId':one['credentialId'],'leaseId':one['leaseId'],'inventoryBefore':before,'inventoryAfter':after})
            if index==1:
                pending=prepare(alice,grant);r=server_op('alice',['revoke',one['credentialId'],'1',key(),'RA-08 server lease revoked before claim']);check(r.returncode==0,'server revokes file lease before another claim')
                code,_,_=claim(pending['downloadId']);check(code==409 and ticket(pending['downloadId'])['state']=='CANCELLED','revocation committed before claim cancels pending ticket')
                bad=rpc(alice,'credential.prepare_download',{'grantId':grant,'requestKey':key()});check(bad.get('isError',False),'new ticket cannot revive revoked package')
                r=server_op('alice',['check',request['resourceId'],one['credentialId'],str(path)]);check(r.returncode!=0 and path.read_bytes()==blob,'retained private file cannot bypass server revocation')
        if bridge:model_outputs.extend(bridge.lines)
        private_values=[*secrets,*[p[k] for p in packets for k in ('ciphertext','authenticationTag','nonce')]]
        model_text='\n'.join(model_outputs);component_text='\n'.join(safe_outputs)
        check(all(v not in model_text for v in private_values),'MCP HTTP/stdio results contain no package bytes or private authentication')
        check(all(v not in component_text for v in private_values),'component and server command outputs contain no package bytes or tokens')
        audit=scalar("SELECT jsonb_agg(to_jsonb(a))::text FROM mcpguard_ra.download_audit a;")
        log=(STATE/'download-process.log').read_text();check(all(v not in audit+log for v in private_values),'persistent delivery audit and application logs exclude private contents')
        check('CLAIM_COMMITTED' in audit and 'DELIVERY_WRITE_COMPLETED' in audit and 'client_saved' not in audit.lower(),'audit separates claim commitment and server writer outcome')
    finally:
        if bridge:bridge.close()
        stop()
    report={'phase':'RA-08','passed':len(CHECKS),'checks':CHECKS,'networkLoss':network,'receipts':receipts,'privateManualDelivery':True,'automaticContext':False,'realDesktopNativeClient':False,'modelProtocolAndComponentOutputRedacted':True,'sameOSUserArbitraryFileAccessIsolationClaimed':False,'existingServiceChanged':False}
    wire.dev.write_private(STATE/'download-e2e-results.json',json.dumps(report,indent=2)+'\n');print('RA-08 private delivery verification passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
