#!/usr/bin/env python3
"""Install the controlled local demo profile without reseeding existing items.

Runs under WSL. Stops/replaces only owned gateway/backend containers; Codex is
never restarted. Private backups and old containers are retained for diagnosis.
"""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlsplit

import dev
import local_service as service

ROOT=dev.ROOT
STATE=service.STATE
BUILD=ROOT/'.dev/controlled-build'
OPERATOR='20000000-0000-4000-8000-000000000004'
EXTRAS=[{'name':'mcpguard-local-redis','kind':'redis'}, {'name':'mcpguard-local-demo-http','kind':'demo-http'}]

def ready_port(port):
    for _ in range(80):
        try:
            with socket.create_connection(('127.0.0.1',port),timeout=.5):return
        except OSError:time.sleep(.1)
    raise RuntimeError('Owned backend did not open its loopback port')

def redis_command(cfg,*args):
    def write(conn,parts):
        conn.sendall(('*'+str(len(parts))+'\r\n').encode()+b''.join(('$'+str(len(p.encode()))+'\r\n'+p+'\r\n').encode() for p in parts))
    with socket.create_connection(('127.0.0.1',18081),timeout=2) as conn:
        conn.settimeout(2)
        stream=conn.makefile('rb')
        write(conn,['AUTH','mcpguard',cfg['controlled_redis_password']])
        if stream.readline()!=b'+OK\r\n':raise RuntimeError('Owned Redis authentication failed')
        write(conn,list(args))
        line=stream.readline(128)
        if not line or line.startswith(b'-'):raise RuntimeError('Owned Redis rejected fixture initialization')

def create_container(name,kind,args):
    info=dev.inspect(name)
    if info:
        service.owned(info,kind)
        if not info['State']['Running']:service.docker('start',name)
        return
    service.docker('run','-d','--name',name,'--restart','unless-stopped','--label',service.LABEL+'='+kind,*args)

