// Owned HTTPS/IP fixture: reproduce Chrome's Secure-dependent host permission
// filtering, then restore a non-Secure HttpOnly SESSION across real MV3 profiles.
import assert from 'node:assert/strict';
import {createServer} from 'node:https';
import {execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,cp,readFile,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {scope,extensionRoot,extensionContext} from './chromium-e2e.mjs';

async function main(){
 const temp=await mkdtemp(join(tmpdir(),'relayproxy-cookie-access-'));
 scope.push(()=>rm(temp,{recursive:true,force:true}));
 execFileSync('openssl',['req','-x509','-newkey','rsa:2048','-nodes',
  '-keyout',join(temp,'key.pem'),'-out',join(temp,'cert.pem'),'-days','1',
  '-subj','/CN=127.0.0.1','-addext','subjectAltName=IP:127.0.0.1'],{stdio:'ignore'});
 const server=createServer({key:await readFile(join(temp,'key.pem')),cert:await readFile(join(temp,'cert.pem'))},(req,res)=>{
  res.setHeader('Content-Type','application/json');
  res.end(JSON.stringify({session:(req.headers.cookie||'').match(/(?:^|;\s*)SESSION=([^;]*)/)?.[1]||null}));
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 scope.push(()=>new Promise(resolve=>server.close(resolve)));
 const origin='https://127.0.0.1:'+server.address().port;
 const policy={schemaVersion:4,siteOrigin:origin,allCookies:true,localStorage:false,sessionStorage:false,indexedDB:false};
 async function profile(name,schemes){
  const root=join(temp,name);await mkdir(root);
  await cp(join(extensionRoot,'src'),join(root,'src'),{recursive:true});
  const manifest=JSON.parse(await readFile(join(extensionRoot,'manifest.json'),'utf8'));
  manifest.host_permissions=schemes.map(scheme=>scheme+'://127.0.0.1/*');
  await writeFile(join(root,'manifest.json'),JSON.stringify(manifest));
  return extensionContext(root,join(temp,name+'-profile'),{localTestTLS:true});
 }
 const source=await profile('source',['https','http']);
 const restricted=await profile('restricted',['https']);
 const target=await profile('target',['https','http']);
 for(const [p,value] of [[source,'source-fixture-session'],[restricted,'restricted-fixture-session'],[target,'target-fixture-session']]){
  await p.context.addCookies([{domain:'127.0.0.1',path:'/',name:'SESSION',value,secure:false,httpOnly:true,sameSite:'Lax'}]);
 }
 const visible=await restricted.options.evaluate(async()=>chrome.cookies.getAll({domain:'127.0.0.1',partitionKey:{}}));
 assert.equal(visible.length,0,'HTTPS-only host permission must reproduce missing non-Secure SESSION');
 await assert.rejects(restricted.options.evaluate(async policy=>{
  const {captureAllCookies}=await import(chrome.runtime.getURL('src/cookies-all.js'));
  return captureAllCookies(policy);
 },policy),/Cookie 访问权限不完整/);
 const snapshot=await source.options.evaluate(async policy=>{
  const {captureAllCookies}=await import(chrome.runtime.getURL('src/cookies-all.js'));
  return captureAllCookies(policy);
 },policy);
 assert.equal(snapshot.cookies.length,1);
 assert.equal(snapshot.cookies[0].name,'SESSION');
 assert.equal(snapshot.cookies[0].secure,false);
 assert.equal(snapshot.cookies[0].httpOnly,true);
 const apply=options=>target.options.evaluate(async({policy,snapshot,options})=>{
  const {applyAllCookies}=await import(chrome.runtime.getURL('src/cookies-all.js'));
  return applyAllCookies('cookie-access-fixture',snapshot,policy,options);
 },{policy,snapshot,options});
 await assert.rejects(apply({}),/CONFLICT/);
 assert.equal((await target.context.cookies(origin))[0].value,'target-fixture-session');
 assert.equal((await apply({allowOverwrite:true})).status,'APPLIED');
 const restored=(await target.context.cookies(origin))[0];
 assert.equal(restored.value,'source-fixture-session');assert.equal(restored.secure,false);assert.equal(restored.httpOnly,true);
 const page=await target.context.newPage();await page.goto(origin+'/whoami');
 assert.deepEqual(JSON.parse(await page.locator('body').innerText()),{session:'source-fixture-session'});
 // Matching value does not justify skipping differing cookie attributes.
 snapshot.cookies[0]={...snapshot.cookies[0],secure:true,httpOnly:false,sameSite:'strict',session:false,expirationDate:Math.floor(Date.now()/1000)+3600};
 await apply({allowOverwrite:true});
 const updated=(await target.context.cookies(origin))[0];
 assert.equal(updated.secure,true);assert.equal(updated.httpOnly,false);assert.equal(updated.sameSite,'Strict');
 assert.equal(updated.expires,snapshot.cookies[0].expirationDate);
 console.log('Chromium HTTPS/IP Cookie regression passed: missing HTTP permission reproduced; non-Secure HttpOnly SESSION overwritten; website received restored SESSION; same-value attributes restored.');
}
let error;
try{await main();}catch(e){error=e;console.error(e);}finally{
 for(const cleanup of scope.reverse()){try{await cleanup();}catch(e){console.error('cleanup:',e);}}
}
if(error)process.exitCode=1;
