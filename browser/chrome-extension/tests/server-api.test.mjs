import test from 'node:test';
import assert from 'node:assert/strict';
import {webcrypto} from 'node:crypto';
import {connectBrowser, currentConnectionState, sendControl, disconnect} from '../src/server-api.js';

globalThis.crypto ||= webcrypto;
function installFakeProfile() {
  const local=new Map([['browserSyncSettings',{serverOrigin:'https://relay.example.com:8443',sites:[]}]]);
  const keys=new Map();
  const db={
    createObjectStore(){},
    transaction(){
      const tx={objectStore(){
        return {
          get(name){
            const req={};
            queueMicrotask(()=>{req.result=keys.get(name);req.onsuccess?.();});
            return req;
          },
          put(value,name) {
            keys.set(name,value);
            queueMicrotask(()=>tx.oncomplete?.());
          }
        };
      }};
      return tx;
    },
    close(){}
  };
  globalThis.indexedDB={open(){
    const req={result:db};
    queueMicrotask(()=>{req.onupgradeneeded?.();req.onsuccess?.();});
    return req;
  }};
  globalThis.chrome={storage:{local:{
    async get(name){return {[name]:local.get(name)};},
    async set(obj){for(const [name,value] of Object.entries(obj))local.set(name,value);}
  }}};
}
test('simultaneous reconnects use one authenticated WSS connection',async()=>{
  installFakeProfile();
  let sockets=0;
  class FakeWebSocket{
    static OPEN=1;
    constructor(url){
      sockets++;
      this.url=String(url);
      this.readyState=0;
      this.listeners={};
      queueMicrotask(()=>{this.readyState=FakeWebSocket.OPEN;this.emit('open',{});});
    }
    addEventListener(event,cb){(this.listeners[event] ||= []).push(cb);}
    emit(event,payload){for(const cb of this.listeners[event]||[])cb(payload);}
    send(raw){
      const message=JSON.parse(raw);
      if(message.type==='AUTH_HELLO')
        queueMicrotask(()=>this.emit('message',{data:JSON.stringify({
          type:'AUTH_CHALLENGE',deviceId:message.deviceId,challenge:'nonce-for-test'
        })}));
      if(message.type==='AUTH_PROOF')
        queueMicrotask(()=>this.emit('message',{data:JSON.stringify({
          type:'AUTH_OK',deviceId:message.deviceId,sessionTransferEnabled:true
        })}));
      if(message.type==='LIST_RULES')
        queueMicrotask(()=>this.emit('message',{data:JSON.stringify({
          type:'RULES',requestId:message.requestId,rules:[]
        })}));
    }
    close(){
      this.readyState=3;
      queueMicrotask(()=>this.emit('close',{}));
    }
  }
  globalThis.WebSocket=FakeWebSocket;
  const concurrent=await Promise.all(Array.from({length:8},()=>connectBrowser()));
  assert.equal(sockets,1,'overlapping alarms opened duplicate sockets');
  assert(concurrent.every(r=>r.state==='AUTHENTICATED'));
  assert.equal(currentConnectionState(),'AUTHENTICATED');
  const rules=await sendControl('LIST_RULES');
  assert.deepEqual(rules.rules,[]);
  disconnect();
  assert.equal(currentConnectionState(),'DISCONNECTED');
});
