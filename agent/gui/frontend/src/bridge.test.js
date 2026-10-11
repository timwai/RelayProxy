import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizeConfigUpdate,saveConfig} from './bridge.js';

test('Agent read-only hasPassword never enters strict exit upstream updates',()=>{
  const payload={exit:{
    enabled:true,
    upstream:{mode:'socks5',address:'127.0.0.1:1080',
      username:'relay',hasPassword:true,password:''}
  }};
  const cleaned=normalizeConfigUpdate(payload);
  assert.deepEqual(cleaned,{exit:{enabled:true,upstream:{
    mode:'socks5',address:'127.0.0.1:1080',username:'relay'
  }}});
  assert.equal(payload.exit.upstream.hasPassword,true);
  assert.equal(payload.exit.upstream.password,'');
});

test('existing custom exit passwords are preserved when another exit is edited or toggled',()=>{
  const payload={proxy:{customExits:[
    {id:'local:one',name:'one',enabled:false,protocol:'socks5',
      address:'example.net:1080',username:'test',hasPassword:true},
    {id:'local:two',name:'two',enabled:true,protocol:'http',
      address:'example.com:8080',username:'',hasPassword:true,
      password:'',clearPassword:false},
    {id:'local:three',name:'three',enabled:true,protocol:'https',
      address:'example.org:443',username:'new',password:'new-secret',hasPassword:false},
    {id:'local:four',name:'four',enabled:true,protocol:'socks5',
      address:'example.org:1080',username:'',hasPassword:true,
      clearPassword:true,password:''}
  ]}};
  const cleaned=normalizeConfigUpdate(payload);
  assert.deepEqual(cleaned.proxy.customExits.map(x=>x.password),[
    undefined,undefined,'new-secret',''
  ]);
  for(const x of cleaned.proxy.customExits){
    assert(!Object.hasOwn(x,'hasPassword'));
    assert(!Object.hasOwn(x,'clearPassword'));
  }
  assert.equal(payload.proxy.customExits[0].hasPassword,true);
});

test('legacy upstream password can be explicitly changed or cleared without reading old secret',()=>{
  assert.deepEqual(normalizeConfigUpdate({exit:{upstream:{
    mode:'http',hasPassword:true,password:'replacement'
  }}}).exit.upstream,{mode:'http',password:'replacement'});
  assert.deepEqual(normalizeConfigUpdate({exit:{upstream:{
    mode:'http',hasPassword:true,password:'',clearPassword:true
  }}}).exit.upstream,{mode:'http',password:''});
});

test('saveConfig sends only writable fields to native/Web bridges',async()=>{
  const original=globalThis.window;
  const sent=[];
  globalThis.window={goSaveConfig:async raw=>{
    sent.push(JSON.parse(raw));return JSON.stringify({ok:true});
  }};
  try{
    const result=await saveConfig('rev-1',{
      exit:{enabled:true,upstream:{mode:'socks5',hasPassword:true,password:''}},
      proxy:{customExits:[{id:'local:x',name:'x',enabled:true,
        protocol:'http',address:'example.net:8080',hasPassword:true}]}
    });
    assert.equal(result.ok,true);
    assert.equal(sent.length,1);
    assert.equal(sent[0].revision,'rev-1');
    assert.deepEqual(sent[0].exit.upstream,{mode:'socks5'});
    assert.deepEqual(sent[0].proxy.customExits,[{
      id:'local:x',name:'x',enabled:true,protocol:'http',address:'example.net:8080'
    }]);
  }finally{globalThis.window=original;}
});
