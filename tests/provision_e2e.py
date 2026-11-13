#!/usr/bin/env python3
"""RA-05 process proof: real MCP request, human API, separate native worker."""
import importlib.util,json,os,signal,subprocess,time,uuid
from datetime import datetime,timedelta,timezone
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('legacy_wire',ROOT/'tests/e2e.py')
wire=importlib.util.module_from_spec(spec);spec.loader.exec_module(wire)
CFG=wire.CFG;STATE=wire.dev.STATE;CHECKS=[]
def check(value,label):
    if not value:raise AssertionError(label)
    CHECKS.append(label)
def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}'
    env=wire.dev.runtime_env(CFG)|{'MCPGUARD_LISTEN':f'127.0.0.1:{port}','MCPGUARD_RESOURCE_CATALOG':'enabled','MCPGUARD_RESOURCE_ACCESS':'enabled'}
    env.pop('MCPGUARD_NATIVE_CONFIG',None)
    private=json.loads((STATE/'provider-private.json').read_text())
    check('MCPGUARD_PROVIDER_CONFIG' not in env,'gateway configuration does not load native management credentials')
    worker_env={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}
    worker_env|={k:CFG[k] for k in ('MCPGUARD_ENCRYPTION_KEY','MCPGUARD_PLAN_KEY')}
    worker_env|={'MCPGUARD_METADATA_DSN':CFG['provisionerDsn'],'MCPGUARD_PROVIDER_CONFIG':str(STATE/'provider-private.json')}
    process=None;bridge=None
    def start():
        with open(STATE/'provision-process.log','a') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log)
        def ready():
            if p.poll() is not None:raise AssertionError('gateway startup failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop():
        if process and process.poll() is None:process.send_signal(signal.SIGTERM);process.wait(timeout=10)
    def run_worker(org,grant,override=None):
        result=subprocess.run([os.environ['MCPGUARD_TEST_PROVISION_BINARY'],'run',org,grant],cwd=ROOT,env=worker_env|(override or {}),capture_output=True,text=True,timeout=35)
        # Neither failure diagnostics nor successful receipts may expose any connection secret.
        output=result.stdout+result.stderr
        forbidden=[CFG['provisioner_password'],*[s.split('@')[0].split(':')[-1] for s in private['managers'].values()]]
        check(all(s not in output for s in forbidden),'worker output excludes private management secrets')
        return result
    try:
        process=start();alice=wire.HTTPMCP(port,'alice');bob=wire.HTTPMCP(port,'bob');bridge=wire.StdioMCP(port,'alice')
        names={t['name'] for t in alice.call('tools/list')['tools']}
        check(not any(n in names for n in ('provision.run','access.approve','pg.get_item','credential.prepare_download')),'future execution and privileged management tools remain unpublished')
        status,headers,body=wire.http(port,'/console/login',body={'username':'visitor','password':CFG['console_secrets']['visitor']},headers={'Origin':origin})
        check(status==200,'independent reviewer browser login')
        cookie=next(v for k,v in headers.items() if k.lower()=='set-cookie').split(';',1)[0];csrf=json.loads(body)['csrf']
        for index in (1,2):
            rid=f'40000000-0000-4000-8000-{index:012d}';profile=f'50000000-0000-4000-8000-{index:012d}'
            request={'resourceId':rid,'profileId':profile,'requestedExpiry':(datetime.now(timezone.utc)+timedelta(minutes=20)).isoformat().replace('+00:00','Z'),'reason':'isolated RA-05 native provisioning','requestKey':str(uuid.uuid4())}
            submitted=alice.call('tools/call',{'name':'access.request','arguments':request})['structuredContent']
            decision={'requestHash':submitted['requestHash'],'expectedRevision':submitted['revision'],'decision':'APPROVE','approvedExpiry':submitted['requestedExpiry'],'reason':'human reviewed native template','requestKey':str(uuid.uuid4())}
            headers={'Cookie':cookie,'Origin':origin,'X-CSRF-Token':csrf,'If-Match':'"'+submitted['revision']+'"','Idempotency-Key':decision['requestKey']}
            status,_,body=wire.http(port,'/console/api/v2/access-requests/'+submitted['requestId']+'/decisions',body=decision,headers=headers)
            approved=json.loads(body)
            check(status==200 and approved['grant']['state']=='PROVISIONING' and 'accountId' not in approved['grant'],'approval remains provisioning before actual native verification')
            bad=run_worker(submitted['orgId'],approved['grant']['grantId'],{'MCPGUARD_METADATA_DSN':CFG['MCPGUARD_METADATA_DSN']})
            check(bad.returncode!=0,'ordinary gateway database identity cannot run management worker')
            result=run_worker(submitted['orgId'],approved['grant']['grantId'])
            check(result.returncode==0,'separate worker creates and verifies actual PG account')
            job=json.loads(result.stdout);check(job['state']=='VERIFIED','worker reports verified receipt')
            current=alice.call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})['structuredContent']
            check(current['grant']['state']=='ACTIVE' and current['grant']['grantVersion']=='2' and current['grant']['accountId'] and current['grant']['bindingId'],'MCP status links active native account and binding')
            check(current['provisioning']==job,'MCP status agrees with separate worker receipt')
            same=bridge.call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})['structuredContent']
            check(same==current,'stdio observes identical verified activation')
            hidden=bob.call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})
            check(hidden.get('isError',False),'another user cannot inspect activated request')
            replay=run_worker(submitted['orgId'],approved['grant']['grantId'])
            check(replay.returncode==0 and json.loads(replay.stdout)==job,'worker replay returns original receipt')
            grants=alice.call('tools/call',{'name':'access.grants','arguments':{'resourceId':rid}})['structuredContent']['grants']
            check(sum(g['grantId']==current['grant']['grantId'] for g in grants)==1,'grant list does not duplicate immutable activation versions')
            check(not any(s in json.dumps(current) for s in ('secretRef','endpointConfigRef','nativeIdentity','ciphertext','password')),'client status exposes no native secret or connection configuration')
        bridge.close();bridge=None;stop();process=start()
        restored=wire.HTTPMCP(port,'alice').call('tools/call',{'name':'access.status','arguments':{'requestId':submitted['requestId']}})['structuredContent']
        check(restored==current,'gateway restart preserves verified native activation')
        duplicate=run_worker(submitted['orgId'],approved['grant']['grantId']);check(json.loads(duplicate.stdout)==job,'worker process restart preserves once-only intent')
    finally:
        if bridge:bridge.close()
        stop()
    report={'phase':'RA-05','passed':len(CHECKS),'checks':CHECKS,'realDesktopClient':False,'actualNativeProvisioning':True,'existingServiceChanged':False}
    (STATE/'provision-e2e-results.json').write_text(json.dumps(report,indent=2)+'\n')
    print('RA-05 process verification passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
