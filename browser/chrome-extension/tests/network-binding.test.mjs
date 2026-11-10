import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {buildPAC,parseBoundHost,proxyEndpoint,proxyAuthDecision,relayProxyFromServerOrigin} from '../src/network-binding.js';

test('PAC sends only approved first-party hosts to relay without DIRECT fallback',()=>{
 const desired={one:{siteOrigin:'https://login.example.com'},two:{siteOrigin:'https://example.net'}};
 const context=vm.createContext({});
 vm.runInContext(buildPAC(desired,proxyEndpoint('relay.example.com',21090))+
   '; this.resolve=FindProxyForURL;',context);
 assert.equal(context.resolve('https://login.example.com','login.example.com'),'HTTPS relay.example.com:21090');
 assert.equal(context.resolve('https://example.net','example.net'),'HTTPS relay.example.com:21090');
 assert.equal(context.resolve('https://sub.login.example.com','sub.login.example.com'),'DIRECT');
 assert.equal(context.resolve('https://other.example.org','other.example.org'),'DIRECT');
 const blocked=vm.createContext({});
 vm.runInContext(buildPAC(desired)+'; this.resolve=FindProxyForURL;',blocked);
 assert.equal(blocked.resolve('https://login.example.com','login.example.com'),'PROXY 127.0.0.1:9');
});
test('network binding accepts HTTP/HTTPS DNS/IP and ports, rejects path and PAC injection',()=>{
 for(const origin of ['ftp://example.com','https://name:pass@example.com',
   'https://example.com/path','https://example.com/?x=1']) {
   assert.throws(()=>parseBoundHost(origin),undefined,origin);
 }
 assert.throws(()=>proxyEndpoint('relay; DIRECT',21090));
 assert.equal(parseBoundHost('https://login.example.com'),'login.example.com');
 for(const [origin,target] of [
  ['http://example.com','example.com:80'],['https://example.com:8443','example.com:8443'],
  ['https://10.99.32.130:30009','10.99.32.130:30009'],['http://localhost:8080','localhost:8080'],
  ['https://a.local','a.local'],['https://[::1]:8443','[::1]:8443']])
  assert.equal(parseBoundHost(origin),target);
});

test('proxy authentication never cancels ordinary website 401 or unrelated proxies',()=>{
 const lease={username:'browser',password:'test-token',host:'relay.example.com',port:21090,expiresAt:1000};
 const ordinary={isProxy:false,scheme:'basic',challenger:{host:'login.example.com',port:443}};
 assert.deepEqual(proxyAuthDecision(ordinary,lease,500),{});
 assert.deepEqual(proxyAuthDecision({...ordinary,isProxy:true,challenger:{host:'unrelated.proxy.com',port:21090}},lease,500),{});
 assert.deepEqual(proxyAuthDecision({...ordinary,isProxy:true,challenger:{host:'relay.example.com',port:21090}},lease,500),
   {authCredentials:{username:'browser',password:'test-token'}});
 assert.deepEqual(proxyAuthDecision({...ordinary,isProxy:true,challenger:{host:'relay.example.com',port:21090}},lease,1000),
   {cancel:true});
});

test('B local Agent native mode and A origin Agent mode can coexist in one PAC',()=>{
 const desired={
  source:{siteOrigin:'https://login.example.com',mode:'source_agent'},
  local:{siteOrigin:'https://app.example.net',mode:'local_agent',port:21291}
 };
 const vmContext=vm.createContext({});
 vm.runInContext(buildPAC(desired,'HTTPS relay.example.com:21090',{port:21291})+
   ';this.resolve=FindProxyForURL;',vmContext);
 assert.equal(vmContext.resolve('https://login.example.com','login.example.com'),'HTTPS relay.example.com:21090');
 assert.equal(vmContext.resolve('https://app.example.net','app.example.net'),'PROXY 127.0.0.1:21291');
 assert.equal(vmContext.resolve('https://else.example.net','else.example.net'),'DIRECT');
 const missing=vm.createContext({});
 vm.runInContext(buildPAC(desired,'HTTPS relay.example.com:21090')+';this.resolve=FindProxyForURL;',missing);
 assert.equal(missing.resolve('https://app.example.net','app.example.net'),'PROXY 127.0.0.1:9');
 assert.throws(()=>buildPAC({
  first:{siteOrigin:'https://app.example.net',mode:'source_agent'},
  second:{siteOrigin:'https://app.example.net',mode:'local_agent',port:21291}
 },'HTTPS relay.example.com:21090',{port:21291}));
});
test('local Agent never receives proxy Basic credentials, remote Relay still does',()=>{
 const oldLocal={host:'127.0.0.1',port:21291,username:'relay-browser',password:'retired-token',
  expiresAt:Number.MAX_SAFE_INTEGER};
 assert.deepEqual(proxyAuthDecision({isProxy:true,scheme:'basic',
  challenger:{host:'127.0.0.1',port:21291}},oldLocal,1000),{});
 const remote={host:'relay.example.com',port:21090,username:'browser',password:'remote-token',
  expiresAt:6000};
 assert.deepEqual(proxyAuthDecision({isProxy:true,scheme:'basic',
  challenger:{host:'relay.example.com',port:21090}},remote,1000),
  {authCredentials:{username:'browser',password:'remote-token'}});
 assert.deepEqual(proxyAuthDecision({isProxy:false,scheme:'basic',
  challenger:{host:'login.example.com',port:443}},remote,1000),{});
});

