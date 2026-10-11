// Per-snapshot E2EE payload codec. The gzip wrapper is encrypted and signed
// inside the existing browser.sync.v1 envelope; Server never decompresses or
// receives any Cookie/Storage plaintext. Older senders' plain JSON still works.
export const MAX_SITE_PLAINTEXT_BYTES=5*1024*1024;
export const MAX_WIRE_PLAINTEXT_BYTES=1024*1024-16; // reserve AES-GCM tag
const COMPRESS_AFTER_BYTES=180*1024;
const CODEC='browser.sync.gzip.v1';
const encode=value=>new TextEncoder().encode(value);
const decode=bytes=>new TextDecoder('utf-8',{fatal:true}).decode(bytes);

function b64url(bytes){
  // Chunk to avoid stack exhaustion with megabyte payloads.
  let result='';
  for(let i=0;i<bytes.length;i+=8192){
    result+=String.fromCharCode(...bytes.subarray(i,i+8192));
  }
  return btoa(result).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/g,'');
}
function unb64url(value){
  if(typeof value!=='string'||!value.length||value.length>MAX_WIRE_PLAINTEXT_BYTES*2||
     !/^[A-Za-z0-9_-]+$/.test(value))throw new Error('压缩快照编码无效');
  const raw=atob(value.replace(/-/g,'+').replace(/_/g,'/')+
    '='.repeat((4-value.length%4)%4));
  return Uint8Array.from(raw,c=>c.charCodeAt(0));
}
async function gzip(bytes){
  const stream=new Blob([bytes]).stream().pipeThrough(new CompressionStream('gzip'));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}
async function gunzipBounded(bytes){
  if(typeof DecompressionStream!=='function')throw new Error('当前 Chrome 不支持安全解压 Storage');
  const reader=new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip')).getReader();
  const parts=[];
  let total=0;
  try{
    for(;;){
      const {done,value}=await reader.read();
      if(done)break;
      total+=value.byteLength;
      if(total>MAX_SITE_PLAINTEXT_BYTES)throw new Error('解压后的站点状态超过 5 MiB 安全上限');
      parts.push(value);
    }
  }catch(error){
    await reader.cancel().catch(()=>{});
    throw error;
  }finally{reader.releaseLock();}
  const out=new Uint8Array(total);
  let offset=0;
  for(const part of parts){out.set(part,offset);offset+=part.length;}
  return out;
}
export async function packSessionPayload(payload){
  const raw=encode(JSON.stringify(payload));
  if(raw.length>MAX_SITE_PLAINTEXT_BYTES)
    throw new Error('完整站点状态超过 5 MiB 安全上限，没有丢弃 Cookie 或 Storage Key');
  if(raw.length<=COMPRESS_AFTER_BYTES)return raw;
  if(typeof CompressionStream!=='function'){
    if(raw.length<=MAX_WIRE_PLAINTEXT_BYTES)return raw;
    throw new Error('当前 Chrome 不支持压缩大型会话，请升级浏览器');
  }
  const compressed=await gzip(raw);
  const wrapped=encode(JSON.stringify({__relaySyncPayload:CODEC,data:b64url(compressed)}));
  // Keep plain JSON for backward compatibility when compression is pointless.
  const preferred=wrapped.length<raw.length?wrapped:raw;
  const wire=preferred.length<=MAX_WIRE_PLAINTEXT_BYTES?preferred:
    raw.length<=MAX_WIRE_PLAINTEXT_BYTES?raw:wrapped;
  if(wire.length>MAX_WIRE_PLAINTEXT_BYTES)
    throw new Error('完整站点状态压缩后仍超过 1 MiB 单帧限制；未截断任何 Key，请清理无关网站缓存后再同步');
  return wire;
}
export async function unpackSessionPayload(wire){
  if(!(wire instanceof Uint8Array)||wire.length>MAX_WIRE_PLAINTEXT_BYTES)
    throw new Error('加密会话载荷超出限制');
  const data=JSON.parse(decode(wire));
  if(data&&typeof data==='object'&&data.__relaySyncPayload===CODEC){
    // No other fields allowed in the wrapper; all bytes were AES-GCM authenticated.
    if(Object.keys(data).length!==2)throw new Error('压缩会话格式无效');
    const inflated=await gunzipBounded(unb64url(data.data));
    const parsed=JSON.parse(decode(inflated));
    if(!parsed||typeof parsed!=='object'||Array.isArray(parsed))
      throw new Error('站点状态格式无效');
    return parsed;
  }
  if(!data||typeof data!=='object'||Array.isArray(data))
    throw new Error('站点状态格式无效');
  return data;
}
