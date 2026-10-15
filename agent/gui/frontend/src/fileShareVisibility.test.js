import test from 'node:test';
import assert from 'node:assert/strict';
import {onlyVerifiedFilePeers} from './fileShareVisibility.js';

test('hosts with files.host approval but no directory ACL are hidden',()=>{
 const raw=[
  {deviceId:'approved-only',online:true,name:'No folders'}, // old Server/Agent
  {deviceId:'no-share',online:true,shares:[]},
  {deviceId:'revoked',online:true,shares:[{id:'a',permission:'none'}]},
  {deviceId:'granted',online:true,shares:[{id:'folder-1',permission:'read'}]},
  {deviceId:'granted',online:true,shares:[{id:'folder-1',permission:'read'}]},
  {deviceId:'offline',online:false,shares:[{id:'folder-2',permission:'read_write'}]},
 ];
 assert.deepEqual(onlyVerifiedFilePeers(raw).map(p=>p.deviceId),['granted']);
});
test('a host remains visible when at least one read-write folder is authorized',()=>{
 const result=onlyVerifiedFilePeers([{deviceId:'host',online:true,shares:[
  {id:'disabled',permission:'none'},
  {id:'active',permission:'read_write'},
 ]}]);
 assert.equal(result.length,1);
});
test('malformed and unauthenticated inventories fail closed',()=>{
 assert.deepEqual(onlyVerifiedFilePeers(null),[]);
 assert.deepEqual(onlyVerifiedFilePeers([{deviceId:'host',online:true,shares:[{id:'',permission:'read'}]}]),[]);
});
test('invalid folder descriptors are not exposed beside a valid share',()=>{
 const result=onlyVerifiedFilePeers([{deviceId:'host',online:true,shares:[
  {id:'readable',permission:'read'}, {id:'not-approved',permission:'none'}
 ]}]);
 assert.deepEqual(result[0].shares,[{id:'readable',permission:'read'}]);
});
