// Visual grouping and safe selection for device capabilities.
export const capabilityGroups=[
 {id:'network',title:'网络代理',description:'允许设备建立代理连接，或对外提供网络出口',icon:'globe',items:[
  {id:'proxy.client',title:'代理客户端',description:'经授权出口访问网络',icon:'route'},
  {id:'proxy.exit',title:'出口节点',description:'向其他授权设备提供网络出口',icon:'globe'}
 ]},
 {id:'remote',title:'远程桌面',description:'控制端与被控端能力独立授权，公网入口需要 RDP 主机',icon:'monitor',items:[
  {id:'rdp.controller',title:'RDP 控制端',description:'主动连接已授权的远程桌面',icon:'monitor'},
  {id:'rdp.host',title:'RDP 主机',description:'作为远程桌面连接目标',icon:'devices'},
  {id:'rdp.public',title:'RDP 公网入口',description:'允许该 RDP 主机使用公网入口',icon:'shield'}
 ]}
];
export const allCapabilities=capabilityGroups.flatMap(group=>group.items.map(item=>item.id));
export function visibleCapabilityGroups(allowed=allCapabilities){
 const permit=new Set(allowed);
 return capabilityGroups.map(group=>({...group,items:group.items.filter(item=>permit.has(item.id))})).filter(group=>group.items.length);
}
export function toggleDeviceCapability(current,key,enabled,allowed=allCapabilities){
 const permit=new Set(allowed);
 if(!permit.has(key))return allCapabilities.filter(id=>permit.has(id)&&current.includes(id));
 if(key==='rdp.public' && enabled && !permit.has('rdp.host'))return allCapabilities.filter(id=>permit.has(id)&&current.includes(id)&&id!=='rdp.public');
 const next=new Set(current.filter(id=>permit.has(id)));
 if(enabled)next.add(key);else next.delete(key);
 if(key==='rdp.public'&&enabled)next.add('rdp.host');
 if(key==='rdp.host'&&!enabled)next.delete('rdp.public');
 return allCapabilities.filter(id=>next.has(id));
}
