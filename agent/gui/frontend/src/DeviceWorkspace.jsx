import React from 'react';
import {Icon} from './icons.jsx';
import {deviceActionAvailable,deviceCapabilityLabel} from './deviceWorkspace.js';
import './deviceWorkspace.css';

const formatId=id=>id&&id.length>28?id.slice(0,12)+'…'+id.slice(-9):id||'等待设备 ID';
function Pill({children,kind=''}){return <span className={'device-pill '+kind}>{children}</span>}
function Status({device,relayConnected}){
 const online=device.self?relayConnected:device.online;
 return <span className="device-state"><i className={online?'on':'off'}/>{online?'在线':'离线'}</span>;
}
function DeviceMark({device,large=false}){
 return <span className={'device-mark '+(device.self?'self ':'')+(large?'large':'')}>
  <Icon name={device.self?'monitor':'devices'} size={large?28:19}/>
 </span>;
}
function DeviceCard({device,relayConnected,onOpenDevice}){
 return <button type="button" className="device-tile" onClick={()=>onOpenDevice(device.id)}>
  <div className="device-tile-top"><DeviceMark device={device}/><Status device={device} relayConnected={relayConnected}/></div>
  <strong title={device.name}>{device.name}</strong>
  <small>{device.self?'当前正在使用的设备':formatId(device.id)}</small>
  <div className="device-tile-chips">{device.self?<Pill kind="blue">本机管理</Pill>:
   device.capabilities.map(cap=><Pill key={cap}>{deviceCapabilityLabel[cap]||cap}</Pill>)}</div>
  <span className="device-tile-open">打开设备工作台 <span aria-hidden="true">→</span></span>
 </button>;
}
export function DeviceHome({devices,status,onOpenDevice,onOpenPage,onRefresh}){
 const remotes=devices.filter(d=>!d.self),online=remotes.filter(d=>d.online).length;
 return <div className="device-home">
  <header className="page-head device-home-head"><div><div className="device-overline">DEVICE WORKSPACE</div><h1>我的设备</h1><p>先选择设备，再使用它已获得授权的能力。</p></div>
   <div className="actions"><button type="button" className="btn" onClick={onRefresh}><Icon name="activity"/> 刷新设备</button></div></header>
  <div className="device-summary">
   <div><span>当前身份</span><strong>{status.connected?(status.identityName||'已连接'):'未连接 Server'}</strong></div>
   <div><span>可访问设备</span><strong>{remotes.length}</strong></div>
   <div><span>在线设备</span><strong>{online}</strong></div>
   <div><span>当前连接</span><strong>{status.connected?(status.latency||0)+' ms':'离线'}</strong></div>
  </div>
  <div className="device-section-label"><h2>本机</h2><span>管理当前设备的代理、共享与运行环境</span></div>
  <div className="device-grid local-grid">{devices.filter(d=>d.self).map(d=>
    <DeviceCard key={d.id} device={d} relayConnected={!!status.connected} onOpenDevice={onOpenDevice}/>)}</div>
  <div className="device-section-label"><h2>已授权的远程设备</h2><span>按服务端授权及共享目录 ACL 显示</span></div>
  {remotes.length?<div className="device-grid">{remotes.map(d=>
   <DeviceCard key={d.id} device={d} relayConnected={!!status.connected} onOpenDevice={onOpenDevice}/>)}</div>:
  <div className="device-empty"><Icon name="devices" size={30}/><strong>暂无可访问的远程设备</strong>
   <p>需要在 Server 审批相关能力。共享文件夹还需要由目录所有者单独授权。</p>
   <button className="btn" type="button" onClick={()=>onOpenPage('devices')}>查看本机身份和授权</button></div>}
  <div className="device-section-label"><h2>全局活动</h2></div>
  <div className="device-activity-actions">
   <button className="btn" onClick={()=>onOpenPage('messages')}><Icon name="bell"/> 消息</button>
   <button className="btn" onClick={()=>onOpenPage('monitor')}><Icon name="activity"/> 实时连接</button>
   <button className="btn" onClick={()=>onOpenPage('diagnostics')}><Icon name="terminal"/> 诊断与日志</button>
  </div>
 </div>;
}
const LOCAL_SECTIONS=[
 {heading:'网络和连接',items:[
  ['overview','dashboard','运行概览','本机连接状态、流量与路径'],
  ['proxy','swap','本机代理','透明代理、SOCKS5 与 HTTP'],
  ['routing','split','分流规则','按域名、进程和订阅调度出口'],
  ['dns','shield','DNS 与防泄漏','本机解析和保护策略'],
  ['exits','globe','授权出口','选择此设备使用的网络出口'],
  ['connection','route','连接与路径','QUIC、P2P 与网络路径']
 ]},
 {heading:'共享与远程',items:[
  ['files','folder','文件共享','本机共享目录、授权和传输任务'],
  ['exitshare','share','出口共享','让授权设备使用本机网络出口'],
  ['rdp','monitor','远程桌面','本机 RDP 能力与已授权远程目标']
 ]},
 {heading:'设备维护',items:[
  ['devices','devices','身份与权限','设备身份、Server 授权状态'],
  ['messages','bell','消息','接收和查看设备消息'],
  ['diagnostics','terminal','诊断日志','排查本机网络和传输问题'],
  ['settings','settings','偏好设置','本机启动、外观及服务配置']
 ]}
];
export function DeviceDetail({device,devices,status,config,selectedExit,onOpenDevice,
 onOpenPage,onOpenFiles,onConnectRDP,onDisconnectRDP,onSelectExit,onRefresh,busyRDP}){
 if(!device)return <div className="device-missing"><h2>设备已不可访问</h2>
  <p>设备可能离线、已被撤销权限，或当前 Relay 会话需要重新认证。</p>
  <button className="btn primary" onClick={()=>onOpenPage('fleet')}>返回我的设备</button></div>;
 const local=device.self;
 const activeRDP=status.rdpTargetId===device.id;
 return <div className="device-detail">
  <button className="device-back" onClick={()=>onOpenPage('fleet')}>‹ 返回我的设备</button>
  <div className="device-detail-hero">
   <DeviceMark device={device} large/>
   <div className="grow"><div className="device-overline">{local?'THIS DEVICE':'AUTHORIZED DEVICE'}</div>
    <h1>{device.name}</h1><div className="mono mini" title={device.deviceId||device.id}>{formatId(device.deviceId||device.id)}</div></div>
   <Status device={device} relayConnected={!!status.connected}/>
   <button className="btn" onClick={onRefresh} aria-label="刷新设备状态"><Icon name="activity"/> 刷新</button>
  </div>
  {local?<>
   <div className="device-hero-info"><div><span>身份</span><b>{status.identityName||config.identityId||'未绑定'}</b></div>
    <div><span>当前出口</span><b>{selectedExit||'自动选择'}</b></div>
    <div><span>Server</span><b>{status.connected?'已连接':'未连接'}</b></div></div>
   {LOCAL_SECTIONS.map(section=><section className="device-section" key={section.heading}>
    <div className="device-section-label"><h2>{section.heading}</h2></div>
    <div className="device-cap-grid">{section.items.map(([page,icon,title,desc])=>
     <button type="button" className="device-cap-card" key={page} onClick={()=>onOpenPage(page)}>
      <span className="device-cap-icon"><Icon name={icon} size={20}/></span><span className="grow"><b>{title}</b><small>{desc}</small></span><span className="device-arrow">›</span>
     </button>)}</div></section>)}
  </>:<>
   <div className="device-hero-info"><div><span>已授权能力</span><b>{device.capabilities.length} 项</b></div>
    <div><span>文件夹权限</span><b>{device.files.length} 个共享目录</b></div>
    <div><span>运行状态</span><b>{device.online?'在线':'离线，暂不可操作'}</b></div></div>
   {!device.online&&<div className="notice warn">设备目前离线，无法开始新的远程桌面、文件传输或网络出口会话。</div>}
   <section className="device-section"><div className="device-section-label"><h2>此设备提供的服务</h2><span>只显示已授权的能力</span></div>
    <div className="device-cap-grid">
     {device.capabilities.includes('rdp.host')&&<div className="device-cap-card static">
      <span className="device-cap-icon"><Icon name="monitor" size={20}/></span>
      <span className="grow"><b>远程桌面</b><small>{activeRDP?'RDP 会话已连接':'访问该设备的桌面'}</small></span>
      {activeRDP?<button className="btn danger" onClick={onDisconnectRDP}>断开</button>:
       <button className="btn primary" disabled={!deviceActionAvailable(device,'rdp')||!!busyRDP}
        onClick={()=>onConnectRDP(device.id)}>{busyRDP?'连接中':'连接'}</button>}
     </div>}
     {device.capabilities.includes('files.host')&&<div className="device-cap-card static">
      <span className="device-cap-icon"><Icon name="folder" size={20}/></span>
      <span className="grow"><b>文件共享</b><small>{device.files.length} 个可访问文件夹</small></span>
      <button className="btn primary" disabled={!deviceActionAvailable(device,'files')}
       onClick={()=>onOpenFiles(device.id)}>浏览文件</button>
     </div>}
     {device.capabilities.includes('proxy.exit')&&<div className="device-cap-card static">
      <span className="device-cap-icon"><Icon name="globe" size={20}/></span>
      <span className="grow"><b>网络出口</b><small>{selectedExit===device.id?'本机正在使用此出口':'通过该设备转发代理流量'}</small></span>
      <button className="btn primary" disabled={!deviceActionAvailable(device,'exit')||selectedExit===device.id}
       onClick={()=>onSelectExit(device.id)}>{selectedExit===device.id?'已选用':'设为出口'}</button>
     </div>}
    </div>
   </section>
   {device.files.length>0&&<section className="device-section">
    <div className="device-section-label"><h2>已授权文件夹</h2></div>
    <div className="device-folder-list">{device.files.map(s=>
     <button className="device-folder-row" key={s.id} onClick={()=>onOpenFiles(device.id)}>
      <Icon name="folder" size={19}/><strong>{s.name||'共享目录'}</strong>
      <Pill kind={s.permission==='read_write'?'green':''}>{s.permission==='read_write'?'读写':'只读'}</Pill>
      <span className="device-arrow">›</span>
     </button>)}</div>
   </section>}
   <div className="device-note">设备可用能力来自当前 Server 会话，文件夹列表还需经共享端实时 ACL 确认。更改授权请前往 Server 管理端。</div>
  </>}
 </div>;
}
