#!/usr/bin/env python3
"""Fresh, owned RA-01/02/03 fixtures. Never reset the live local service or old fixtures."""
import argparse, base64, hashlib, importlib.util, json, os, secrets, socket, subprocess, sys, time, uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BUILD = ROOT / ".dev/ra01-03-build"
IMAGE = "dbhub-local/golang:1.26.9-bookworm"
spec = importlib.util.spec_from_file_location("ra_dev", ROOT / "scripts/dev.py")
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)
POINTER = ROOT / ".dev/ra01-03-latest"

def runner(args, state=None, env=None, capture=True, allow_failure=False):
    options = ["run","--rm","--network","host",
      "--mount",f"type=bind,src={ROOT},dst=/work",
      "--mount","type=volume,src=mcpguard-ra-go-build,dst=/root/.cache/go-build",
      "--mount","type=volume,src=mcpguard-ra-go-modules,dst=/go/pkg/mod",
      "-w","/work","-e","GOTOOLCHAIN=local","-e","GOFLAGS=-mod=readonly -buildvcs=false"]
    if state:
        options += ["--mount",f"type=bind,src={state},dst=/fixture"]
    if env:
        envfile=state/"test.env"
        dev.write_private(envfile,"".join(k+"="+str(v)+"\n" for k,v in env.items() if k.startswith("MCPGUARD_")))
        options += ["--env-file",str(envfile)]
    try:
        return dev.docker(*options,IMAGE,*args,capture_output=capture,text=True)
    except subprocess.CalledProcessError as error:
        destination=state if state else BUILD
        destination.mkdir(parents=True,exist_ok=True)
        log=destination/('command-failure-'+str(time.time_ns())+'.log')
        log.write_text((error.stdout or '')+'\n'+(error.stderr or ''))
        if state is None:
            print((error.stderr or error.stdout or 'Build failed')[-2200:])
        if allow_failure:
            return subprocess.CompletedProcess(error.cmd,error.returncode,error.stdout,error.stderr)
        raise

def build():
    BUILD.mkdir(parents=True,exist_ok=True)
    runner(["sh","-c","python3 scripts/generate_v02_contracts.py --check && go build -p 2 -o .dev/ra01-03-build/mcpguard ./cmd/mcpguard && go build -p 2 -o .dev/ra01-03-build/mcpguard-catalog ./cmd/mcpguard-catalog && go build -p 2 -o .dev/ra01-03-build/mcpguard-connect ./cmd/mcpguard-connect && go build -p 2 -o .dev/ra01-03-build/mcpguard-demo-http ./cmd/mcpguard-demo-http"])

def wait_pg(name):
    for _ in range(150):
        try:
            dev.docker("exec",name,"pg_isready","-q","-h","127.0.0.1","-U","postgres",capture_output=True)
            return
        except subprocess.CalledProcessError:
            pass
        time.sleep(.1)
    raise SystemExit("Isolated PostgreSQL TCP readiness failed")

def admin(cfg,database,sql):
    return dev.psql(cfg,database,sql)

