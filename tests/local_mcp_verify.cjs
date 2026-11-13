// Use the official client's configured local MCP connection; no model turn is started.
const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');
const readline = require('node:readline');
const root = path.resolve(__dirname,'..');
const argv = process.argv.slice(2);
const option = (name) => { const index=argv.indexOf(name); return index < 0 ? null : argv[index+1]; };
let threadId = option('--thread');
const output = option('--output');
if (!output) throw new Error('--output is required.');
const repeatAfterMs = Number(option('--repeat-after-ms') || 0);
if (!Number.isInteger(repeatAfterMs) || repeatAfterMs < 0 || repeatAfterMs > 120000) throw new Error('Invalid repeat delay.');
const codex = option('--codex') || 'codex.exe';
const pending = new Map();
let nextId=1;
let stderr='';
const child=cp.spawn(codex,['app-server','--stdio'],{cwd:root,windowsHide:true,stdio:['pipe','pipe','pipe']});
child.stderr.on('data',data=>{stderr+=data.toString();});
const lines=readline.createInterface({input:child.stdout});
lines.on('line',line=>{
  let value; try {value=JSON.parse(line);} catch {return;}
  const item=pending.get(value.id);
  if (!item) {
    if(value.id !== undefined && value.method) child.stdin.write(JSON.stringify({id:value.id,error:{code:-32601,message:'Verification client does not handle interactive requests'}})+'\n');
    return;
  }
  pending.delete(value.id); clearTimeout(item.timer);
  if(value.error) item.reject(new Error(JSON.stringify(value.error))); else item.resolve(value.result);
});
function request(method,params) {
  const id=nextId++;
  return new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>{pending.delete(id);reject(new Error('RPC timed out: '+method));},90000);
    pending.set(id,{resolve,reject,timer});
    child.stdin.write(JSON.stringify({id,method,params})+'\n');
  });
}
(async()=>{
  await request('initialize',{clientInfo:{name:'mcpguard_local_service_verifier',version:'1'},capabilities:{experimentalApi:true}});
  child.stdin.write(JSON.stringify({method:'initialized',params:{}})+'\n');
  if (threadId) {
    await request('thread/resume',{threadId,excludeTurns:true});
  } else {
    // In-memory client context only: no durable chat, agent, or model invocation.
    const context=await request('thread/start',{ephemeral:true,cwd:root,approvalPolicy:'never'});
    threadId=context.thread.id;
  }
  const status=await request('mcpServerStatus/list',{threadId,serverName:'mcpguard_local',detail:'toolsAndAuthOnly'});
  const server=status.data?.find(s=>s.name==='mcpguard_local');
  if (!server || !Object.values(server.tools || {}).some(t=>t.name==='pg.get_demo_item')) throw new Error('Configured local read tool was not discovered.');
  async function read() {
    const result=await request('mcpServer/tool/call',{threadId,server:'mcpguard_local',tool:'pg.get_demo_item',arguments:{itemId:'item_demo_1'}});
    const data=result.structuredContent || JSON.parse(result.content.find(c=>c.type==='text').text);
    if (result.isError || data.requestStatus!=='SUCCEEDED' || data.data?.item?.note!=='Alice scope A') throw new Error('Native local read did not succeed.');
    return result;
  }
  const result=await read();
  const report={capturedAt:new Date().toISOString(),status:'PASS',client:'Official Codex app-server mcpServer/tool/call',threadId,
    endpoint:'http://127.0.0.1:18080/mcp',transport:'Streamable HTTP',server:'mcpguard_local',tool:'pg.get_demo_item',arguments:{itemId:'item_demo_1'},
    officialClientConfiguredToolInvocation:true,ephemeralClientContext:!option('--thread'),cloudPluginUsed:false,modelTurnStarted:false,result};
  if (repeatAfterMs) report.status='WAITING_FOR_REPEAT';
  fs.writeFileSync(path.resolve(output),JSON.stringify(report,null,2)+'\n');
  if (repeatAfterMs) {
    // Keep this same official client context open across the renewal boundary.
    await new Promise(resolve=>setTimeout(resolve,repeatAfterMs));
    report.repeatedResult=await read();
    report.repeatedAt=new Date().toISOString();
    report.sameClientContextAcrossCalls=true;
    report.status='PASS';
    fs.writeFileSync(path.resolve(output),JSON.stringify(report,null,2)+'\n');
  }
  console.log(JSON.stringify(report,null,2));
})().catch(error=>{console.error('Local MCP verification failed: '+error.message);process.exitCode=1;}).finally(()=>{
  for(const item of pending.values())clearTimeout(item.timer);
  const logStamp=new Date().toISOString().replace(/[-:.]/g,'');
  fs.writeFileSync(path.join(root,'.dev/local-service/client-verifier-'+logStamp+'.stderr.log'),stderr,{mode:0o600});
  lines.close(); child.stdin.end(); child.kill();
});
