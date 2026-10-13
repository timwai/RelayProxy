import test from 'node:test';
import assert from 'node:assert/strict';
import {showRestoredWebsiteTab} from '../src/session-tabs.js';

test('v2 reuses the exact tab owning sessionStorage rather than making a new tab',async()=>{
  const operations=[];
  globalThis.chrome={
    tabs:{
      async get(id){assert.equal(id,42);return {id,url:'https://example.com/account',incognito:false,windowId:3};},
      async update(id,options){operations.push(['update',id,options]);},
      async create(){throw new Error('Opening a fresh tab discards per-tab Session Storage');}
    },
    windows:{async update(id,options){operations.push(['focus',id,options]);}}
  };
  await showRestoredWebsiteTab(42,'https://example.com');
  assert.deepEqual(operations,[['update',42,{active:true}],['focus',3,{focused:true}]]);
});
test('v2 refuses to focus a tab navigated to another origin',async()=>{
  let changed=false;
  globalThis.chrome={
    tabs:{async get(){return {id:5,url:'https://other.example/path',incognito:false};},
      async update(){changed=true;}},
    windows:{async update(){changed=true;}}
  };
  await assert.rejects(showRestoredWebsiteTab(5,'https://example.com'),/navigated/);
  assert.equal(changed,false);
});
test('v2 rejects incognito and invalid restored tab identifiers',async()=>{
  globalThis.chrome={tabs:{async get(){return {id:4,url:'https://example.com',incognito:true};}}};
  await assert.rejects(showRestoredWebsiteTab(-1,'https://example.com'),/identifier/);
  await assert.rejects(showRestoredWebsiteTab(4,'https://example.com'),/missing or navigated/);
});