def prepare_legacy_regression(cfg,state,runid):
    """Enable the existing controlled tests only against this new metadata scene."""
    from urllib.parse import urlsplit
    cfg.update({key:secrets.token_hex(24) for key in ("writer_password","http_password","redis_password")})
    cfg.update({"http_token":secrets.token_urlsafe(32),"http_port":dev.free_port(),"redis_port":dev.free_port()})
    sql=""
    for role,key in (("mcpguard_writer","writer_password"),("mcpguard_http","http_password")):
        sql+=f"CREATE ROLE {role} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{cfg[key]}';GRANT CONNECT ON DATABASE mcpguard_business TO {role};"
    admin(cfg,"postgres",sql)
    admin(cfg,"mcpguard_business","""
      GRANT USAGE ON SCHEMA demo TO mcpguard_writer,mcpguard_http;
      GRANT SELECT ON demo.items TO mcpguard_writer;
      GRANT UPDATE(note,version) ON demo.items TO mcpguard_writer;
      CREATE TABLE demo.offices(scope_id text NOT NULL,office_id text NOT NULL,display_name text NOT NULL,PRIMARY KEY(scope_id,office_id));
      CREATE TABLE demo.stock(scope_id text NOT NULL,item_id text NOT NULL,available integer NOT NULL CHECK(available>=0),PRIMARY KEY(scope_id,item_id));
      CREATE TABLE demo.reservations(scope_id text NOT NULL,id text NOT NULL,item_id text NOT NULL,quantity integer NOT NULL CHECK(quantity BETWEEN 1 AND 10),PRIMARY KEY(scope_id,id));
      GRANT SELECT ON demo.offices,demo.stock TO mcpguard_http;
      GRANT UPDATE(available) ON demo.stock TO mcpguard_http;
      GRANT INSERT ON demo.reservations TO mcpguard_http;
    """)
    address=urlsplit(cfg["MCPGUARD_BUSINESS_DSN"]).netloc.rsplit("@",1)[1]
    native={"writerDsn":f"postgres://mcpguard_writer:{cfg['writer_password']}@{address}/mcpguard_business?sslmode=disable",
      "redisAddress":f"127.0.0.1:{cfg['redis_port']}","redisUsername":"mcpguard","redisPassword":cfg["redis_password"],
      "httpBase":f"http://127.0.0.1:{cfg['http_port']}","httpToken":cfg["http_token"]}
    http={"Listen":f"127.0.0.1:{cfg['http_port']}","Token":cfg["http_token"],
      "DSN":f"postgres://mcpguard_http:{cfg['http_password']}@{address}/mcpguard_business?sslmode=disable"}
    dev.write_private(state/"native.json",json.dumps(native))
    dev.write_private(state/"http.json",json.dumps(http))
    dev.write_private(state/"redis.acl",f"user default off\nuser mcpguard on >{cfg['redis_password']} ~demo:*:notes:welcome ~demo:*:notes:help +get +set +ping\n")
    # Redis runs under its own container UID. Only this file is mounted; the
    # host parent remains 0700, so other host users cannot traverse to the ACL.
    os.chmod(state/"redis.acl",0o644)
    redis="mcpguard-ra-redis-"+runid
    dev.docker("run","-d","--name",redis,"--label","mcpguard.fixture=ra01-03","--label","mcpguard.run="+runid,
      "-p",f"127.0.0.1:{cfg['redis_port']}:6379","--mount",f"type=bind,src={state/'redis.acl'},dst=/etc/redis/users.acl,readonly",
      "redis:7.4-alpine","redis-server","--aclfile","/etc/redis/users.acl","--save","","--appendonly","no",capture_output=True)
    cfg["raRedis"]=redis
    cfg["MCPGUARD_NATIVE_CONFIG"]=str(state/"native.json")

