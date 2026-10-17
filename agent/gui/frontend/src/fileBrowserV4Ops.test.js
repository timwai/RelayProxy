import assert from 'node:assert/strict';
import test from 'node:test';
import {validRemotePath,planSameShareMoves} from './fileBrowserV4Ops.js';
const active={device:'host-1',share:'shared-A',path:'work'};
const source={...active,names:['notes.txt','design']};
test('same-share moves resolve relative paths without changing the selected device',()=>{
 assert.deepEqual(planSameShareMoves(source,active,{...active,path:'archive'},true),[
  {source:'work/notes.txt',destination:'archive/notes.txt'},
  {source:'work/design',destination:'archive/design'},
 ]);
});
test('untrusted drag payloads and cross-device or cross-share targets always fail',()=>{
 for(const dest of [{device:'host-2',share:'shared-A',path:'archive'},{device:'host-1',share:'shared-B',path:'archive'}]){
  assert.throws(()=>planSameShareMoves(source,active,dest,true),/不支持/);
 }
 assert.throws(()=>planSameShareMoves(source,active,active,false),/不支持/);
 assert.throws(()=>planSameShareMoves({...source,device:'host-2'},active,active,true),/不支持/);
});
test('rejects malformed paths, duplicate names, self and nested-folder moves',()=>{
 for(const path of ['../outside','/etc','x\\y','a//b','a/./b','a/../b','.relayproxy-upload-123']){
  assert.equal(validRemotePath(path),false);
 }
 assert.throws(()=>planSameShareMoves({...source,names:['design']},active,{...active,path:'work/design/sub'},true),/不能/);
 assert.throws(()=>planSameShareMoves(source,active,active,true),/不能/);
 assert.throws(()=>planSameShareMoves({...source,names:['same','same']},active,{...active,path:'archive'},true),/重复/);
 assert.throws(()=>planSameShareMoves({...source,names:['a/b']},active,{...active,path:'archive'},true),/名称/);
});
