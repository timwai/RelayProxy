import test from 'node:test';
import assert from 'node:assert/strict';
import {createDirectoryCache} from './fileBrowserCache.js';

test('directory cache is scoped per peer/share/path and expires',()=>{
 let clock=100;
 const cache=createDirectoryCache({ttlMs:6000,now:()=>clock});
 const files=[{name:'sensitive.txt',size:1}];
 cache.set('a','share-a','.',files);
 files.push({name:'after.txt'});
 assert.deepEqual(cache.get('a','share-a','.'),[{name:'sensitive.txt',size:1}]);
 assert.equal(cache.get('b','share-a','.'),null);
 assert.equal(cache.get('a','share-b','.'),null);
 assert.equal(cache.get('a','share-a','sub'),null);
 clock=6100;
 assert.equal(cache.get('a','share-a','.'),null);
});
test('revoked peers, explicit invalidation and cache bounds',()=>{
 const cache=createDirectoryCache({maxEntries:2});
 cache.set('a','s','.',[{name:'one'}]);
 cache.set('b','s','.',[]);
 cache.set('c','s','.',[]);
 assert.equal(cache.size,2);
 assert.equal(cache.get('a','s','.'),null);
 cache.delete('b','s','.');
 assert.equal(cache.get('b','s','.'),null);
 cache.clearDevice('c');
 assert.equal(cache.size,0);
 cache.set('a','s','.',[]);
 cache.clear();
 assert.equal(cache.get('a','s','.'),null);
});
test('cache rejects non-array data',()=>{
 const cache=createDirectoryCache();
 cache.set('device','share','.',null);
 assert.equal(cache.size,0);
});
