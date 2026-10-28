import React, {useState} from 'react';
import {Badge,Button,Card,Field,Input,Metric,SaveBar,Select,Setting,Switch,Textarea} from './App.jsx';
import {ruleListText,splitRuleList} from './ruleLines.js';

const DEFAULT_PORT=15080,DEFAULT_LIMIT=1024;

export default function GatewayPage({config,status,exits,setv,save,dirty,onGoto}) {
 const [saving,setSaving]=useState(false);
 const g=config.gateway||{},s=status?.gateway||config.gatewayStatus||{};
 const linux=config.platform==='linux';
 const exitOptions=[...exits,...(config.customExits||[]).map(e=>({...e,online:e.enabled}))];
 const stateTone=!linux?'neutral':s.running?'ok':g.enabled?'warn':'neutral';
 const stateLabel=!linux?'当前平台不支持':s.running?'运行中':g.enabled?'已启用 · 未运行':'未启用';
 const apply=async()=>{
  if(!linux||saving)return;
  setSaving(true);
  try {
   await save({gateway:{...g,enabled:!!g.enabled,source_cidrs:splitRuleList(g.source_cidrs),port:Number(g.port)||0,max_connections:Number(g.max_connections)||0}});
  } finally {
   setSaving(false);
  }
 };

 return <>
  <Card title="网关接入" eyebrow="LAN GATEWAY">
   <div className="mini">LAN 设备把默认网关和 DNS 指向本机即可接入，无需安装客户端。仅支持 Linux IPv4 TCP / UDP；需安装 nftables、iproute2 并以 root 运行。</div>
   {!linux&&<div className="notice top-gap">此平台不支持网关接入。设备分组与出口选择仍可使用。</div>}
   <Setting title="启用局域网网关" desc="复用公共分流规则；规则可按「局域网网关」入口和来源网段匹配。"><Switch checked={!!g.enabled} disabled={!linux} label="启用局域网网关" onChange={v=>setv('gateway.enabled',v)}/></Setting>
   <div className="form-grid">
    <Field label="LAN 网卡" help="仅拦截从该网卡进入的流量，不能为 lo"><Input placeholder="eth0" disabled={!linux} value={g.interface||''} onChange={e=>setv('gateway.interface',e.target.value)}/></Field>
    <Field label="网关默认出口" help="网关流量未命中指定出口的规则时使用"><Select disabled={!linux} value={g.exit_id||''} onChange={e=>setv('gateway.exit_id',e.target.value)}>
     <option value="">跟随 Agent 默认出口</option>
     {exitOptions.map(e=><option key={e.deviceId||e.id} value={e.deviceId||e.id}>{e.name||e.id||e.deviceId}{e.online===false?' · 不可用':''}</option>)}
    </Select></Field>
    <Field label="允许接入的来源 IPv4 网段" help="可用换行、逗号或分号分隔，如 192.168.1.0/24；最多 32 个网段" className="full"><Textarea rows="3" disabled={!linux} placeholder="192.168.1.0/24" value={ruleListText(g.source_cidrs)} onChange={e=>setv('gateway.source_cidrs',e.target.value)}/></Field>
    <Field label="拦截端口" help={'1024–65535，默认 '+DEFAULT_PORT}><Input type="number" min="1024" max="65535" disabled={!linux} value={g.port||DEFAULT_PORT} onChange={e=>setv('gateway.port',Number(e.target.value))}/></Field>
    <Field label="最大并发连接" help={'1–65536，默认 '+DEFAULT_LIMIT}><Input type="number" min="1" max="65536" disabled={!linux} value={g.max_connections||DEFAULT_LIMIT} onChange={e=>setv('gateway.max_connections',Number(e.target.value))}/></Field>
   </div>
   <div className="mini top-gap">DNS 统一通过所选出口的加密 DNS 解析；IPv6 和其他转发协议会被阻断。</div>
   <div className="actions top-gap"><Button onClick={()=>onGoto('routing')}>编辑公共分流规则</Button></div>
  </Card>
  <div className="cards-4">
   <Metric label="活动连接" value={s.active||0} hint={s.interface?'网卡 '+s.interface:'等待启动'}/>
   <Metric label="TCP 连接" value={s.tcp||0} hint="累计透明转发"/>
   <Metric label="UDP 会话" value={s.udp||0} hint="累计透明转发"/>
   <Metric label="DNS 查询" value={s.dns||0} hint={(s.rejected||0)+' 次拒绝'}/>
  </div>
  <Card title="运行状态" eyebrow="GATEWAY STATUS" action={<Badge tone={stateTone}>{stateLabel}</Badge>}>
   {s.lastError&&<div className="notice warn">{s.lastError}</div>}
   <div className={s.lastError?'mini top-gap':'mini'}>停止或退出 Agent 会保留转发阻断规则，避免进程重启期间 LAN 流量绕过策略。关闭开关并保存会显式移除本功能的内核规则；不修改现有防火墙。</div>
  </Card>
  <SaveBar dirty={dirty} disabled={!linux||saving} label={saving?'正在应用…':'保存并应用网关'} hint={linux?'保存后立即应用：网关会按新配置重新接管 LAN 流量，无需重启客户端。':'当前平台不支持网关接入，无法保存网关设置。'} onSave={apply}/>
 </>;
}
