#!/usr/bin/env python3
"""Independent RA-04 HTTP + stdio + protected browser API verification."""
import importlib.util,json,os,signal,subprocess,time,uuid
from datetime import datetime,timedelta,timezone
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('legacy_wire',ROOT/'tests/e2e.py')
wire=importlib.util.module_from_spec(spec);spec.loader.exec_module(wire)
CFG=wire.CFG;STATE=wire.dev.STATE;CHECKS=[]
def check(condition,label):
    if not condition:raise AssertionError(label)
    CHECKS.append(label)
def stamp():return (datetime.now(timezone.utc)+timedelta(minutes=30)).isoformat().replace('+00:00','Z')
def key():return str(uuid.uuid4())
def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}'
    env=wire.dev.runtime_env(CFG)
    env['MCPGUARD_LISTEN']=f'127.0.0.1:{port}'
    env['MCPGUARD_RESOURCE_CATALOG']='enabled';env['MCPGUARD_RESOURCE_ACCESS']='enabled'
    # Fixed demo adapters are unnecessary for the new workflow, which dispatches
    # no native account or business operation.
    env.pop('MCPGUARD_NATIVE_CONFIG',None)
    checks_path=STATE/'access-e2e-results.json'
    process=None;bridge=None
    def start():
        log=open(STATE/'access-process.log','a')
        p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log);log.close()
        def ready():
            if p.poll() is not None:raise AssertionError('isolated access binary startup failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop(p):
        if p and p.poll() is None:p.send_signal(signal.SIGTERM);p.wait(timeout=10)
    def login(user):
        status,headers,body=wire.http(port,'/console/login',body={'username':user,'password':CFG['console_secrets'][user]},headers={'Origin':origin})
        check(status==200,'independent browser login: '+user)
        cookie=next(value for name,value in headers.items() if name.lower()=='set-cookie').split(';',1)[0]
        return cookie,json.loads(body)['csrf']
    rid='40000000-0000-4000-8000-000000000001';profile='50000000-0000-4000-8000-000000000001'
    try:
        process=start();alice=wire.HTTPMCP(port,'alice');bob=wire.HTTPMCP(port,'bob')
        names={t['name'] for t in alice.call('tools/list')['tools']}
        check({'access.request','access.status','access.cancel','access.grants'}<=names,'production MCP advertises RA-04 tools')
        check(not any(n in names for n in ('access.approve','access.decide','pg.get_item','fixture.pg_read_item','credential.prepare_download')),'approval and future native/credential tools are absent')
        request={'resourceId':rid,'profileId':profile,'requestedExpiry':stamp(),'reason':'isolated RA-04 process request','requestKey':key()}
        for extra in ('ownerId','deciderId','nativeIdentity','sql'):
            result=alice.call('tools/call',{'name':'access.request','arguments':request|{extra:'must be rejected'}})
            check(result.get('isError',False),'MCP rejects caller-selected '+extra)
        result=bob.call('tools/call',{'name':'access.request','arguments':request})
        check(result.get('isError',False),'visible but non-requestable Bob cannot apply')
        result=alice.call('tools/call',{'name':'access.request','arguments':request})
        submitted=result['structuredContent']
        check(not result.get('isError',False) and submitted['state']=='PENDING_APPROVAL','MCP request reaches durable pending state')
        bridge=wire.StdioMCP(port,'alice')
        replay=bridge.call('tools/call',{'name':'access.request','arguments':request})
        check(replay['structuredContent']==submitted,'HTTP and stdio share exact idempotency snapshot')
        altered=alice.call('tools/call',{'name':'access.request','arguments':request|{'reason':'different payload'}})
        check(altered.get('isError',False) and 'IDEMPOTENCY_CONFLICT' in json.dumps(altered),'idempotency conflict is explicit')
        hidden=bob.call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})
        check(hidden.get('isError',False),'Bob cannot read Alice request')
        visitor_cookie,visitor_csrf=login('visitor');alice_cookie,alice_csrf=login('alice')
        status,_,body=wire.http(port,'/console/api/v2/access-requests?limit=1',headers={'Cookie':visitor_cookie},method='GET')
        check(status==200 and any(r['requestId']==submitted['requestId'] for r in json.loads(body)['requests']),'scoped reviewer discovers pending queue')
        decision={'requestHash':submitted['requestHash'],'expectedRevision':submitted['revision'],'decision':'APPROVE','approvedExpiry':submitted['requestedExpiry'],'reason':'reviewed bounded access','requestKey':key()}
        path='/console/api/v2/access-requests/'+submitted['requestId']+'/decisions'
        headers={'Cookie':visitor_cookie,'Origin':origin,'X-CSRF-Token':visitor_csrf,'If-Match':'"'+submitted['revision']+'"','Idempotency-Key':decision['requestKey']}
        status,_,_=wire.http(port,path,CFG['visitor_token'],decision,{'Origin':origin})
        check(status==401,'MCP bearer cannot replace browser session for approval')
        status,_,_=wire.http(port,path,body=decision,headers={k:v for k,v in headers.items() if k!='X-CSRF-Token'})
        check(status==403,'approval requires CSRF')
        status,_,_=wire.http(port,path,body=decision,headers=headers|{'Origin':'http://evil.invalid'})
        check(status==403,'approval requires trusted origin')
        status,_,_=wire.http(port,path,body=decision,headers=headers|{'Cookie':alice_cookie,'X-CSRF-Token':alice_csrf})
        check(status==403,'resource reviewer still cannot approve own request')
        status,_,_=wire.http(port,path,body=decision|{'expectedRevision':'2'},headers=headers)
        check(status==400,'browser If-Match agrees with command revision')
        status,_,_=wire.http(port,path,body=decision|{'requestHash':'changed'},headers=headers)
        check(status==409,'approval rejects changed request hash')
        status,_,_=wire.http(port,'/api/v2/access-requests/'+submitted['requestId']+'/decisions',CFG['visitor_token'],decision)
        check(status==404,'bearer REST has no approval alias')
        status,_,body=wire.http(port,path,body=decision,headers=headers)
        approved=json.loads(body)
        check(status==200 and approved['request']['state']=='APPROVED' and approved['grant']['state']=='PROVISIONING','protected approval atomically produces provisioning grant')
        check('accountId' not in approved['grant'] and 'bindingId' not in approved['grant'],'no placeholder account or binding is fabricated')
        status,_,body=wire.http(port,path,body=decision,headers=headers)
        check(status==200 and json.loads(body)==approved,'browser approval replay does not create another grant')
        observed=bridge.call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})['structuredContent']
        check(observed==approved,'stdio observes the linked request decision and grant')
        grants=alice.call('tools/call',{'name':'access.grants','arguments':{'resourceId':rid}})['structuredContent']['grants']
        check(any(g['grantId']==approved['grant']['grantId'] for g in grants),'owner sees new grant in resource-filtered list')
        bgrants=bob.call('tools/call',{'name':'access.grants','arguments':{}})['structuredContent']['grants']
        check(not bgrants,'Bob grant list remains isolated')
        directory=alice.call('tools/call',{'name':'resource.get','arguments':{'resourceId':rid}})['structuredContent']
        check(directory['resource']['accessState']=='PROVISIONING','directory reflects current provisioning state')
        cancelled=alice.call('tools/call',{'name':'access.cancel','arguments':{'requestId':submitted['requestId'],'expectedRevision':approved['request']['revision'],'requestKey':key()}})
        check(cancelled.get('isError',False),'approved request cannot be cancelled as pending')
        another=alice.call('tools/call',{'name':'access.request','arguments':request|{'requestKey':key()}})['structuredContent']
        cancelled=bridge.call('tools/call',{'name':'access.cancel','arguments':{'requestId':another['requestId'],'expectedRevision':another['revision'],'requestKey':key()}})
        check(cancelled['structuredContent']['state']=='CANCELLED','stdio cancellation closes a pending request')
        bridge.close();bridge=None;stop(process);process=start()
        restored=wire.HTTPMCP(port,'alice').call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})['structuredContent']
        check(restored==approved,'process restart preserves request decision and encrypted state')
        check(not any(value in json.dumps(restored) for value in ('secretRef','endpointConfigRef','password','ciphertext','ra_alice')),'workflow outputs contain no native configuration or ciphertext')
    finally:
        if bridge:bridge.close()
        stop(process)
    checks_path.write_text(json.dumps({'phase':'RA-04','passed':len(CHECKS),'checks':CHECKS,'realDesktopClient':False,'nativeProvisioningImplemented':False,'existingServiceChanged':False},indent=2)+'\n')
    print('RA-04 process verification passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
