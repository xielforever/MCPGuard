#!/usr/bin/env python3
"""RA-06 real MCP, separate CLI recovery, SIGKILL and native cleanup proof."""
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
def key():return str(uuid.uuid4())
def sql_json(container,database,sql):
    r=wire.dev.docker('exec','-i',container,'psql','-X','-A','-t','-v','ON_ERROR_STOP=1','-U','postgres','-d',database,input=sql,capture_output=True,text=True)
    return json.loads(r.stdout.strip())
def expire_lease(org,grant):
    org=str(uuid.UUID(org));grant=str(uuid.UUID(grant))
    sql=f"BEGIN;ALTER TABLE mcpguard_ra.provision_recovery DISABLE TRIGGER provision_recovery_guard;UPDATE mcpguard_ra.provision_recovery l SET lease_until=clock_timestamp()-interval '1 second',next_attempt_at=clock_timestamp()-interval '1 second' FROM mcpguard_ra.provisioning_jobs j WHERE (j.org_id,j.job_id)=(l.org_id,l.job_id) AND j.org_id='{org}' AND j.grant_id='{grant}';ALTER TABLE mcpguard_ra.provision_recovery ENABLE TRIGGER provision_recovery_guard;COMMIT;"
    wire.dev.psql(CFG,'mcpguard_meta',sql)
