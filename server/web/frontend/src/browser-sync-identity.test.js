import test from 'node:test';
import assert from 'node:assert/strict';
import {findBrowserIdentity} from './browser-sync-identity.js';

const active={id:'idn_00000000-0000-0000-0000-000000000001',shortId:'a1b2c3d4e5f6g7h8',name:'工作身份',status:'active'};
const disabled={id:'idn_disabled',shortId:'b1c2d3e4f5g6h7i8',status:'disabled'};

test('browser approval recognizes the displayed public ID and legacy internal ID',()=>{
  for(const reference of [active.shortId,active.shortId.toUpperCase(),active.id]){
    assert.equal(findBrowserIdentity([disabled,active],reference),active);
  }
});

test('browser approval rejects disabled, missing and unknown requested identities',()=>{
  for(const reference of [disabled.shortId,disabled.id,'unknown','',null]){
    assert.equal(findBrowserIdentity([active,disabled],reference),null);
  }
  assert.equal(findBrowserIdentity([],active.shortId),null);
  assert.equal(findBrowserIdentity([{...active,status:undefined,state:'disabled'}],active.shortId),null);
});