test('local Agent does not accept or supply proxy authorization',()=>{
 const local={host:'127.0.0.1',port:21291};
 assert.deepEqual(proxyAuthDecision({isProxy:true,scheme:'basic',
   challenger:{host:'127.0.0.1',port:21291}},local,1000),{});
});

test('relay proxy endpoint derives only from already selected HTTPS Server origin',()=>{
  assert.deepEqual(relayProxyFromServerOrigin('https://relay.example.com'),{host:'relay.example.com',port:443});
  assert.deepEqual(relayProxyFromServerOrigin('https://relay.example.com:20000'),{host:'relay.example.com',port:20000});
  for(const unsafe of ['http://relay.example.com','https://relay.example.com:99999',
    'https://relay.example.com/path',
    'https://relay.example.com/?redirect=evil','https://user:pw@relay.example.com'])
    assert.throws(()=>relayProxyFromServerOrigin(unsafe),undefined,unsafe);
});

test('global Chrome Agent proxy bypasses its own Relay WSS control hostname without weakening paired sites',()=>{
 const desired={one:{siteOrigin:'https://login.example.com',mode:'source_agent'}};
 const script=buildPAC(desired,null,null,{enabled:true,port:21291},'https://relay.example.com:20000');
 const context=vm.createContext({});
 vm.runInContext(script+';this.resolve=FindProxyForURL;',context);
 assert.equal(context.resolve('wss://relay.example.com:20000/api/v1/browser-sync/ws','relay.example.com'),'DIRECT');
 assert.equal(context.resolve('https://other.example.net','other.example.net'),'PROXY 127.0.0.1:21291');
 assert.equal(context.resolve('https://login.example.com','login.example.com'),'PROXY 127.0.0.1:9');
});

test('an explicit paired hostname never becomes DIRECT via Relay bootstrap exception',()=>{
 const desired={one:{siteOrigin:'https://relay.example.com',mode:'source_agent'}};
 const script=buildPAC(desired,null,null,{enabled:true,port:21291},'https://relay.example.com:20000');
 const context=vm.createContext({});
 vm.runInContext(script+';this.resolve=FindProxyForURL;',context);
 assert.equal(context.resolve('https://relay.example.com/','relay.example.com'),'PROXY 127.0.0.1:9');
 assert.equal(context.resolve('wss://relay.example.com:20000/','relay.example.com'),'DIRECT');
});


test('PAC scopes DNS/IP network routes to the paired TCP port for HTTP HTTPS and WebSocket',()=>{
 const context=vm.createContext({});
 vm.runInContext(buildPAC({
  ip:{siteOrigin:'https://10.99.32.130:30009'},
  http:{siteOrigin:'http://intranet'},
  v6:{siteOrigin:'https://[::1]:9443',mode:'local_agent',port:21291}
 },'HTTPS 10.99.1.1:21090',{port:21291})+';this.resolve=FindProxyForURL;',context);
 for(const url of ['https://10.99.32.130:30009/a','wss://10.99.32.130:30009/ws'])
  assert.equal(context.resolve(url,'10.99.32.130'),'HTTPS 10.99.1.1:21090');
 for(const url of ['https://10.99.32.130/','http://10.99.32.130:30008/'])
  assert.equal(context.resolve(url,'10.99.32.130'),'DIRECT');
 assert.equal(context.resolve('http://intranet/path','intranet'),'HTTPS 10.99.1.1:21090');
 assert.equal(context.resolve('ws://intranet/ws','intranet'),'HTTPS 10.99.1.1:21090');
 assert.equal(context.resolve('https://intranet/','intranet'),'DIRECT');
 assert.equal(context.resolve('https://[::1]:9443/','[::1]'),'PROXY 127.0.0.1:21291');
 assert.equal(context.resolve('wss://[::1]:9443/','::1'),'PROXY 127.0.0.1:21291');
 assert.equal(context.resolve('https://[::1]/','[::1]'),'DIRECT');
});


test('proxy authentication supports remote IPv4 and IPv6 relay addresses',()=>{
 for(const [host,challenger] of [['127.0.0.1','127.0.0.1'],['10.99.1.1','10.99.1.1'],['[::1]','::1']]){
  const lease={host,port:21090,username:'browser',password:'fixture-token',expiresAt:5000};
  assert.deepEqual(proxyAuthDecision({isProxy:true,scheme:'basic',challenger:{host:challenger,port:21090}},lease,1000),
   {authCredentials:{username:'browser',password:'fixture-token'}});
 }
});
