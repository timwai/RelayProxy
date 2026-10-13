import assert from 'node:assert/strict';
import test from 'node:test';
import {proposeBrutalFromSpeedTests,validateBrutalCalibrationSample} from './brutalCalibration.js';

const record=(up=12,down=18,overrides={})=>({
 exitId:'exit-a',
 upload:{path:'relay_quic',megabitsPerSecond:up,durationMs:3000,
  quic:{congestion_controller:'bbr-aggressive'}},
 download:{path:'relay_quic',megabitsPerSecond:down,durationMs:3000,
  quic:{congestion_controller:'bbr-aggressive'}},...overrides
});
test('5 same-path BBR rounds return conservative floor but never enable automatically',()=>{
 const result=proposeBrutalFromSpeedTests(Array.from({length:5},()=>record()));
 assert.equal(result.upload.candidateMbps,7);
 assert.equal(result.download.candidateMbps,10);
 assert.equal(result.rounds,5);
 assert.equal(result.autoApplyAllowed,false);
 assert.equal(result.brutalDefaultEligible,false);
});
test('lower tail controls candidate, not optimistic average',()=>{
 const samples=[record(4,5),record(20,30),record(20,30),record(20,30),record(20,30)];
 const p=proposeBrutalFromSpeedTests(samples);
 assert.ok(p.upload.candidateMbps<10);
 assert.ok(p.download.candidateMbps<15);
});
test('incomplete samples reject even when most runs are fast',()=>{
 assert.throws(()=>proposeBrutalFromSpeedTests(Array.from({length:4},()=>record())),/5 轮/);
});
test('do not calibrate Brutal mode, fallback or unverified UDP paths',()=>{
 const baseline=Array.from({length:5},()=>record());
 for(const unsafe of [
  record(12,18,{upload:{...record().upload,quic:{congestion_controller:'brutal'}}}),
  record(12,18,{upload:{...record().upload,quic:{congestion_controller:'bbr-aggressive',brutal_fallback_reason:'queueing'}}}),
  record(12,18,{upload:{...record().upload,path:'P2P QUIC'}}),
  record(12,18,{upload:{...record().upload,quic:null}}),
  record(12,18,{upload:{...record().upload,durationMs:300}}),
  record(12,18,{upload:{...record().upload,megabitsPerSecond:0}})
 ]){
  assert.throws(()=>proposeBrutalFromSpeedTests([unsafe,...baseline.slice(1)]));
 }
});
test('reject a run with missing exit ID and do not defer failure',()=>{
 assert.throws(()=>validateBrutalCalibrationSample(record(12,18,{exitId:''})),/出口标识/);
 const run=record();
 assert.equal(validateBrutalCalibrationSample(run).relayPath,'relay_quic');
});
test('mixing exit IDs invalidates five otherwise healthy runs',()=>{
 const all=Array.from({length:5},()=>record());
 all[3].exitId='exit-b';
 assert.throws(()=>proposeBrutalFromSpeedTests(all),/出口或 Relay 路径改变/);
});
test('a path change during calibration invalidates the result',()=>{
 const list=Array.from({length:5},()=>record());
 list[2].download.path='relay_tls';
 assert.throws(()=>proposeBrutalFromSpeedTests(list),/测速路径不是固定 Relay QUIC/);
});
