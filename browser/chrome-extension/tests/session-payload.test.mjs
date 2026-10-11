import test from 'node:test';
import assert from 'node:assert/strict';
import {packSessionPayload,unpackSessionPayload,MAX_SITE_PLAINTEXT_BYTES,MAX_WIRE_PLAINTEXT_BYTES} from '../src/session-payload.js';

test('legacy small session remains plain JSON and roundtrips',async()=>{
  const original={schemaVersion:2,siteOrigin:'https://example.test',
    cookies:[],storage:{local:{name:'Alice'},session:{csrf:'value'}}};
  const bytes=await packSessionPayload(original);
  assert.deepEqual(JSON.parse(new TextDecoder().decode(bytes)),original);
  assert.deepEqual(await unpackSessionPayload(bytes),original);
});

test('more than 180 KiB Storage is transmitted with all keys intact',async()=>{
  const original={schemaVersion:2,siteOrigin:'https://example.test',cookies:[],
    storage:{local:{a:'x'.repeat(350*1024),last:'do-not-drop'},
      session:{tab:'y'.repeat(220*1024),last:'session-kept'}}};
  const wire=await packSessionPayload(original);
  assert.ok(wire.length<MAX_WIRE_PLAINTEXT_BYTES);
  assert.ok(wire.length<new TextEncoder().encode(JSON.stringify(original)).length);
  assert.deepEqual(await unpackSessionPayload(wire),original);
});

test('incompressible payload under 1 MiB can use original JSON without loss',async()=>{
  const bytes=new Uint8Array(640*1024);
  // PRNG output is deterministic and does not rely on external entropy.
  let x=123456789;
  for(let i=0;i<bytes.length;i++){x^=x<<13;x^=x>>>17;x^=x<<5;bytes[i]=x&255;}
  const original={cookies:[],storage:{local:{random:Buffer.from(bytes).toString('base64')},session:{}}};
  const wire=await packSessionPayload(original);
  assert.ok(wire.length<=MAX_WIRE_PLAINTEXT_BYTES);
  assert.deepEqual(await unpackSessionPayload(wire),original);
});

test('compressed payload bigger than site ceiling is rejected before encrypt',async()=>{
  const original={cookies:[],storage:{local:{massive:'x'.repeat(MAX_SITE_PLAINTEXT_BYTES)},session:{}}};
  await assert.rejects(packSessionPayload(original),/5 MiB/);
});

test('bounded gzip decompression rejects bombs without truncating or returning partial state',async()=>{
  const data=new TextEncoder().encode(JSON.stringify({value:'x'.repeat(MAX_SITE_PLAINTEXT_BYTES+512)}));
  const compressed=await new Response(new Blob([data]).stream().pipeThrough(
    new CompressionStream('gzip'))).arrayBuffer();
  const wrapped={__relaySyncPayload:'browser.sync.gzip.v1',
    data:Buffer.from(compressed).toString('base64url')};
  await assert.rejects(unpackSessionPayload(new TextEncoder().encode(JSON.stringify(wrapped))),/5 MiB/);
});

test('tampered compressed wrapper format is rejected before applying keys',async()=>{
  for(const invalid of [
    {__relaySyncPayload:'browser.sync.gzip.v1',data:'*bad'},
    {__relaySyncPayload:'browser.sync.gzip.v1',data:'QQ',another:'unknown'},
    null,[],
  ]){
    await assert.rejects(unpackSessionPayload(new TextEncoder().encode(JSON.stringify(invalid))));
  }
});