def main():
    port=wire.dev.free_port();origin=f'http://127.0.0.1:{port}'
    env=wire.dev.runtime_env(CFG)|{'MCPGUARD_LISTEN':f'127.0.0.1:{port}','MCPGUARD_RESOURCE_CATALOG':'enabled','MCPGUARD_RESOURCE_ACCESS':'enabled'}
    env.pop('MCPGUARD_NATIVE_CONFIG',None)
    worker_env={k:v for k,v in os.environ.items() if not k.startswith('MCPGUARD_')}
    worker_env|={k:CFG[k] for k in ('MCPGUARD_ENCRYPTION_KEY','MCPGUARD_PLAN_KEY')}
    worker_env|={'MCPGUARD_METADATA_DSN':CFG['provisionerDsn'],'MCPGUARD_PROVIDER_CONFIG':str(STATE/'provider-private.json')}
    process=None;bridge=None;receipts=[];helpers=[]
    def start():
        with open(STATE/'recovery-process.log','a') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_BINARY']],cwd=ROOT,env=env,stdout=log,stderr=log)
        def ready():
            if p.poll() is not None:raise AssertionError('gateway startup failed')
            try:return wire.http(port,'/healthz',method='GET')[0]==200
            except OSError:return False
        wire.until(ready,8);return p
    def stop():
        if process and process.poll() is None:process.send_signal(signal.SIGTERM);process.wait(timeout=10)
    def worker(org,grant,command='recover',override=None):
        result=subprocess.run([os.environ['MCPGUARD_TEST_PROVISION_BINARY'],command,org,grant],env=worker_env|(override or {}),capture_output=True,text=True,timeout=35)
        output=result.stdout+result.stderr
        check(CFG['provisioner_password'] not in output and CFG['MCPGUARD_ENCRYPTION_KEY'] not in output,'worker output excludes private credentials')
        return result,json.loads(result.stdout) if result.stdout.strip() else None
    def helper(org,grant,mode):
        marker=STATE/('crash-marker-'+key()+'.json');log_path=STATE/('crashed-worker-'+key()+'.log')
        helper_env=worker_env|{'MCPGUARD_TEST_PROVISIONER_DSN':CFG['provisionerDsn'],'MCPGUARD_RA_PROVISION_CONFIG':str(STATE/'provider-private.json'),'MCPGUARD_RECOVERY_HELPER':mode,'MCPGUARD_RECOVERY_ORG':org,'MCPGUARD_RECOVERY_GRANT':grant,'MCPGUARD_RECOVERY_MARKER':str(marker)}
        with log_path.open('w') as log:p=subprocess.Popen([os.environ['MCPGUARD_TEST_RECOVERY_PROBE'],'-test.run=^TestRecoveryCrashProcessHelper$'],cwd=ROOT,env=helper_env,stdout=log,stderr=log)
        helpers.append(p)
        deadline=time.monotonic()+12
        while time.monotonic()<deadline:
            if p.poll() is not None:raise AssertionError('recovery checkpoint process failed; inspect private log')
            try:
                record=json.loads(marker.read_text())
                if record.get('jobId'):return p,record
            except (FileNotFoundError,ValueError):pass
            time.sleep(.02)
        p.kill();p.wait();raise AssertionError('checkpoint timeout')
    def kill(p):
        p.kill();p.wait(timeout=5);check(p.returncode==-signal.SIGKILL,'worker process was forcibly killed at the checkpoint')
    try:
        process=start();alice=wire.HTTPMCP(port,'alice');bob=wire.HTTPMCP(port,'bob');bridge=wire.StdioMCP(port,'alice')
        names={t['name'] for t in alice.call('tools/list')['tools']}
        check(not any(n in names for n in ('provision.recover','provision.cleanup','access.approve','pg.get_item','credential.prepare_download')),'MCP does not publish privileged recovery or future execution')
        status,headers,body=wire.http(port,'/console/login',body={'username':'visitor','password':CFG['console_secrets']['visitor']},headers={'Origin':origin})
        check(status==200,'independent human reviewer session')
        cookie=next(v for k,v in headers.items() if k.lower()=='set-cookie').split(';',1)[0];csrf=json.loads(body)['csrf']
        def approved(index):
            request={'resourceId':f'40000000-0000-4000-8000-{index:012d}','profileId':f'50000000-0000-4000-8000-{index:012d}','requestedExpiry':(datetime.now(timezone.utc)+timedelta(minutes=20)).isoformat().replace('+00:00','Z'),'reason':'RA-06 isolated crash recovery','requestKey':key()}
            r=alice.call('tools/call',{'name':'access.request','arguments':request})['structuredContent']
            d={'requestHash':r['requestHash'],'expectedRevision':r['revision'],'decision':'APPROVE','approvedExpiry':r['requestedExpiry'],'reason':'human reviewed bounded recovery fixture','requestKey':key()}
            h={'Cookie':cookie,'Origin':origin,'X-CSRF-Token':csrf,'If-Match':'"'+r['revision']+'"','Idempotency-Key':d['requestKey']}
            status,_,body=wire.http(port,'/console/api/v2/access-requests/'+r['requestId']+'/decisions',body=d,headers=h)
            out=json.loads(body);check(status==200 and out['grant']['state']=='PROVISIONING','approval remains separate from native recovery');return out
        def current(out):return alice.call('tools/call',{'name':'access.status','arguments':{'requestId':out['request']['requestId']}})['structuredContent']
        first=approved(1);org=first['request']['orgId'];grant=first['grant']['grantId']
        p,checkpoint=helper(org,grant,'after_create')
        check(current(first)['provisioning']['state']=='EXECUTING','gateway sees committed dispatch intent before crash')
        early,waiting=worker(org,grant);check(early.returncode!=0 and waiting['state']=='EXECUTING' and waiting['recovery']['attempts']==0,'live dispatch lease cannot be stolen')
        kill(p);expire_lease(org,grant)
        bad,_=worker(org,grant,override={'MCPGUARD_METADATA_DSN':CFG['MCPGUARD_METADATA_DSN']});check(bad.returncode!=0,'ordinary gateway identity cannot run recovery')
        result,job=worker(org,grant);check(result.returncode==0 and job['state']=='VERIFIED','independent CLI recovers account created before SIGKILL')
        active=current(first);check(active['grant']['state']=='ACTIVE' and active['grant']['accountId']==checkpoint['accountId'] and job['accountPlanHash']==checkpoint['planHash'],'recovery activates original account and frozen plan')
        check(bridge.call('tools/call',{'name':'access.status','arguments':{'requestId':first['request']['requestId']}})['structuredContent']==active,'stdio observes identical recovered activation')
        hidden=bob.call('tools/call',{'name':'access.status','arguments':{'requestId':first['request']['requestId']}});check(hidden.get('isError',False),'recovered request remains isolated from another user')
        result,replay=worker(org,grant);check(result.returncode==0 and replay==job,'terminal recovery replay creates no lease or account')
        receipts.append({'case':'created_before_crash','job':job,'accountId':checkpoint['accountId']})
        second=approved(2);org=second['request']['orgId'];grant=second['grant']['grantId'];p,checkpoint=helper(org,grant,'before_dispatch');kill(p);expire_lease(org,grant)
        result,job=worker(org,grant);check(result.returncode==0 and job['state']=='FAILED' and job['recovery']['lastOutcome']=='NATIVE_ABSENT_SEALED','missing native result is sealed without a new creation')
        facts=sql_json(CFG['raPgAdmins'][1]['container'],'mcpguard_business',f"SELECT jsonb_build_object('accounts',(SELECT count(*) FROM mcpguard_provision.accounts WHERE account_id='{uuid.UUID(checkpoint['accountId'])}'),'fence',(SELECT reason FROM mcpguard_provision.account_fences WHERE account_id='{uuid.UUID(checkpoint['accountId'])}'));")
        check(facts=={'accounts':0,'fence':'ABSENT'},'PG proves zero native accounts and a permanent absence fence')
        _,replay=worker(org,grant,'run');check(replay['state']=='FAILED' and replay['jobId']==job['jobId'],'ordinary run cannot re-dispatch the sealed task')
        receipts.append({'case':'crashed_before_creation','job':job,'nativeFacts':facts})
        third=approved(2);org=third['request']['orgId'];grant=third['grant']['grantId'];p,checkpoint=helper(org,grant,'after_create');kill(p)
        subject=str(uuid.UUID(third['grant']['subjectId']));rid=str(uuid.UUID(third['grant']['resourceId']))
        wire.dev.psql(CFG,'mcpguard_meta',f"UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id='{uuid.UUID(org)}' AND principal_id='{subject}' AND resource_id='{rid}';")
        expire_lease(org,grant);result,pending=worker(org,grant);check(result.returncode!=0 and pending['state']=='CLEANUP_PENDING' and current(third)['grant']['state']=='REVOKED','authorization removed before recovery prevents late activation')
        p,_=helper(org,grant,'after_cleanup');kill(p)
        check(current(third)['provisioning']['state']=='CLEANUP_PENDING','cleanup side effect alone does not fabricate a committed receipt')
        expire_lease(org,grant);result,done=worker(org,grant)
        check(result.returncode==0 and done['state']=='CLEANED' and done['recovery']['cleanupAttempts']==2,'cleanup after lost receipt converges through a second bounded claim')
        account=str(uuid.UUID(checkpoint['accountId']))
        facts=sql_json(CFG['raPgAdmins'][1]['container'],'mcpguard_business',f"SELECT jsonb_build_object('login',r.rolcanlogin,'select',has_table_privilege(r.rolname,'resource_demo.items','SELECT'),'fences',(SELECT count(*) FROM mcpguard_provision.account_fences WHERE account_id='{account}')) FROM mcpguard_provision.accounts a JOIN pg_roles r ON r.oid=a.role_oid WHERE a.account_id='{account}';")
        check(facts=={'login':False,'select':False,'fences':1},'PG confirms owned LOGIN and template SELECT removed with one permanent fence')
        closed=current(third);check(closed['grant']['state']=='REVOKED' and closed['provisioning']==done,'cleanup result preserves revoked authorization')
        check(not any(v in json.dumps(closed) for v in ('owner_id','secretRef','endpointConfigRef','nativeIdentity','ciphertext','password')),'client recovery status remains free of native configuration and lease secrets')
        receipts.append({'case':'revoked_and_cleanup_receipt_lost','job':done,'nativeFacts':facts})
        bridge.close();bridge=None;stop();process=start()
        restored=wire.HTTPMCP(port,'alice').call('tools/call',{'name':'access.status','arguments':{'requestId':third['request']['requestId']}})['structuredContent']
        check(restored==closed,'gateway restart preserves cleanup proof and revocation')
    finally:
        for p in helpers:
            if p.poll() is None:p.kill();p.wait(timeout=5)
        if bridge:bridge.close()
        stop()
    report={'phase':'RA-06','passed':len(CHECKS),'checks':CHECKS,'receipts':receipts,'actualProcessKill':True,'actualNativeReconciliationAndCleanup':True,'realDesktopClient':False,'existingServiceChanged':False}
    (STATE/'recovery-e2e-results.json').write_text(json.dumps(report,indent=2)+'\n')
    print('RA-06 process verification passed: '+str(len(CHECKS)))
if __name__=='__main__':main()