def setup():
    cfg=dev.load();service.validate_fixture(cfg)
    if not (BUILD/'mcpguard').is_file() or not (BUILD/'mcpguard-demo-http').is_file():raise RuntimeError('Build and test both controlled binaries first')
    if not (STATE/'service.json').is_file():raise RuntimeError('Existing managed local service is required')
    # No ports, credentials or targets are accepted from MCP arguments.
    cfg.setdefault('controlled_writer_password',secrets.token_hex(24))
    cfg.setdefault('controlled_http_password',secrets.token_hex(24))
    cfg.setdefault('controlled_http_token',secrets.token_urlsafe(32))
    cfg.setdefault('controlled_redis_password',secrets.token_hex(24))
    login_path=ROOT/'.dev/local-console-login.json'
    credentials=json.loads(login_path.read_text()) if login_path.exists() else {name:secrets.token_urlsafe(32) for name in ('alice','operator')}
    backup=STATE/('before-controlled-'+service.stamp().lower());backup.mkdir(mode=0o700)
    runtime=STATE/'controlled-runtime';runtime.mkdir(mode=0o700,exist_ok=True)
    with service.operation_lock():
        db,gateway=dev.inspect(service.DB),dev.inspect(service.GATEWAY)
        if not db or not gateway:raise RuntimeError('Managed local containers are missing')
        service.owned(db,'database');service.owned(gateway,'gateway')
        if not db['State']['Running']:service.docker('start',service.DB)
        service.ready_db()
        shutil.copy2(ROOT/'.dev/env.json',backup/'env.json')
        shutil.copy2(ROOT/'bin/mcpguard',backup/'mcpguard')
        if (STATE/'runtime.env').exists():shutil.copy2(STATE/'runtime.env',backup/'runtime.env')
        with (backup/'metadata-before.dump').open('wb') as dump:
            dev.docker('exec',service.DB,'pg_dump','-U','postgres','-d','mcpguard_meta','-Fc',stdout=dump,stderr=subprocess.PIPE)
        before=service.counts();service.save(backup/'ledger-before.json',before)
        if gateway['State']['Running']:service.docker('stop',service.GATEWAY)
        try:
            env=dev.runtime_env(cfg);env['MCPGUARD_MIGRATION_DSN']=cfg['MCPGUARD_MIGRATION_DSN']
            subprocess.run([str(BUILD/'mcpguard'),'migrate'],env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
            dev.psql(cfg,'mcpguard_meta',(ROOT/'scripts/fixtures/grants.sql').read_text())
            for role,key in [('mcpguard_writer','controlled_writer_password'),('mcpguard_http','controlled_http_password')]:
                dev.psql(cfg,'postgres',f"DO $$ BEGIN IF NOT EXISTS(SELECT FROM pg_roles WHERE rolname='{role}') THEN CREATE ROLE {role} LOGIN; END IF; END $$; ALTER ROLE {role} WITH NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg[key]}'; GRANT CONNECT ON DATABASE mcpguard_business TO {role};")
            # Only new demo tables and absent rows are inserted. Never run business.sql here.
            dev.psql(cfg,'mcpguard_business',"""
            GRANT USAGE ON SCHEMA demo TO mcpguard_writer,mcpguard_http;
            GRANT SELECT ON demo.items TO mcpguard_writer; GRANT UPDATE(note,version) ON demo.items TO mcpguard_writer;
            CREATE TABLE IF NOT EXISTS demo.offices(scope_id text NOT NULL,office_id text NOT NULL,display_name text NOT NULL,PRIMARY KEY(scope_id,office_id));
            CREATE TABLE IF NOT EXISTS demo.stock(scope_id text NOT NULL,item_id text NOT NULL,available integer NOT NULL CHECK(available>=0),PRIMARY KEY(scope_id,item_id));
            CREATE TABLE IF NOT EXISTS demo.reservations(scope_id text NOT NULL,id text NOT NULL,item_id text NOT NULL,quantity integer NOT NULL CHECK(quantity BETWEEN 1 AND 10),PRIMARY KEY(scope_id,id));
            GRANT SELECT ON demo.offices,demo.stock TO mcpguard_http; GRANT UPDATE(available) ON demo.stock TO mcpguard_http; GRANT INSERT ON demo.reservations TO mcpguard_http;
            INSERT INTO demo.offices SELECT DISTINCT scope_id,'office-demo','Local demo office' FROM demo.items ON CONFLICT DO NOTHING;
            INSERT INTO demo.stock SELECT scope_id,item_id,100 FROM demo.items ON CONFLICT DO NOTHING;
            """)
            op_token_hash=hashlib.sha256(secrets.token_bytes(32)).hexdigest()
            sql=f"BEGIN; SELECT org_id FROM mcpguard.authorization_heads WHERE org_id='{dev.ORG}' FOR UPDATE; INSERT INTO mcpguard.control_heads(org_id) VALUES('{dev.ORG}') ON CONFLICT DO NOTHING;"
            sql+=f"INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,can_read,scope_id) VALUES('{dev.ORG}','{OPERATOR}','Local operator (demo)',decode('{op_token_hash}','hex'),clock_timestamp()+interval '90 days',false,'scope_a') ON CONFLICT DO NOTHING;"
            sql+=f"INSERT INTO mcpguard.control_members VALUES('{dev.ORG}','{OPERATOR}',true) ON CONFLICT DO NOTHING;"
            for i,resource in enumerate(['demo.pg','demo.redis','demo.directory','demo.inventory'],1):
                sql+=f"INSERT INTO mcpguard.control_resources VALUES('{dev.ORG}','{resource}','30000000-0000-4000-8000-{i:012d}',true) ON CONFLICT DO NOTHING; INSERT INTO mcpguard.reviewer_grants VALUES('{dev.ORG}','{OPERATOR}','{resource}',true) ON CONFLICT DO NOTHING;"
            for tool in ['pg.update_demo_note','redis.get_demo_note','redis.set_demo_note','directory.get_demo_office','inventory.reserve_demo']:
                sql+=f"INSERT INTO mcpguard.tool_grants VALUES('{dev.ORG}','{dev.IDS['alice']}','{tool}',true) ON CONFLICT DO NOTHING;"
            sql+="COMMIT;"
            dev.psql(cfg,'mcpguard_meta',sql)
            addr=urlsplit(cfg['MCPGUARD_BUSINESS_DSN']).netloc.rsplit('@',1)[1]
            native={'writerDsn':f"postgres://mcpguard_writer:{cfg['controlled_writer_password']}@{addr}/mcpguard_business?sslmode=disable",'redisAddress':'127.0.0.1:18081','redisUsername':'mcpguard','redisPassword':cfg['controlled_redis_password'],'httpBase':'http://127.0.0.1:18082','httpToken':cfg['controlled_http_token']}
            backend={'Listen':'127.0.0.1:18082','Token':cfg['controlled_http_token'],'DSN':f"postgres://mcpguard_http:{cfg['controlled_http_password']}@{addr}/mcpguard_business?sslmode=disable"}
            console=[{'Username':name,'OrgID':dev.ORG,'PrincipalID':dev.IDS['alice'] if name=='alice' else OPERATOR,'PasswordHash':hashlib.sha256(password.encode()).hexdigest()} for name,password in credentials.items()]
            for name,data in [('native.json',native),('console.json',console)]:dev.write_private(runtime/name,json.dumps(data))
            dev.write_private(STATE/'demo-http.json',json.dumps(backend))
            dev.write_private(login_path,json.dumps(credentials,indent=2))
            dev.write_private(STATE/'redis.acl',f"user default off\nuser mcpguard on >{cfg['controlled_redis_password']} ~demo:*:notes:welcome ~demo:*:notes:help +get +set +ping\n")
            cfg.update(MCPGUARD_NATIVE_CONFIG=str(runtime/'native.json'),MCPGUARD_CONSOLE_CONFIG=str(runtime/'console.json'))
            dev.write_private(ROOT/'.dev/env.json',json.dumps(cfg,indent=2))
            http_info=dev.inspect('mcpguard-local-demo-http')
            if http_info:
                service.owned(http_info,'demo-http')
                if http_info['State']['Running']:service.docker('stop','mcpguard-local-demo-http')
            shutil.copy2(BUILD/'mcpguard',ROOT/'bin/mcpguard')
            shutil.copy2(BUILD/'mcpguard-demo-http',ROOT/'bin/mcpguard-demo-http')
            uid=str(os.getuid());gid=str(os.getgid())
            redis_info=dev.inspect('mcpguard-local-redis')
            if redis_info:
                service.owned(redis_info,'redis')
                mounts=redis_info.get('HostConfig',{}).get('Mounts',[])
                no_copy=any(m.get('Target')=='/data' and m.get('VolumeOptions',{}).get('NoCopy') for m in mounts)
                if not no_copy:
                    if redis_info['State']['Running'] or redis_info['State']['Status']=='restarting':service.docker('stop','mcpguard-local-redis')
                    service.docker('rename','mcpguard-local-redis','mcpguard-local-redis-before-controlled-'+service.stamp().lower())
            if dev.inspect('mcpguard-local-redis') is None:
                volume=subprocess.run(['docker','--host=unix:///var/run/docker.sock','volume','inspect','mcpguard-local-redis-data'],capture_output=True,text=True)
                if volume.returncode==0:
                    if json.loads(volume.stdout)[0].get('Labels',{}).get(service.LABEL)!='redis':raise RuntimeError('Redis volume belongs to another workload')
                else:service.docker('volume','create','--label',service.LABEL+'=redis','mcpguard-local-redis-data')
                service.docker('run','--rm','--mount','type=volume,src=mcpguard-local-redis-data,dst=/data,volume-nocopy','--entrypoint','chown','redis:7.4-alpine',uid+':'+gid,'/data')
            create_container('mcpguard-local-redis','redis',['--user',uid+':'+gid,'--cap-drop','ALL','--security-opt','no-new-privileges','-p','127.0.0.1:18081:6379','--mount',f"type=bind,src={STATE/'redis.acl'},dst=/etc/redis/users.acl,readonly",'--mount','type=volume,src=mcpguard-local-redis-data,dst=/data,volume-nocopy','redis:7.4-alpine','redis-server','--aclfile','/etc/redis/users.acl','--appendonly','yes','--appendfsync','everysec'])
            ready_port(18081)
            for scope in ['scope_a','scope_b']:
                redis_command(cfg,'SET',f'demo:{scope}:notes:welcome','Local demo welcome','NX')
            create_container('mcpguard-local-demo-http','demo-http',['--user',uid+':'+gid,'--network','host','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','-e','MCPGUARD_DEMO_HTTP_CONFIG=/run/demo-http.json','--mount',f"type=bind,src={STATE/'demo-http.json'},dst=/run/demo-http.json,readonly",'--mount',f"type=bind,src={ROOT/'bin/mcpguard-demo-http'},dst=/usr/local/bin/mcpguard-demo-http,readonly",gateway['Image'],'/usr/local/bin/mcpguard-demo-http'])
            ready_port(18082)
            container_cfg={**cfg,'MCPGUARD_NATIVE_CONFIG':'/run/mcpguard/native.json','MCPGUARD_CONSOLE_CONFIG':'/run/mcpguard/console.json'}
            if cfg.get('MCPGUARD_NOTIFICATION_CONFIG'):container_cfg['MCPGUARD_NOTIFICATION_CONFIG']='/run/mcpguard/telegram.json'
            dev.write_private(STATE/'runtime.env',service.runtime_text(container_cfg))
            archived=service.GATEWAY+'-before-controlled-'+service.stamp().lower();service.docker('rename',service.GATEWAY,archived)
            create_container(service.GATEWAY,'gateway',['--user',uid+':'+gid,'--network','host','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--env-file',str(STATE/'runtime.env'),'--mount',f"type=bind,src={ROOT/'bin/mcpguard'},dst=/usr/local/bin/mcpguard,readonly",'--mount',f"type=bind,src={runtime},dst=/run/mcpguard,readonly",gateway['Image'],'/usr/local/bin/mcpguard'])
            service.save(STATE/'controlled-services.json',{'containers':EXTRAS,'backup':str(backup),'archivedGateway':archived})
            for _ in range(60):
                if service.health():break
                time.sleep(.25)
            else:raise RuntimeError('New local gateway did not become live; private backup retained')
            after=service.counts()
            if before!=after:raise RuntimeError('Historical read ledger changed unexpectedly during installation')
            report={'deployedAt':datetime.now(timezone.utc).isoformat(),'endpoint':'http://127.0.0.1:18080/mcp','console':'http://127.0.0.1:18080/web/','binarySha256':hashlib.sha256((ROOT/'bin/mcpguard').read_bytes()).hexdigest(),'oldReadLedgerPreserved':True,'ledgerBefore':before,'ledgerAfter':after,'notification':'TELEGRAM_CREDENTIALS_PENDING','sso':'PROVIDER_SELECTION_PENDING','nativeVerification':'PENDING_FINAL_UNIFIED_NATIVE_CALLS','backupDirectory':str(backup)}
            service.save(STATE/'controlled-deployment.json',report)
            print(json.dumps(report,indent=2))
        except BaseException:
            # Do not run an old binary against a new schema or restore a database
            # automatically. Evidence remains available for a forward repair.
            raise

if __name__=='__main__':
    if sys.argv[1:]!=['setup']:raise SystemExit('Usage: python3 scripts/local_controlled.py setup')
    try:setup()
    except (OSError,subprocess.SubprocessError,RuntimeError) as e:
        diagnostic={'at':service.stamp(),'errorType':type(e).__name__}
        if isinstance(e,RuntimeError):diagnostic['safeReason']=str(e)
        service.save(STATE/('controlled-install-failure-'+service.stamp()+'.json'),diagnostic)
        print('Controlled installation failed: '+type(e).__name__+'. Private diagnostics and backups retained; secrets suppressed.',file=sys.stderr)
        raise SystemExit(1)
