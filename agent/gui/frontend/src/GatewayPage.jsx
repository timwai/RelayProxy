import React, {useState} from 'react';
import {splitRuleList} from './ruleLines.js';

export default function GatewayPage({config,status,exits,setv,save,dirty,onGoto}) {
 const [saving,setSaving]=useState(false);
 const g=config.gateway||{},s=status?.gateway||config.gatewayStatus||{};
 const linux=config.platform==='linux';
 const apply=async()=>{
  if(!linux||saving)return;
  setSaving(true);
  try {
   await save({gateway:{...g,source_cidrs:splitRuleList(g.source_cidrs)}});
  } finally {
   setSaving(false);
  }
 };

 return <>
  <section className="card">
   <div className="card-head">
    <div><h2>网关接入</h2><div className="mini" role="status">{saving?'正在应用配置…':dirty?'存在未保存的修改':'所有修改已保存'}</div></div>
    <button type="button" className="btn primary" aria-label="保存并应用网关" aria-busy={saving} disabled={!linux||saving} onClick={apply}>{saving?'正在应用…':'保存并应用'}</button>
   </div>
   <p className="mini">LAN 设备将默认网关和 DNS 指向本机，无需安装客户端。仅支持 Linux IPv4 TCP / UDP。</p>
   {!linux&&<div className="notice">此平台不支持网关接入。设备分组与出口选择仍可使用。</div>}
   <label className="check-line"><input type="checkbox" disabled={!linux} checked={!!g.enabled} onChange={e=>setv('gateway.enabled',e.target.checked)}/>启用局域网网关</label>
   <div className="form-grid">
    <label className="field">LAN 网卡<input className="control" placeholder="eth0" value={g.interface||''} onChange={e=>setv('gateway.interface',e.target.value)}/></label>
    <label className="field">默认出口<select className="control" value={g.exit_id||''} onChange={e=>setv('gateway.exit_id',e.target.value)}>
     <option value="">跟随 Agent 默认出口</option>
     {[...exits,...(config.customExits||[]).map(e=>({...e,online:e.enabled}))].map(e=><option key={e.deviceId||e.id} value={e.deviceId||e.id}>{e.name||e.id||e.deviceId}{e.online===false?' · 不可用':''}</option>)}
    </select></label>
    <label className="field full">允许接入的来源 IPv4 网段<textarea className="control" rows="3" placeholder="192.168.1.0/24" value={Array.isArray(g.source_cidrs)?g.source_cidrs.join('\n'):g.source_cidrs||''} onChange={e=>setv('gateway.source_cidrs',e.target.value)}/></label>
    <label className="field">拦截端口<input className="control" type="number" min="1024" max="65535" value={g.port||15080} onChange={e=>setv('gateway.port',Number(e.target.value))}/></label>
    <label className="field">最大并发连接<input className="control" type="number" min="1" max="65536" value={g.max_connections||1024} onChange={e=>setv('gateway.max_connections',Number(e.target.value))}/></label>
   </div>
   <p className="mini">需安装 nftables、iproute2 并以 root 运行。复用公共分流规则，可按「网关」入口和来源网段匹配。DNS 统一通过所选出口的加密 DNS；IPv6 和其他转发协议会被阻断。</p>
   <button type="button" className="btn" onClick={()=>onGoto('routing')}>编辑公共分流规则</button>
  </section>
  <section className="card">
   <h2>运行状态 · {s.running?'运行中':'未运行'}</h2>
   <p>活动连接 {s.active||0} · TCP {s.tcp||0} · UDP {s.udp||0} · DNS {s.dns||0} · 拒绝 {s.rejected||0}</p>
   {s.lastError&&<div className="notice warn">{s.lastError}</div>}
   <p className="mini">停止或退出 Agent 会保留转发阻断规则。取消勾选网关并保存会显式移除本功能的内核规则；不修改现有防火墙。</p>
  </section>
 </>;
}
