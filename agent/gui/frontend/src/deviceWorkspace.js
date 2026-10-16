// A device appears here only through a capability the current Agent actually
// received from the Server. File access additionally requires live ACL proof.
import {onlyVerifiedFilePeers} from './fileShareVisibility.js';

const list=value=>Array.isArray(value)?value:[];
const cleanId=item=>typeof item?.deviceId==='string'&&item.deviceId.trim()?item.deviceId:
  typeof item?.id==='string'&&item.id.trim()?item.id:'';
const safeName=(value,fallback)=>typeof value==='string'&&value.trim()?value.trim():fallback;

export function buildDeviceInventory({status={},config={},exits=[],targets=[],filePeers=[]}={}){
 const local={
  id:'local',deviceId:status.deviceId||'',name:safeName(status.deviceName||config.deviceName,'本设备'),
  self:true,online:!!status.connected,capabilities:['local'],files:[],exit:null,rdp:null
 };
 // A stale local list is useful for debugging on the old pages, but must not
 // become an actionable remote-device inventory when the Relay is offline.
 if(!status.connected)return [local];
 const byId=new Map();
 const ensure=(id,name)=>{
  if(!id||id===status.deviceId||id.startsWith('local:'))return null;
  if(!byId.has(id))byId.set(id,{id,deviceId:id,name:safeName(name,id),self:false,
   online:false,capabilities:[],files:[],exit:null,rdp:null});
  const device=byId.get(id);
  if(device.name===id&&name)device.name=safeName(name,id);
  return device;
 };
 const add=(device,cap)=>{
  if(device&&!device.capabilities.includes(cap))device.capabilities.push(cap);
 };
 for(const exit of list(exits)){
  const device=ensure(cleanId(exit),exit.name||exit.deviceName);
  if(!device)continue;
  device.exit=exit;
  device.online ||= exit.online===true;
  add(device,'proxy.exit');
 }
 for(const target of list(targets)){
  const device=ensure(cleanId(target),target.name||target.deviceName);
  if(!device)continue;
  device.rdp=target;
  device.online ||= target.online===true;
  add(device,'rdp.host');
 }
 for(const peer of onlyVerifiedFilePeers(filePeers)){
  const device=ensure(peer.deviceId,peer.name);
  if(!device)continue;
  device.files=peer.shares;
  device.online=true;
  add(device,'files.host');
 }
 return [local,...[...byId.values()].sort((a,b)=>
  Number(b.online)-Number(a.online)||a.name.localeCompare(b.name,'zh-CN'))];
}

export function deviceActionAvailable(device,action){
 if(!device||device.self||!device.online)return false;
 if(action==='files')return device.capabilities.includes('files.host')&&device.files.length>0;
 if(action==='rdp')return device.capabilities.includes('rdp.host')&&device.rdp?.online===true;
 if(action==='exit')return device.capabilities.includes('proxy.exit')&&device.exit?.online===true;
 return false;
}

export const deviceCapabilityLabel={
 'proxy.exit':'网络出口','rdp.host':'远程桌面','files.host':'共享文件'
};
