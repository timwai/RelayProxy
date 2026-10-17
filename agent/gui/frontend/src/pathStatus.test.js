import test from 'node:test';
import assert from 'node:assert/strict';
import {proxyPathStatus,rdpPathLabel} from './pathStatus.js';

test('proxy availability and the accepted RDP path remain independent',()=>{
 const status={connected:true,directPath:'p2p_quic',p2pPath:'p2p_quic',rdpPathTcp:'relay',rdpPathUdp:'waiting'};
 assert.equal(proxyPathStatus(status).p2pReady,true);
 assert.equal(rdpPathLabel(status.rdpPathTcp),'Relay');
 assert.equal(rdpPathLabel(status.rdpPathUdp),'等待客户端流量');
});

test('public direct takes precedence over a standby P2P candidate',()=>{
 const status={connected:true,directPath:'public_direct_quic',p2pPath:'p2p_quic'};
 assert.equal(proxyPathStatus(status).directReady,true);
 assert.equal(proxyPathStatus(status).p2pReady,false);
 assert.equal(proxyPathStatus({...status,connected:false}).directReady,false);
});

test('RDP can use both direct and relay TCP channels',()=>{
 assert.equal(rdpPathLabel('relay + tcp_p2p'),'Relay + P2P (TCP)');
 assert.equal(rdpPathLabel('disabled'),'未启用');
});
