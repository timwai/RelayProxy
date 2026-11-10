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

function reloadFixture({after,removed=false,rejectReload=false}){
 const updates=new Set(),removals=new Set();
 let reloaded=false;
 globalThis.chrome={tabs:{
  async get(id){return {id,url:reloaded?after:'https://example.com/account',status:'complete'};},
  async reload(id){
   if(rejectReload)throw new Error('reload denied');
   reloaded=true;
   if(removed)for(const callback of removals)callback(id);
   else for(const callback of updates)callback(id,{status:'complete'});
  },
  onUpdated:{addListener(f){updates.add(f);},removeListener(f){updates.delete(f);}},
  onRemoved:{addListener(f){removals.add(f);},removeListener(f){removals.delete(f);}}
 }};
 return {updates,removals};
}
test('reload waits for completion and cleans up event listeners',async()=>{
 const {reloadRestoredWebsiteTab}=await import('../src/session-tabs.js');
 const fixture=reloadFixture({after:'https://example.com/account'});
 await reloadRestoredWebsiteTab(42,'https://example.com');
 assert.equal(fixture.updates.size,0);assert.equal(fixture.removals.size,0);
});
test('reload refuses post-refresh cross-origin navigation, closed tabs and Chrome errors',async()=>{
 const {reloadRestoredWebsiteTab}=await import('../src/session-tabs.js');
 for(const [setup,pattern] of [
  [{after:'https://other.example/path'},/跳转/],
  [{after:'https://example.com',removed:true},/已关闭/],
  [{after:'https://example.com',rejectReload:true},/reload denied/]
 ]){
  const fixture=reloadFixture(setup);
  await assert.rejects(reloadRestoredWebsiteTab(42,'https://example.com'),pattern);
  assert.equal(fixture.updates.size,0);assert.equal(fixture.removals.size,0);
 }
});
