import test from 'node:test';
import assert from 'node:assert/strict';
import {classifyDeviceTestPath,deviceSpeedLinkEvidence} from './deviceSpeedLink.js';

test('scope measured Direct/P2P/Relay only to exact speed-test exit ID',()=>{
 const exit={deviceId:'dev-a',direct:{public:{available:true,endpoints:[{address:'198.51.100.1:443',verified:true},{address:'203.0.113.1:443',verified:false}]}}};
 const sample={exitId:'dev-a',upload:{path:'relay_quic',quic:{smoothed_rtt_ms:22}},download:{path:'public_direct_quic',quic:{sent_packet_loss_pct:0.5}}};
 const evidence=deviceSpeedLinkEvidence('dev-a',exit,sample);
 assert.equal(evidence.verifiedTest,true);
 assert.equal(evidence.usedPublic,true);
 assert.equal(evidence.usedRelay,true);
 assert.equal(evidence.usedP2P,false);
 assert.equal(evidence.verifiedEndpointCount,1);
 assert.equal(evidence.publicAdvertised,true);
 assert.equal(evidence.uploadPath,'relay_quic');
 assert.equal(evidence.downloadPath,'public_direct_quic');
 assert.deepEqual(evidence.quicUpload,{smoothed_rtt_ms:22});
});

test('a result from another device must not appear as this device\'s link measurement',()=>{
 const r=deviceSpeedLinkEvidence('dev-a',{direct:{public:{available:true,endpoints:[]}}},{exitId:'dev-b',upload:{path:'p2p_quic'},download:{path:'relay_quic'}});
 assert.equal(r.verifiedTest,false);
 assert.equal(r.usedP2P,false);
 assert.equal(r.usedRelay,false);
 assert.deepEqual(r.paths,[]);
 assert.equal(r.publicAdvertised,true);
});

test('missing speed provenance cannot be attributed to selected remote device',()=>{
 for(const value of [null,{upload:{path:'public_direct_quic'}},{exitId:'',upload:{path:'public_direct_quic'}},{exitId:'dev-a',error:'timeout'}]){
  assert.equal(deviceSpeedLinkEvidence('dev-a',{},value).verifiedTest,false);
 }
});

test('classify direct, punched P2P, Relay and unclassified paths without guessing',()=>{
 assert.equal(classifyDeviceTestPath('public_direct_quic'),'public');
 assert.equal(classifyDeviceTestPath('direct_tls'),'public');
 assert.equal(classifyDeviceTestPath('p2p_quic'),'p2p');
 assert.equal(classifyDeviceTestPath('relay_quic'),'relay');
 assert.equal(classifyDeviceTestPath('custom_quic'),'unknown');
});
