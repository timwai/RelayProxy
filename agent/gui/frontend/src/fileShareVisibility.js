// Accept only current, authenticated directory ACL responses from Agent.
// An outdated Agent/Server providing only device capabilities fails closed.
export function onlyVerifiedFilePeers(peers){
 if(!Array.isArray(peers))return [];
 const ids=new Set();
 const result=[];
 for(const peer of peers){
  if(!peer||peer.online!==true||typeof peer.deviceId!=='string'||!peer.deviceId||ids.has(peer.deviceId))continue;
  if(!Array.isArray(peer.shares))continue;
  const shares=peer.shares.filter(share=>share&&typeof share.id==='string'&&share.id.length>0&&
   (share.permission==='read'||share.permission==='read_write'));
  if(!shares.length)continue;
  ids.add(peer.deviceId);
  result.push({...peer,shares});
 }
 return result;
}