def prepare():
    build()
    runid=uuid.uuid4().hex[:10]
    state=Path("/tmp")/("mcpguard-ra01-03-"+runid)
    state.mkdir(mode=0o700)
    cfg={"container":"mcpguard-ra-meta-"+runid,"runId":runid,
      **{k:secrets.token_hex(24) for k in ("admin_password","app_password","reader_password")},
      "MCPGUARD_ENCRYPTION_KEY":base64.b64encode(secrets.token_bytes(32)).decode(),
      "MCPGUARD_PLAN_KEY":base64.b64encode(secrets.token_bytes(32)).decode(),
      "MCPGUARD_AUTH_MODE":"dev-tokens","MCPGUARD_LISTEN":"127.0.0.1:"+str(dev.free_port()),
      **{k+"_token":secrets.token_urlsafe(32) for k in dev.IDS}}
    dev.STATE=state
    dev.write_private(state/"env.json",json.dumps(cfg))
    original=dev.run
    def staged(args,**kwargs):
        args=list(args)
        if args[0]==str(ROOT/"bin/mcpguard"):args[0]=str(BUILD/"mcpguard")
        return original(args,**kwargs)
    dev.run=staged
    dev.build=lambda:None
    dev.up()
    cfg=dev.load()
    prepare_legacy_regression(cfg,state,runid)
    private={"endpoints":{},"secrets":{}}
    bundle={"schemaVersion":"0.2","resources":[],"heads":[],"profiles":[],"tools":[],"access":[],"bindings":[]}
    pg_admins=[]
    for index,label in enumerate(("orders","customers"),1):
        name=f"mcpguard-ra-pg{index}-{runid}"
        password=secrets.token_hex(24)
        envfile=state/f"pg{index}.env"
        dev.write_private(envfile,"POSTGRES_PASSWORD="+password+"\n")
        dev.docker("run","-d","--name",name,"--label","mcpguard.fixture=ra01-03","--label","mcpguard.run="+runid,
          "--env-file",str(envfile),"-p","127.0.0.1::5432",dev.IMAGE,capture_output=True)
        wait_pg(name)
        port=int(dev.inspect(name)["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"])
        native_cfg={"container":name}
        admin(native_cfg,"postgres","CREATE DATABASE mcpguard_business;")
        passwords={user:secrets.token_hex(24) for user in ("alice","bob")}
        sql="CREATE SCHEMA resource_demo;REVOKE ALL ON SCHEMA public,resource_demo FROM PUBLIC;"
        sql+="CREATE TABLE resource_demo.items(owner_name name NOT NULL,item_id text NOT NULL,note text NOT NULL,version bigint NOT NULL,PRIMARY KEY(owner_name,item_id));"
        sql+="ALTER TABLE resource_demo.items ENABLE ROW LEVEL SECURITY;ALTER TABLE resource_demo.items FORCE ROW LEVEL SECURITY;"
        sql+="CREATE POLICY native_owner ON resource_demo.items FOR SELECT TO PUBLIC USING(owner_name=current_user);"
        for user in ("alice","bob"):
            role="ra_"+user
            sql+=f"CREATE ROLE {role} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '{passwords[user]}';"
            sql+=f"GRANT CONNECT ON DATABASE mcpguard_business TO {role};GRANT USAGE ON SCHEMA resource_demo TO {role};GRANT SELECT ON resource_demo.items TO {role};"
            version="9007199254740993" if user=="bob" else "1"
            sql+=f"INSERT INTO resource_demo.items VALUES('{role}','item_demo_1','{label} / {user}',{version});"
        sql+="INSERT INTO resource_demo.items VALUES('ra_bob','item_demo_bob_only','private bob row',1);"
        admin(native_cfg,"mcpguard_business",sql)
        endpoint={"host":"127.0.0.1","port":port,"database":"mcpguard_business","sslMode":"disable"}
        endpoint_ref=f"pg.{label}.v1"
        private["endpoints"][endpoint_ref]=endpoint
        fingerprint=hashlib.sha256(json.dumps(endpoint,sort_keys=True,separators=(",",":")).encode()).hexdigest()
        rid=f"40000000-0000-4000-8000-{index:012d}"
        profile=f"50000000-0000-4000-8000-{index:012d}"
        bundle["resources"].append({"resourceId":rid,"orgId":dev.ORG,"version":"1","kind":"POSTGRES","alias":label,"displayName":label+" PG","endpointConfigRef":endpoint_ref,"targetFingerprint":fingerprint})
        bundle["heads"].append({"orgId":dev.ORG,"resourceId":rid,"version":"1","revision":"1","enabled":True})
        bundle["profiles"].append({"orgId":dev.ORG,"resourceId":rid,"profileId":profile,"version":"1","name":"isolated-read",
          "allowedOperations":["pg.item.read.v1"],"nativeEnforcement":"PG_OWNER_RLS_V1","approvalPolicyRef":"fixture-preprovisioned",
          "timeoutMs":3000,"maxRows":1,"maxResultBytes":16384})
        bundle["tools"].append({"orgId":dev.ORG,"resourceId":rid,"resourceVersion":"1","profileId":profile,"profileVersion":"1",
          "toolName":"fixture.pg_read_item","toolVersion":"1","operationId":"pg.item.read.v1"})
        for u,user in enumerate(("alice","bob"),1):
            secret_ref=f"{label}.{user}"
            private["secrets"][secret_ref+"@1"]={"username":"ra_"+user,"password":passwords[user],"version":"1"}
            bundle["access"].append({"orgId":dev.ORG,"principalId":dev.IDS[user],"resourceId":rid,"canDiscover":True,"canRequest":user=="alice"})
            bundle["bindings"].append({"bindingId":f"60000000-0000-4000-8000-{index*10+u:012d}","orgId":dev.ORG,"principalId":dev.IDS[user],
              "resourceId":rid,"resourceVersion":"1","profileId":profile,"profileVersion":"1","bindingVersion":"1",
              "nativeIdentity":"ra_"+user,"secretRef":secret_ref,"secretVersion":"1","enabled":True})
        pg_admins.append({"container":name,"dsn":f"postgres://postgres:{password}@127.0.0.1:{port}/mcpguard_business?sslmode=disable"})
    # Visible but not executable resource for discovery/request distinction.
    hidden=dict(bundle["resources"][0]);hidden.update({"resourceId":"40000000-0000-4000-8000-000000000003","alias":"request-only","displayName":"Request only PG"})
    bundle["resources"].append(hidden)
    bundle["heads"].append({"orgId":dev.ORG,"resourceId":hidden["resourceId"],"version":"1","revision":"1","enabled":True})
    bundle["access"].append({"orgId":dev.ORG,"principalId":dev.IDS["alice"],"resourceId":hidden["resourceId"],"canDiscover":True,"canRequest":True})
    dev.write_private(state/"ra-secrets.json",json.dumps(private))
    dev.write_private(state/"catalog.json",json.dumps(bundle))
    env=os.environ.copy()
    env["MCPGUARD_MIGRATION_DSN"]=cfg["MCPGUARD_MIGRATION_DSN"]
    for args in (["migrate"],["migrate"],["grant-runtime","mcpguard_app"],["import",str(state/"catalog.json")],["import",str(state/"catalog.json")]):
        subprocess.run([str(BUILD/"mcpguard-catalog"),*args],cwd=ROOT,env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    cfg["raPgAdmins"]=pg_admins
    cfg["raState"]=str(state)
    dev.write_private(state/"env.json",json.dumps(cfg))
    POINTER.parent.mkdir(parents=True,exist_ok=True)
    POINTER.write_text(str(state)+"\n")
    print(json.dumps({"status":"PREPARED","runId":runid,"businessClusters":2,"principals":2,"nativeBindings":4,"privateState":str(state),"liveServiceChanged":False}))

def selected():
    state=Path(POINTER.read_text().strip())
    if state.parent!=Path("/tmp") or not state.name.startswith("mcpguard-ra01-03-"):raise SystemExit("Invalid private fixture path")
    cfg=json.loads((state/"env.json").read_text())
    if cfg["container"]!="mcpguard-ra-meta-"+cfg["runId"]:raise SystemExit("Fixture identity mismatch")
    for record in cfg["raPgAdmins"]:
        info=dev.inspect(record["container"])
        if not info or info["Config"]["Labels"].get("mcpguard.run")!=cfg["runId"]:raise SystemExit("Fixture ownership mismatch")
    return state,cfg

def test():
    state,cfg=selected()
    env=dev.runtime_env(cfg)
    env.update({"MCPGUARD_TEST_METADATA_ADMIN_DSN":cfg["MCPGUARD_MIGRATION_DSN"],
       "MCPGUARD_TEST_BUSINESS_ADMIN_DSN":cfg["MCPGUARD_TEST_BUSINESS_ADMIN_DSN"],
       "MCPGUARD_RA_CONFIG":"/fixture/env.json","MCPGUARD_RA_PRIVATE_CONFIG":"/fixture/ra-secrets.json"})
    if "raRedis" in cfg:
        env["MCPGUARD_NATIVE_CONFIG"]="/fixture/native.json"
    for user in dev.IDS:env["MCPGUARD_TEST_"+user.upper()+"_TOKEN"]=cfg[user+"_token"]
    # Do not reseed. Every rerun has its own log; repeat the whole fixture for a fresh data scene.
    daemon_env=dev.runtime_env(cfg)
    daemon_env["MCPGUARD_DEMO_HTTP_CONFIG"]=str(state/"http.json")
    with open(state/"demo-http.log","a") as log:
        daemon=subprocess.Popen([str(BUILD/"mcpguard-demo-http")],env=daemon_env,stdout=log,stderr=log) if "raRedis" in cfg else None
        try:
            if daemon:
                import urllib.request
                for _ in range(60):
                    try:
                        request=urllib.request.Request(f"http://127.0.0.1:{cfg['http_port']}/healthz",headers={"Authorization":"Bearer "+cfg["http_token"]})
                        urllib.request.urlopen(request,timeout=1).close()
                        break
                    except Exception:time.sleep(.1)
                else:raise SystemExit("Isolated legacy HTTP backend not ready")
            result=runner(["go","test","-race","-p","1","-count=1","-json","./..."],state,env,allow_failure=True)
        finally:
            if daemon:
                daemon.terminate()
                try:daemon.wait(timeout=5)
                except subprocess.TimeoutExpired:daemon.kill();daemon.wait()
    log=state/("go-tests-"+time.strftime("%Y%m%dT%H%M%SZ",time.gmtime())+".jsonl")
    log.write_text(result.stdout+"\n"+result.stderr)
    events=[]
    for line in result.stdout.splitlines():
        if line.startswith("{"):
            try:events.append(json.loads(line))
            except ValueError:pass
    failures=[e.get("Test",e.get("Package")) for e in events if e.get("Action")=="fail"]
    report={"exitCode":result.returncode,"passedTests":sum(e.get("Action")=="pass" and "Test" in e for e in events),
      "skippedTests":[e["Test"] for e in events if e.get("Action")=="skip" and "Test" in e],"failures":failures,"privateEvidence":str(log)}
    print(json.dumps(report))
    if result.returncode:
        for event in events:
            if event.get("Action")=="output" and any(x in event.get("Output","") for x in ("FAIL","resources_integration_test.go","catalog_integration_test.go","native_test.go")):
                print(event.get("Output","").rstrip())
        raise SystemExit(result.returncode)

def contracts():
    state,cfg=selected()
    result=runner(["python3","scripts/generate_v02_contracts.py","--check"],state)
    print(result.stdout.strip())

def wire():
    state,cfg=selected()
    env=dev.runtime_env(cfg)
    env.update({"MCPGUARD_TEST_STATE":str(state),"MCPGUARD_TEST_BINARY":str(BUILD/"mcpguard"),
      "MCPGUARD_TEST_CONNECT_BINARY":str(BUILD/"mcpguard-connect")})
    result=subprocess.run([sys.executable,"tests/e2e.py"],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    log=state/("wire-tests-"+time.strftime("%Y%m%dT%H%M%SZ",time.gmtime())+".log")
    log.write_text(result.stdout)
    if result.returncode:
        raise SystemExit("HTTP/stdio regression failed; private evidence: "+str(log))
    report=json.loads((state/"e2e-results.json").read_text())
    print(json.dumps({"exitCode":0,"checks":len(report["checks"]),"privateEvidence":str(state/"e2e-results.json")}))

def smoke():
    """Prove the production binary's opt-in branch, without publishing a read probe."""
    import signal, urllib.request, urllib.error
    state,cfg=selected()
    env=dev.runtime_env(cfg)
    for key in ("MCPGUARD_NATIVE_CONFIG","MCPGUARD_CONSOLE_CONFIG","MCPGUARD_NOTIFICATION_CONFIG"):
        env.pop(key,None)
    port=dev.free_port()
    env.update({"MCPGUARD_LISTEN":f"127.0.0.1:{port}","MCPGUARD_RESOURCE_CATALOG":"enabled"})
    checks=[]
    session_id=None
    def request(path,token=None,body=None):
        nonlocal session_id
        headers={"Accept":"application/json, text/event-stream","MCP-Protocol-Version":"2025-11-25"}
        if path=="/mcp" and session_id:headers["Mcp-Session-Id"]=session_id
        if token:headers["Authorization"]="Bearer "+token
        if body is not None:headers["Content-Type"]="application/json"
        req=urllib.request.Request(f"http://127.0.0.1:{port}"+path,data=json.dumps(body).encode() if body is not None else None,headers=headers)
        with urllib.request.urlopen(req,timeout=5) as response:
            if response.headers.get("Mcp-Session-Id"):session_id=response.headers["Mcp-Session-Id"]
            data=response.read()
            return json.loads(data) if data else None
    with open(state/"resource-process-smoke.log","a") as log:
        process=subprocess.Popen([str(BUILD/"mcpguard")],env=env,stdout=log,stderr=log)
        try:
            for _ in range(70):
                if process.poll() is not None:raise SystemExit("Opt-in process startup failed; private log retained")
                try:request("/healthz");break
                except (OSError,ValueError):time.sleep(.1)
            else:raise SystemExit("Opt-in process readiness failed")
            checks.append("production binary starts with explicit catalog opt-in")
            for user,count in (("alice",3),("bob",2),("visitor",0)):
                out=request("/api/v2/resources",cfg[user+"_token"])
                assert out["schemaVersion"]=="0.2" and len(out["resources"])==count
                assert all(field not in json.dumps(out) for field in ("password","endpointConfigRef","secretRef","127.0.0.1","ra_alice"))
                checks.append("REST directory current identity and safe view: "+user)
            token=cfg["alice_token"]
            init=request("/mcp",token,{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"ra-production-process-smoke","version":"1"}}})
            assert "result" in init
            request("/mcp",token,{"jsonrpc":"2.0","method":"notifications/initialized"})
            listing=request("/mcp",token,{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}})
            names={tool["name"] for tool in listing["result"]["tools"]}
            assert {"resource.list","resource.get"}<=names
            assert "fixture.pg_read_item" not in names and "pg.get_item" not in names
            checks.append("production MCP exposes discovery and excludes test/future reads")
            result=request("/mcp",token,{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"resource.list","arguments":{}}})["result"]
            assert not result.get("isError",False) and len(result["structuredContent"]["resources"])==3
            checks.append("production MCP resource.list reaches authoritative catalog")
        finally:
            if process.poll() is None:process.send_signal(signal.SIGTERM)
            try:process.wait(timeout=10)
            except subprocess.TimeoutExpired:process.kill();process.wait();raise SystemExit("Opt-in process shutdown exceeded bound")
        assert process.returncode==0
        checks.append("isolated production process shuts down cleanly")
    report={"exitCode":0,"checks":checks,"realDesktopClient":False,"existingServiceChanged":False}
    dev.write_private(state/"resource-process-smoke.json",json.dumps(report,indent=2))
    print(json.dumps({"exitCode":0,"checks":len(checks),"privateEvidence":str(state/"resource-process-smoke.json")}))

if __name__=="__main__":
    parser=argparse.ArgumentParser();parser.add_argument("command",choices=["prepare","test","contracts","wire","smoke"]);args=parser.parse_args()
    try:{"prepare":prepare,"test":test,"contracts":contracts,"wire":wire,"smoke":smoke}[args.command]()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"Isolated command failed: {Path(error.cmd[0]).name}, exit {error.returncode}. Sensitive output withheld; original scene retained.")

