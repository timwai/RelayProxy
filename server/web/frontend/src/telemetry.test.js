import assert from 'node:assert/strict';
import test from 'node:test';
import {recordTelemetry, routeBreakdown, appendSample, formatRate, SAMPLE_INTERVAL_MS, MAX_SAMPLES} from './telemetry.js';

const dashboard = {onlineDevices:3,onlineExits:2,activeConnections:7,activeP2PSessions:1,todayUpload:4096,todayDownload:8192,exitNodes:[{deviceId:'server',deviceName:'Relay Server',activeStreams:2}]};
const sess = (bytesUp,bytesDown,connectedAt='2026-10-09T05:00:00Z',directPath='relay_quic') => ({clientDeviceId:'device-a',connectedAt,bytesUp,bytesDown,peerDiagnostics:{payload:{status:{directPath}}}});

test('first sample has no fabricated traffic rate; subsequent samples reflect actual deltas', () => {
  const first = recordTelemetry(dashboard,[sess(1024,2048)],[],null,1000);
  assert.equal(first.sample.uploadBps, null);
  assert.equal(first.sample.downloadBps,null);
  const second = recordTelemetry(dashboard,[sess(3072,7168)],[{sessionId:1}],first,11000);
  assert.equal(second.sample.uploadBps,204.8);
  assert.equal(second.sample.downloadBps,512);
  assert.equal(second.sample.online,3);
  assert.equal(second.sample.connections,7);
  assert.equal(second.sample.availableExits,2);
  assert.equal(second.sample.exits[0].streams,2);
  assert.equal(second.sample.p2pNegotiations,1);
});

test('reconnects and counter resets do not generate negative rates or huge spikes', () => {
  const first=recordTelemetry(dashboard,[sess(1e9,2e9)],[],null,1000);
  const reconnected=recordTelemetry(dashboard,[sess(20,40,'2026-10-09T05:01:00Z')],[],first,11000);
  assert.equal(reconnected.sample.uploadBps,0);
  assert.equal(reconnected.sample.downloadBps,0);
  const counterReset=recordTelemetry(dashboard,[sess(10,10,'2026-10-09T05:01:00Z')],[],reconnected,21000);
  assert.equal(counterReset.sample.uploadBps,0);
  assert.equal(counterReset.sample.downloadBps,0);
});

test('measure only sessions that persist across consecutive snapshots', () => {
  const first=recordTelemetry(dashboard,[sess(100,100)],[],null,1000);
  const next=recordTelemetry(dashboard,[sess(1100,2200),{clientDeviceId:'device-b',connectedAt:'2026-10-09T05:00:00Z',bytesUp:999999,bytesDown:999999}],[],first,11000);
  assert.equal(next.sample.uploadBps,100);
  assert.equal(next.sample.downloadBps,210);
});

test('derive honest route breakdown only from reported path, not guessed transport', () => {
  const routes=routeBreakdown([sess(0,0,undefined,'p2p_quic'),sess(0,0,undefined,'public_direct_quic'),sess(0,0),{clientDeviceId:'device-c',transport:'quic'}]);
  assert.deepEqual(routes,{p2p:1,publicDirect:1,relay:1,unknown:1});
});

test('zero, missing and invalid values remain safe',()=>{
  const sample=recordTelemetry({onlineDevices:-3,onlineExits:'bad',activeConnections:null,exitNodes:[{activeStreams:-1}]},[],[],null,1000).sample;
  assert.equal(sample.online,0);
  assert.equal(sample.availableExits,0);
  assert.equal(sample.connections,0);
  assert.equal(sample.exits[0].streams,0);
  assert.equal(formatRate(null),'等待采样');
  assert.equal(formatRate(1048576),'1.00 MiB/s');
  assert.equal(formatRate(0),'0 B/s');
});

test('history is bounded to real samples, no artificial backfill',()=>{
  let history=[];
  for(let i=0;i<MAX_SAMPLES+20;i++)history=appendSample(history,{timestamp:i});
  assert.equal(history.length,MAX_SAMPLES);
  assert.equal(history[0].timestamp,20);
  assert.equal(history.at(-1).timestamp,MAX_SAMPLES+19);
  assert.equal(SAMPLE_INTERVAL_MS,10000);
});
