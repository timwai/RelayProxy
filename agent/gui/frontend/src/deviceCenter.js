import {onlyVerifiedFilePeers} from './fileShareVisibility.js';

// Merge distinct, server-authorized capabilities by actual device ID.
// A file-share host appears ONLY after live directory ACL verification.
// Local custom proxies are configuration entries, not remote device identities.
export function buildDeviceCenter({proxyExits=[],rdpTargets=[],filePeers=[]}={}){
 const devices=new Map();
 const add=(id,name)=>{
  if(typeof id!=='string'||!id)return null;
  if(!devices.has(id))devices.set(id,{id,name:name||id,proxy:null,rdp:null,shares:[],online:false});
  const device=devices.get(id);
  if(name&&(device.name===device.id||!device.name))device.name=name;
  return device;
 };
 for(const exit of Array.isArray(proxyExits)?proxyExits:[]){
  const id=exit?.deviceId||exit?.id;
  // Locally configured SOCKS5/HTTP exits are not remote devices.
  if(typeof id!=='string'||!id||id.startsWith('local:'))continue;
  const d=add(id,exit.name||exit.deviceName);
  if(!d)continue;
  d.proxy=exit;
  if(exit.online===true)d.online=true;
 }
 for(const target of Array.isArray(rdpTargets)?rdpTargets:[]){
  const id=target?.deviceId||target?.id;
  const d=add(id,target?.name||target?.deviceName);
  if(!d)continue;
  d.rdp=target;
  if(target.online===true)d.online=true;
 }
 for(const peer of onlyVerifiedFilePeers(filePeers)){
  const d=add(peer.deviceId,peer.name||peer.deviceName);
  if(!d)continue;
  d.shares=peer.shares;
  d.online=true; // Verified shares are only returned from currently online hosts.
 }
 return [...devices.values()].sort((a,b)=>Number(b.online)-Number(a.online)||a.name.localeCompare(b.name,'zh-CN'));
}
