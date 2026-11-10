// The extension submits the public connection ID; older enrollments may
// contain the internal ID. Both must resolve to the same active identity.
export function findBrowserIdentity(identities,reference){
  const requested=String(reference||'').trim();
  if(!requested)return null;
  return identities.find(item=>{
    const state=item.status??item.state;
    return (!state||state==='active')&&(item.id===requested||
      (!!item.shortId&&item.shortId.toLowerCase()===requested.toLowerCase()));
  })||null;
}
