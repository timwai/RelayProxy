import test from 'node:test';
import assert from 'node:assert/strict';
import {tabFor,execute} from '../src/web-storage.js';

const origin='https://example.com';
function tabs(all,active=[]){
 globalThis.chrome={tabs:{
  async query(query){return query.active?active:all;},
  async get(id){return all.find(tab=>tab.id===id);}
 }};
}
test('Storage selects the most recently used site tab when extension Options is active',async()=>{
 const all=[{id:1,url:origin+'/old',lastAccessed:100},
  {id:2,url:origin+'/current',lastAccessed:300},
  {id:3,url:'chrome-extension://fixture/options.html',lastAccessed:400}];
 tabs(all,[all[2]]);
 assert.equal(await tabFor(origin),2);
 tabs(all,[all[0]]);assert.equal(await tabFor(origin),1,'explicit active site wins');
});
test('Storage refuses ambiguous tabs without recency and ignores incognito or other origins',async()=>{
 tabs([{id:1,url:origin},{id:2,url:origin}]);
 await assert.rejects(tabFor(origin),/多个 Storage 标签页/);
 tabs([{id:1,url:origin,lastAccessed:100},{id:2,url:origin,incognito:true,lastAccessed:999},
  {id:3,url:'https://other.example.com',lastAccessed:1000}]);
 assert.equal(await tabFor(origin),1);
});
test('Storage rejects pending cross-origin navigation and unloaded tabs',async()=>{
 for(const extra of [{pendingUrl:'https://other.example.com'},
  {status:'loading'},{discarded:true},{frozen:true}]){
  tabs([{id:1,url:origin,...extra}]);
  await assert.rejects(tabFor(origin),/跳转|尚未就绪/);
 }
});
test('Storage scripting requires top-level nonempty replies and pinned document identity',async()=>{
 for(const frame of [{result:null},{result:undefined},{result:true,frameId:1},
  {result:true,frameId:0,documentId:'other'}]){
  globalThis.chrome={scripting:{async executeScript(request){
   assert.equal(request.world,'ISOLATED');return [frame];
  }}};
  await assert.rejects(execute(1,()=>true,[],'expected'),/Storage/);
 }
});

test('Storage new tab can finish loading before the completion listener is registered',async()=>{
 const listeners=new Set();
 globalThis.chrome={tabs:{
  async query(){return [];},async create(){return {id:9,url:origin,status:'loading'};},
  async get(){return {id:9,url:origin,status:'complete'};},
  onUpdated:{addListener(fn){listeners.add(fn);},removeListener(fn){listeners.delete(fn);}},
  onRemoved:{addListener(fn){listeners.add(fn);},removeListener(fn){listeners.delete(fn);}}
 }};
 assert.equal(await tabFor(origin,{create:true}),9);
 assert.equal(listeners.size,0);
});
