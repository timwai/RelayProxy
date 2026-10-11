import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {buildPAC,parseBoundHost,proxyEndpoint,proxyAuthDecision} from '../src/network-binding.js';

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
test('network binding rejects IP, custom port, localhost, path and PAC injection',()=>{
 for(const origin of ['https://127.0.0.1','https://example.com:8443','http://example.com',
   'https://localhost','https://a.local','https://name:pass@example.com',
   'https://example.com/path','https://example.com/?x=1']) {
   assert.throws(()=>parseBoundHost(origin),undefined,origin);
 }
 assert.throws(()=>proxyEndpoint('relay; DIRECT',21090));
 assert.equal(parseBoundHost('https://login.example.com'),'login.example.com');
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
