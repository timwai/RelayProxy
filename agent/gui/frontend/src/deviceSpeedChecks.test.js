import test from 'node:test';
import assert from 'node:assert/strict';
import {deviceSpeedChecks,DEVICE_SPEED_REVIEW_STEPS} from './deviceSpeedChecks.js';

const authorized={deviceId:'mac-a',name:'Office Mac',online:true,direct:{public:{available:true,endpoints:[{verified:true,address:'198.51.100.1:443'}]}}};
const measurement={exitId:'mac-a',upload:{path:'p2p_quic'},download:{path:'relay_quic'}};
const rows=(phase,result=null,revealCount=0)=>deviceSpeedChecks({exitID:'mac-a',exit:authorized,connected:true,phase,result,revealCount});
const get=(list,id)=>list.find(x=>x.id===id);

test('before test: preflight authorization and verified candidates checked, measured links unconfirmed',()=>{
 const items=rows('idle');
 assert.deepEqual(items.map(r=>r.id),['authorization','candidate','test','public','p2p','relay','upload','download']);
 assert.equal(get(items,'authorization').state,'checked');
 assert.equal(get(items,'candidate').state,'checked');
 assert.equal(get(items,'test').state,'waiting');
 assert.equal(get(items,'relay').state,'waiting');
});

test('during actual RPC: show progress but never fabricate per-path success',()=>{
 const items=rows('running');
 assert.equal(get(items,'test').state,'progress');
 for(const id of ['public','p2p','relay','upload','download'])assert.equal(get(items,id).state,'waiting');
});

test('after RPC: check measurement results progressively in a stable row order',()=>{
 const start=rows('reviewing',measurement,0);
 assert.equal(get(start,'test').state,'checked');
 assert.equal(get(start,'p2p').state,'waiting');
 const first=rows('reviewing',measurement,1);
 assert.equal(get(first,'public').state,'candidate');
 assert.equal(get(first,'p2p').state,'waiting');
 const second=rows('reviewing',measurement,2);
 assert.equal(get(second,'p2p').state,'checked');
 assert.equal(get(second,'relay').state,'waiting');
 const complete=rows('done',measurement,DEVICE_SPEED_REVIEW_STEPS);
 assert.equal(get(complete,'public').state,'candidate');
 assert.equal(get(complete,'p2p').state,'checked');
 assert.equal(get(complete,'relay').state,'checked');
 assert.equal(get(complete,'upload').detail,'p2p_quic');
 assert.equal(get(complete,'download').detail,'relay_quic');
});

test('a candidate is never mistaken for a measured direct path',()=>{
 const result=rows('done',measurement,DEVICE_SPEED_REVIEW_STEPS);
 assert.equal(get(result,'public').state,'candidate');
 const verifiedDirect=rows('done',{exitId:'mac-a',upload:{path:'public_direct_quic'},download:{path:'public_direct_quic'}},5);
 assert.equal(get(verifiedDirect,'public').state,'checked');
});

test('wrong exit ID, disconnected and failed measurements cannot be checked',()=>{
 const mismatch=rows('done',{exitId:'other',upload:{path:'p2p_quic'}},5);
 assert.equal(get(mismatch,'test').state,'failed');
 assert.equal(get(mismatch,'p2p').state,'waiting');
 const error=rows('failed',{exitId:'mac-a',error:'timeout'},5);
 assert.equal(get(error,'test').state,'failed');
 assert.equal(get(error,'relay').state,'waiting');
 const offline=deviceSpeedChecks({exitID:'mac-a',exit:authorized,connected:false});
 assert.equal(get(offline,'authorization').state,'failed');
});

test('missing one direction is never counted as a verified dual-direction test',()=>{
 const incomplete=rows('done',{exitId:'mac-a',upload:{path:'p2p_quic'}},5);
 assert.equal(get(incomplete,'test').state,'failed');
 assert.equal(get(incomplete,'p2p').state,'waiting');
});
