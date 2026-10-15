// Accept only peers for which the authenticated local Agent confirmed at
// least one current, readable shared folder on the target host.
// A Server files.host grant or an older Agent's bare peers list is not enough.
export function onlyVerifiedFilePeers(peers){
 if(!Array.isArray(peers))return [];
 const ids=new Set();
 return peers.filter(peer=>{
  if(!peer||peer.online!==true||typeof peer.deviceId!=='string'||!peer.deviceId||ids.has(peer.deviceId))return false;
  if(!Array.isArray(peer.shares)||!peer.shares.some(share=>
   share&&typeof share.id==='string'&&share.id.length>0&&
   (share.permission==='read'||share.permission==='read_write')))return false;
  ids.add(peer.deviceId);
  return true;
 });
}
