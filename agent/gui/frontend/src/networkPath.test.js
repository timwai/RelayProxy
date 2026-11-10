import test from 'node:test';
import assert from 'node:assert/strict';
import {pathLabel,activePathLabel} from '../../../../internal/webui/pathLabel.mjs';
import {connectionPathLabel} from './connectionPathLabel.js';
import {rdpPathLabel,proxyPathStatus} from './pathStatus.js';

test('scope is attached to the accepted transport, never inferred from P2P',()=>{
 const lan={path:'p2p_quic',network:'lan',transport:'quic'};
 assert.equal(pathLabel(lan),'局域网直连 · QUIC');
 assert.equal(pathLabel({...lan,network:'public'}),'P2P 公网直连 · QUIC');
 assert.equal(pathLabel({...lan,network:'virtual'}),'虚拟网络直连 · QUIC');
 assert.equal(pathLabel({...lan,network:'unknown'}),'直连（网络范围未知） · QUIC');
 assert.equal(pathLabel(lan,'relay_tls'),'中继 · TLS');
 assert.equal(connectionPathLabel({action:'PROXY',exit_id:'x',path:'p2p_quic',pathInfo:lan},[{id:'x',name:'家'}]),'局域网直连 · QUIC · 家');
 assert.equal(rdpPathLabel('tcp_p2p',[{path:'tcp_p2p',network:'lan',transport:'tcp'}]),'局域网直连 · TCP');
});

test('available direct transport is independent of actual data usage',()=>{
 const status={connected:true,directPath:'p2p_quic',activeProxyPaths:[]};
 assert.equal(proxyPathStatus(status).path,'暂无活动代理连接');
 status.activeProxyPaths=[{path:'relay_tls',network:'public',transport:'tls'}];
 assert.equal(proxyPathStatus(status).path,'中继 · TLS');
 assert.equal(activePathLabel(undefined),'未提供实际连接路径');
 assert.equal(activePathLabel([...status.activeProxyPaths,{path:'p2p_quic',network:'lan',transport:'quic'}]),'中继 · TLS + 局域网直连 · QUIC');
});
