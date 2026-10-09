import test from 'node:test';
import assert from 'node:assert/strict';
import {summarizeSession,summarizePathReport} from './diagnostics.js';

test('session diagnostics use the existing server API wire names',()=>{
 const row=summarizeSession({clientDeviceId:'a',exitDeviceId:'b',mode:'CLIENT',transport:'quic',bytesUp:420,
  tunnelDiagnostics:{quic:{smoothed_rtt_ms:15.5,sent_packets_lost:1}},peerDiagnostics:{payload:{status:{
   directPath:'public_direct_quic',directState:'AVAILABLE',directRttMs:14,directBytesDown:4096,directFallbackCount:2,
   tunnelDiagnostics:{quic:{smoothed_rtt_ms:12,sent_packets_lost:0}},
   directQuic:{send_bps:1500000,receive_bps:2200000,rtt_deviation_ms:2.3,sent_packet_loss_pct:0.2,gso:true}
  }}}},[{id:'a',name:'笔记本'},{id:'b',name:'出口'}]);
 assert.equal(row.client,'笔记本');assert.equal(row.exit,'出口');assert.equal(row.role,'CLIENT');
 assert.equal(row.relayRttMs,15.5);assert.equal(row.remoteRelayRttMs,12);
 assert.equal(row.directPath,'Public Direct QUIC');assert.equal(row.directReceiveBps,2200000);
 assert.equal(row.directGso,true);assert.equal(row.fallbackCount,2);
});
test('P2P reports retain failure reasons and endpoint summaries',()=>{
 const r=summarizePathReport({path:'relay_quic',reason:'unreachable IPv6',fallbackCount:2,candidateSummary:'IPv4 only'});
 assert.equal(r.name,'Relay QUIC');assert.equal(r.reason,'unreachable IPv6');
 assert.match(r.detail,/回退 2 次/);assert.match(r.detail,/IPv4 only/);
});