import test from 'node:test';
import assert from 'node:assert/strict';
import {allCapabilities,visibleCapabilityGroups,toggleDeviceCapability} from './capability-ui.js';

test('device capabilities are grouped into network and remote desktop permissions',()=>{
 assert.deepEqual(visibleCapabilityGroups().map(g=>g.title),['网络代理','远程桌面']);
 assert.equal(allCapabilities.length,5);
 assert.deepEqual(visibleCapabilityGroups(['proxy.client','rdp.host']).map(g=>g.items.map(i=>i.id)),
   [['proxy.client'],['rdp.host']]);
 assert.deepEqual(visibleCapabilityGroups([]),[]);
});

test('clicking an authorization card toggles only allowed capabilities',()=>{
 assert.deepEqual(toggleDeviceCapability(['proxy.client'],'proxy.exit',true,['proxy.client']),['proxy.client']);
 assert.deepEqual(toggleDeviceCapability(['proxy.client'],'proxy.client',false),[]);
 assert.deepEqual(toggleDeviceCapability([],'proxy.client',true),['proxy.client']);
});

test('public RDP always requires its host capability, including during approval',()=>{
 const all=allCapabilities;
 const selected=toggleDeviceCapability([],'rdp.public',true,all);
 assert.deepEqual(selected,['rdp.host','rdp.public']);
 assert.deepEqual(toggleDeviceCapability(selected,'rdp.host',false,all),[]);
 assert.deepEqual(toggleDeviceCapability([],'rdp.public',true,['rdp.public']),[]);
});

test('unknown and unapproved values cannot be introduced by selections',()=>{
 assert.deepEqual(toggleDeviceCapability(['proxy.exit'],'rdp.controller',true,['proxy.client']),[]);
});
