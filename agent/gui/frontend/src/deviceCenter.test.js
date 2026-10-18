import test from 'node:test';
import assert from 'node:assert/strict';
import {buildDeviceCenter} from './deviceCenter.js';

test('merge proxy, RDP and verified file ACL capabilities by real device identity',()=>{
 const devices=buildDeviceCenter({
  proxyExits:[{deviceId:'a',name:'Office Mac',online:true},{deviceId:'b',name:'Lab',online:false},{id:'local:proxy',name:'Local proxy',online:true}],
  rdpTargets:[{deviceId:'a',name:'Office Mac',online:true},{deviceId:'c',name:'Windows PC',online:true}],
  filePeers:[{deviceId:'a',name:'Office Mac',online:true,shares:[{id:'docs',name:'Docs',permission:'read'}]}],
 });
 assert.deepEqual(devices.map(d=>d.id).sort(),['a','b','c']);
 const mac=devices.find(d=>d.id==='a');
 assert.ok(mac.proxy&&mac.rdp);
 assert.equal(mac.shares.length,1);
 assert.equal(mac.online,true);
 assert.equal(devices.find(d=>d.id==='b').online,false);
});

test('do not leak file icons for stale, revoked, malformed or offline ACLs',()=>{
 const devices=buildDeviceCenter({
  proxyExits:[{deviceId:'old',online:true}],
  filePeers:[
   {deviceId:'old',online:true,shares:[{id:'private',permission:'none'}]},
   {deviceId:'stale',online:false,shares:[{id:'docs',permission:'read'}]},
   {deviceId:'legacy',online:true},
  ],
 });
 assert.deepEqual(devices.map(d=>d.id),['old']);
 assert.deepEqual(devices[0].shares,[]);
});

test('files-only devices are visible when an online share ACL is verified',()=>{
 const devices=buildDeviceCenter({filePeers:[{deviceId:'fs-only',name:'File Host',online:true,shares:[{id:'readonly',permission:'read'},{id:'write',permission:'read_write'}]}]});
 assert.equal(devices.length,1);
 assert.equal(devices[0].name,'File Host');
 assert.equal(devices[0].shares.length,2);
 assert.equal(devices[0].proxy,null);
 assert.equal(devices[0].rdp,null);
});
