import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {buildPAC,parseBoundHost,proxyEndpoint} from '../src/network-binding.js';

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
