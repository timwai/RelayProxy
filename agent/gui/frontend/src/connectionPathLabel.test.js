import test from 'node:test';
import assert from 'node:assert/strict';
import {connectionPathLabel} from './connectionPathLabel.js';

const exits = [
  {deviceId:'exit-a',name:'家里电脑'},
  {deviceId:'exit-b',deviceName:'办公室电脑'},
  {id:'local-socks',name:'自定义代理'},
];

test('proxy connection path displays the current device name instead of its ID', () => {
  assert.equal(connectionPathLabel({action:'PROXY',exit_id:'exit-a'},exits),'PROXY · 家里电脑');
  assert.equal(connectionPathLabel({action:'PROXY',exit_id:'exit-b',path:'relay_quic'},exits),'relay_quic · 办公室电脑');
  assert.equal(connectionPathLabel({action:'PROXY',exit_id:'local-socks',path:'p2p_quic'},exits),'p2p_quic · 自定义代理');
});

test('legacy paths with embedded IDs replace the ID rather than adding another suffix', () => {
  assert.equal(connectionPathLabel({action:'PROXY',exit_id:'exit-a',path:'PROXY · exit-a'},exits),'PROXY · 家里电脑');
});

test('device renames are reflected in existing connections without mutating IDs', () => {
  const connection = {action:'PROXY',exit_id:'exit-a',path:'relay_tls'};
  assert.equal(connectionPathLabel(connection,exits),'relay_tls · 家里电脑');
  assert.equal(connectionPathLabel(connection,[{deviceId:'exit-a',name:'新名称'}]),'relay_tls · 新名称');
  assert.equal(connection.exit_id,'exit-a');
});

test('unknown devices safely fall back to their IDs; direct and rejected traffic stay unchanged', () => {
  assert.equal(connectionPathLabel({action:'PROXY',exit_id:'unknown',path:'relay_quic'},exits),'relay_quic · unknown');
  assert.equal(connectionPathLabel({action:'DIRECT',exit_id:'exit-a'},exits),'DIRECT');
  assert.equal(connectionPathLabel({action:'REJECT',exit_id:'exit-a',path:'blocked'},exits),'blocked');
  assert.equal(connectionPathLabel({action:'PROXY',path:'relay_quic'},exits),'relay_quic');
  assert.equal(connectionPathLabel({},exits),'—');
});
