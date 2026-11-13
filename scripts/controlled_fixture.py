#!/usr/bin/env python3
"""Isolated integration fixture for the controlled-tool batch. Never reseeds live data."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / '.dev/controlled-test-v2-2026-10-23'
BUILD = ROOT / '.dev/controlled-build'
spec = importlib.util.spec_from_file_location('dev', ROOT / 'scripts/dev.py')
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)

def prepare():
    BUILD.mkdir(parents=True, exist_ok=True)
    dev.STATE = STATE
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not (STATE / 'env.json').exists():
        cfg = {'container': 'mcpguard-controlled-test-v2-20261023',
               **{k: secrets.token_hex(24) for k in ('admin_password', 'app_password', 'reader_password')},
               'MCPGUARD_ENCRYPTION_KEY': base64.b64encode(secrets.token_bytes(32)).decode(),
               'MCPGUARD_PLAN_KEY': base64.b64encode(secrets.token_bytes(32)).decode(),
               'MCPGUARD_AUTH_MODE': 'dev-tokens', 'MCPGUARD_LISTEN': '127.0.0.1:' + str(dev.free_port()),
               **{name+'_token': secrets.token_urlsafe(32) for name in dev.IDS}}
        dev.write_private(STATE/'env.json', json.dumps(cfg))
    cfg=dev.load()
    if cfg['container']!='mcpguard-controlled-test-v2-20261023':
        raise SystemExit('Isolated fixture identity mismatch')
    original=dev.run
    def staged(args, **kwargs):
        args=list(args)
        if args[0]==str(ROOT/'bin/mcpguard'): args[0]=str(BUILD/'mcpguard')
        return original(args,**kwargs)
    dev.run=staged
    dev.build=lambda:None
    dev.up()
    cfg=dev.load()
    cfg.setdefault('writer_password',secrets.token_hex(24))
    cfg.setdefault('http_password',secrets.token_hex(24))
    cfg.setdefault('http_token',secrets.token_urlsafe(32))
    cfg.setdefault('console_secrets',{name:secrets.token_urlsafe(32) for name in dev.IDS})
    cfg.setdefault('redis_password',secrets.token_hex(24))
    cfg.setdefault('http_port',dev.free_port())
    cfg.setdefault('redis_port',dev.free_port())
    for role,key in [('mcpguard_writer','writer_password'),('mcpguard_http','http_password')]:
        dev.psql(cfg,'postgres',f"DO $$ BEGIN IF NOT EXISTS(SELECT FROM pg_roles WHERE rolname='{role}') THEN CREATE ROLE {role} LOGIN; END IF; END $$; ALTER ROLE {role} WITH NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg[key]}'; GRANT CONNECT ON DATABASE mcpguard_business TO {role};")
    dev.psql(cfg,'mcpguard_business',"""
    GRANT USAGE ON SCHEMA demo TO mcpguard_writer,mcpguard_http;
    GRANT SELECT ON demo.items TO mcpguard_writer;
    GRANT UPDATE(note,version) ON demo.items TO mcpguard_writer;
    CREATE TABLE IF NOT EXISTS demo.offices(scope_id text NOT NULL,office_id text NOT NULL,display_name text NOT NULL,PRIMARY KEY(scope_id,office_id));
    CREATE TABLE IF NOT EXISTS demo.stock(scope_id text NOT NULL,item_id text NOT NULL,available integer NOT NULL CHECK(available>=0),PRIMARY KEY(scope_id,item_id));
    CREATE TABLE IF NOT EXISTS demo.reservations(scope_id text NOT NULL,id text NOT NULL,item_id text NOT NULL,quantity integer NOT NULL CHECK(quantity BETWEEN 1 AND 10),PRIMARY KEY(scope_id,id));
    INSERT INTO demo.offices VALUES('scope_a','office-demo','Alice demo office'),('scope_b','office-demo','Bob demo office') ON CONFLICT DO NOTHING;
    INSERT INTO demo.stock VALUES('scope_a','item_demo_1',1000),('scope_b','item_demo_1',1000) ON CONFLICT DO NOTHING;
    GRANT SELECT ON demo.offices,demo.stock TO mcpguard_http;
    GRANT UPDATE(available) ON demo.stock TO mcpguard_http;
    GRANT INSERT ON demo.reservations TO mcpguard_http;
    """)
    sql=f"INSERT INTO mcpguard.control_heads(org_id) VALUES('{dev.ORG}') ON CONFLICT DO NOTHING;"
    for i,resource in enumerate(['demo.pg','demo.redis','demo.directory','demo.inventory'],1):
        sql+=f"INSERT INTO mcpguard.control_resources VALUES('{dev.ORG}','{resource}','30000000-0000-4000-8000-{i:012d}',true) ON CONFLICT DO NOTHING;"
        sql+=f"INSERT INTO mcpguard.reviewer_grants VALUES('{dev.ORG}','{dev.IDS['visitor']}','{resource}',true) ON CONFLICT DO NOTHING;"
    for name in ['alice','bob']:
        for tool in ['pg.update_demo_note','redis.get_demo_note','redis.set_demo_note','directory.get_demo_office','inventory.reserve_demo']:
            sql+=f"INSERT INTO mcpguard.tool_grants VALUES('{dev.ORG}','{dev.IDS[name]}','{tool}',true) ON CONFLICT DO NOTHING;"
    sql+=f"INSERT INTO mcpguard.control_members VALUES('{dev.ORG}','{dev.IDS['visitor']}',true) ON CONFLICT DO NOTHING;"
    dev.psql(cfg,'mcpguard_meta',sql)
    from urllib.parse import urlsplit
    addr=urlsplit(cfg['MCPGUARD_BUSINESS_DSN']).netloc.rsplit('@',1)[1]
    native={'writerDsn':f"postgres://mcpguard_writer:{cfg['writer_password']}@{addr}/mcpguard_business?sslmode=disable",
            'redisAddress':f"127.0.0.1:{cfg['redis_port']}",'redisUsername':'mcpguard','redisPassword':cfg['redis_password'],
            'httpBase':f"http://127.0.0.1:{cfg['http_port']}",'httpToken':cfg['http_token']}
    http_cfg={'Listen':f"127.0.0.1:{cfg['http_port']}",'Token':cfg['http_token'],
              'DSN':f"postgres://mcpguard_http:{cfg['http_password']}@{addr}/mcpguard_business?sslmode=disable"}
    logins=[{'Username':name,'OrgID':dev.ORG,'PrincipalID':ident,'PasswordHash':hashlib.sha256(cfg['console_secrets'][name].encode()).hexdigest()} for name,ident in dev.IDS.items()]
    for name,v in [('native.json',native),('http.json',http_cfg),('console.json',logins)]:
        dev.write_private(STATE/name,json.dumps(v))
    cfg.update({'MCPGUARD_NATIVE_CONFIG':str(STATE/'native.json'),'MCPGUARD_CONSOLE_CONFIG':str(STATE/'console.json')})
    dev.write_private(STATE/'env.json',json.dumps(cfg,indent=2))
    # Restricted Redis ACL: fixed namespace and only GET, SET and health PING.
    dev.write_private(STATE/'redis.acl',f"user default off\nuser mcpguard on >{cfg['redis_password']} ~demo:*:notes:welcome ~demo:*:notes:help +get +set +ping\n")
    info=dev.inspect('mcpguard-controlled-test-redis-v2-20261023')
    if info is None:
        dev.docker('run','-d','--name','mcpguard-controlled-test-redis-v2-20261023','--label','mcpguard.fixture=controlled-test','-p',f"127.0.0.1:{cfg['redis_port']}:6379",'--mount',f"type=bind,src={STATE/'redis.acl'},dst=/etc/redis/users.acl,readonly",'redis:7.4-alpine','redis-server','--aclfile','/etc/redis/users.acl','--save','','--appendonly','no',capture_output=True)
    elif info['Config']['Labels'].get('mcpguard.fixture')!='controlled-test':
        raise SystemExit('Redis container owner mismatch')
    elif not info['State']['Running']:dev.docker('start',info['Name'],capture_output=True)
    print('Controlled fixture prepared. Independent console reviewer is visitor; all data is isolated. No secrets printed.')

def test():
    dev.STATE=STATE;cfg=dev.load();env=dev.runtime_env(cfg)
    env.update({k:v for k,v in cfg.items() if k in ['MCPGUARD_NATIVE_CONFIG','MCPGUARD_CONSOLE_CONFIG']})
    env['MCPGUARD_DEMO_HTTP_CONFIG']=str(STATE/'http.json')
    env['MCPGUARD_TEST_METADATA_ADMIN_DSN']=cfg['MCPGUARD_MIGRATION_DSN']
    env['MCPGUARD_TEST_BUSINESS_ADMIN_DSN']=cfg['MCPGUARD_TEST_BUSINESS_ADMIN_DSN']
    env['MCPGUARD_TEST_BINARY']=str(BUILD/'mcpguard')
    env['MCPGUARD_TEST_STATE']=str(STATE)
    for name in dev.IDS:env['MCPGUARD_TEST_'+name.upper()+'_TOKEN']=cfg[name+'_token']
    with open(STATE/'http.log','a') as log:
        p=subprocess.Popen([str(BUILD/'mcpguard-demo-http')],env=env,stdout=log,stderr=log)
        try:
            import urllib.request
            for _ in range(60):
                try:
                    req=urllib.request.Request(f"http://127.0.0.1:{cfg['http_port']}/healthz",headers={'Authorization':'Bearer '+cfg['http_token']})
                    urllib.request.urlopen(req,timeout=1).close();break
                except Exception:time.sleep(.1)
            else:raise SystemExit('Isolated demo HTTP backend did not become ready')
            result=subprocess.run(['go','test','-race','-p','2','-count=1','-json','./...'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
            if (STATE/'go-tests.jsonl').exists():(STATE/('go-tests-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime())+'.jsonl')).write_text((STATE/'go-tests.jsonl').read_text())
            (STATE/'go-tests.jsonl').write_text(result.stdout)
            events=[json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
            failed=[e.get('Test',e['Package']) for e in events if e.get('Action')=='fail']
            for e in events:
                if e.get('Action')=='output' and ('FAIL' in e.get('Output','') or e.get('Package','').endswith('/controlled') and 'error' in e.get('Output','').lower()):print(e.get('Output','').rstrip())
            print(json.dumps({'exitCode':result.returncode,'passedTests':sum(e.get('Action')=='pass' and 'Test'in e for e in events),'failures':failed,'evidence':str(STATE/'go-tests.jsonl')}))
            if result.returncode:raise SystemExit(result.returncode)
            wire=subprocess.run([sys.executable,'tests/e2e.py'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
            (STATE/'wire-tests.log').write_text(wire.stdout)
            if wire.returncode:
                print('HTTP/stdio regression failed; details retained in private fixture evidence.')
                print(wire.stdout[-1800:])
                raise SystemExit(wire.returncode)
            print('HTTP/stdio regression passed; structured evidence: '+str(STATE/'e2e-results.json'))
        finally:
            p.terminate()
            try:p.wait(timeout=5)
            except subprocess.TimeoutExpired:p.kill();p.wait()

def ui_up():
    dev.STATE=STATE;cfg=dev.load();runtime=dev.runtime_env(cfg)
    selected={k:v for k,v in runtime.items() if k.startswith('MCPGUARD_')}
    dev.write_private(STATE/'ui.env',''.join(k+'='+v+'\n' for k,v in selected.items()))
    image=dev.inspect('mcpguard-local-runner')['Image']
    for name,binary,env_args in [
        ('mcpguard-controlled-ui-http','mcpguard-demo-http',['-e','MCPGUARD_DEMO_HTTP_CONFIG='+str(STATE/'http.json')]),
        ('mcpguard-controlled-ui-gateway','mcpguard',['--env-file',str(STATE/'ui.env')])]:
        info=dev.inspect(name)
        if info:
            if info['Config'].get('Labels',{}).get('mcpguard.fixture')!='controlled-ui':raise SystemExit('UI fixture owner mismatch')
            if not info['State']['Running']:dev.docker('start',name,capture_output=True)
        else:
            dev.docker('run','-d','--name',name,'--label','mcpguard.fixture=controlled-ui','--network','host','--mount',f'type=bind,src={ROOT},dst={ROOT},readonly',*env_args,image,str(BUILD/binary),capture_output=True)
    print('Isolated UI fixture ready at http://'+cfg['MCPGUARD_LISTEN']+'/web/ ; login secrets remain private.')

if __name__=='__main__':
    try:
        {'prepare':prepare,'test':test,'ui-up':ui_up}[sys.argv[1]]()
    except subprocess.CalledProcessError as e:
        raise SystemExit(f'Controlled fixture command failed: {Path(e.cmd[0]).name}, exit {e.returncode}; sensitive output withheld.')
