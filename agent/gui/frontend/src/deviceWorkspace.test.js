import assert from 'node:assert/strict';
import test from 'node:test';
import {buildDeviceInventory,deviceActionAvailable} from './deviceWorkspace.js';

const status={connected:true,deviceId:'self-id',deviceName:'本机'};
test('only verified authorized resources create devices, merging by device ID',()=>{
 const list=buildDeviceInventory({status,exits:[{deviceId:'a',name:'工作电脑',online:true},{deviceId:'local:custom',name:'代理'}],
  targets:[{deviceId:'a',online:true},{deviceId:'b',name:'实验机',online:false}],
  filePeers:[{deviceId:'a',name:'工作电脑',online:true,shares:[{id:'1',permission:'read'}]},
    {deviceId:'secret',name:'不应显示',online:true,shares:[]}]});
 assert.deepEqual(list.map(d=>d.id),['local','a','b']);
 assert.deepEqual(list[1].capabilities,['proxy.exit','rdp.host','files.host']);
 assert.equal(deviceActionAvailable(list[1],'files'),true);
 assert.equal(deviceActionAvailable(list[2],'rdp'),false);
});
test('offline inventory never exposes stale remote entries as authorized',()=>{
 const list=buildDeviceInventory({status:{...status,connected:false},
  exits:[{deviceId:'a',online:true}],targets:[{deviceId:'b',online:true}],
  filePeers:[{deviceId:'c',online:true,shares:[{id:'s',permission:'read'}]}]});
 assert.deepEqual(list.map(d=>d.id),['local']);
});
test('file capability requires authenticated share list and blocks unapproved entry',()=>{
 const list=buildDeviceInventory({status,filePeers:[
  {deviceId:'no-proof',online:true},
  {deviceId:'bad-share',online:true,shares:[{id:'foo',permission:'none'}]},
  {deviceId:'valid',online:true,shares:[{id:'bar',permission:'read_write'}]}]});
 assert.deepEqual(list.map(d=>d.id),['local','valid']);
 assert.equal(deviceActionAvailable(list[1],'files'),true);
});
test('same device ID as self is not listed twice',()=>{
 const list=buildDeviceInventory({status,exits:[{deviceId:'self-id',online:true}]});
 assert.equal(list.length,1);
});
