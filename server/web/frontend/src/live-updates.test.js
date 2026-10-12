import test from 'node:test';
import assert from 'node:assert/strict';
import {subscribeLiveUpdates} from './live-updates.js';

test('shared SSE connection coalesces invalidations and stops on last unsubscribe',async()=>{
  const prior={window:globalThis.window,document:globalThis.document,EventSource:globalThis.EventSource};
  const events=new Map();
  let opened=0,closed=0;
  class FakeSource {
    constructor(path,options){
      assert.equal(path,'/api/v1/live/events');
      assert.equal(options.withCredentials,true);
      this.listeners=new Map();
      opened++;
      FakeSource.latest=this;
    }
    addEventListener(type,fn){this.listeners.set(type,fn)}
    emit(type){this.listeners.get(type)?.()}
    close(){closed++}
  }
  globalThis.window={
    addEventListener(type,fn){events.set('w:'+type,fn)},
    removeEventListener(type){events.delete('w:'+type)}
  };
  globalThis.document={
    visibilityState:'visible',
    addEventListener(type,fn){events.set('d:'+type,fn)},
    removeEventListener(type){events.delete('d:'+type)}
  };
  globalThis.EventSource=FakeSource;
  try{
    let a=0,b=0;
    const unsubA=subscribeLiveUpdates(()=>a++);
    const unsubB=subscribeLiveUpdates(()=>b++);
    assert.equal(opened,1,'each panel must not open its own socket');
    FakeSource.latest.emit('invalidate');
    FakeSource.latest.emit('invalidate');
    await new Promise(resolve=>setTimeout(resolve,220));
    assert.equal(a,1);
    assert.equal(b,1);
    unsubA();
    FakeSource.latest.emit('invalidate');
    await new Promise(resolve=>setTimeout(resolve,220));
    assert.equal(a,1);
    assert.equal(b,2);
    unsubB();
    assert.equal(closed,1);
    assert.equal(events.size,0,'global listeners must be cleaned up');
  }finally{
    globalThis.window=prior.window;
    globalThis.document=prior.document;
    globalThis.EventSource=prior.EventSource;
  }
});
