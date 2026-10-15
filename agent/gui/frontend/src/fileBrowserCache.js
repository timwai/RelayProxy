// Short-lived, bounded UI-only metadata cache. File bytes are never cached.
// Entries are scoped to the authenticated peer, share and directory path.
// The Server/host continues to check ACLs on every actual network request.
export function createDirectoryCache({ttlMs=6000,maxEntries=48,now=()=>Date.now()}={}){
 const cache=new Map();
 const key=(device,share,path)=>JSON.stringify([device,share,path]);
 return {
  get(device,share,path){
   const id=key(device,share,path),item=cache.get(id);
   if(!item)return null;
   if(now()-item.time>=ttlMs){cache.delete(id);return null}
   cache.delete(id);cache.set(id,item);
   return item.entries.slice();
  },
  set(device,share,path,entries){
   if(!Array.isArray(entries))return;
   const id=key(device,share,path);
   cache.delete(id);
   cache.set(id,{device,time:now(),entries:entries.slice()});
   while(cache.size>maxEntries)cache.delete(cache.keys().next().value);
  },
  delete(device,share,path){cache.delete(key(device,share,path))},
  clearDevice(device){
   for(const [id,value] of cache)if(value.device===device)cache.delete(id);
  },
  clear(){cache.clear()},
  get size(){return cache.size},
 };
}
