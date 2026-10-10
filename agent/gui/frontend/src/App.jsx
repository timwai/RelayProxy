import React, {useCallback,useEffect,useMemo,useRef,useState} from 'react';
import {call,callJSON,hasBridge,installNativeHooks,parseMutation,saveConfig} from './bridge.js';
import {exitInventoryKey as makeExitInventoryKey,exitUsable,selectedExitUnavailable} from './exitInventory.js';
import {PROTOCOL_CHOICES,dropTargetIndex,moveRule,normalizeRuleLists,protocolChoice,protocolsFromChoice,ruleListText,splitRuleList} from './ruleLines.js';
import {Icon} from './icons.jsx';
import {enableFakeIP,setDoHBlocking,setOtherDNSForwarding,setAutoDNS,setProxyDNS} from './dnsSettings.js';
import {routingPagePatch,dnsPagePatch} from './configPatches.js';

const NAV=[
 {group:'概览',items:[['overview','dashboard','运行概览'],['devices','devices','身份与设备']]},
 {group:'连接',items:[['connection','route','连接与路径'],['exits','globe','出口选择']]},
 {group:'本机',items:[['proxy','swap','本机代理'],['routing','split','分流规则'],['dns','shield','DNS 与防泄漏'],['exitshare','share','本机出口共享'],['rdp','monitor','远程桌面']]},
 {group:'观察',items:[['messages','bell','消息'],['monitor','activity','实时监控'],['diagnostics','terminal','诊断与日志']]}
];

// The browser's /connections bookmark and shareable ?page=... links should
// open the same React route as the desktop shell. Never trust arbitrary page
// names or persist unknown values into the navigation state.
const PAGE_IDS = new Set([...NAV.flatMap(group => group.items.map(item => item[0])), 'settings']);
function initialPage() {
 const requested = new URLSearchParams(window.location.search).get('page');
 if (PAGE_IDS.has(requested)) return requested;
 const previous = sessionStorage.getItem('relayproxy-react-page');
 return PAGE_IDS.has(previous) ? previous : 'overview';
}

const EMPTY={
 revision:'',serverAddress:'',quicPort:35820,tcpPort:35821,tlsEnabled:true,insecureTls:false,deviceName:'',identityId:'',transport:'auto',
 socks5:{enabled:true,listen:'127.0.0.1',port:1080},http:{enabled:true,listen:'127.0.0.1',port:8080},defaultExitId:'',customExits:[],upstreamExitId:'',
 exitEnabled:true,allowInternet:true,allowPrivateNetwork:false,allowLoopback:false,accessMode:'',accessDomains:[],accessCidrs:[],
 exitUpstream:{mode:'',address:'',username:'',password:''},
 rdp:{enabled:true,address:'127.0.0.1:3389'},
 p2p:{enabled:true,mode:'auto',punchTimeoutMs:1200,keepaliveSec:10,idleTimeoutSec:120,maxExitSessions:4,fallback:true,upnpAllowed:false},publicDirectAdvertise:'',
 networkMode:'',network:{mode:'',exclude_processes:[]},networkCapabilities:{},isAutostart:false,minimizeToTray:true,theme:'system',
 verificationPopupTimeoutSec:15,routing:{mode:'global_proxy',dns_mode:'proxy',auto_detect_dns:false,dns_association_enabled:true,proxy_dns_enabled:false,fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false,dns_exit_id:'',doh_blocked_ips:[],default_action:'PROXY',rules:[]},configPath:'',version:''
};

const cx=(...v)=>v.filter(Boolean).join(' ');
const arr=v=>Array.isArray(v)?v:[];
const fmtTime=v=>{if(!v)return '—';const d=new Date(v);return Number.isNaN(d.getTime())?String(v):d.toLocaleString()};
const fmtBytes=(v,rate=false)=>{let n=Math.max(0,Number(v)||0),i=0;const u=['B','KB','MB','GB','TB'];while(n>=1024&&i<u.length-1){n/=1024;i++}return n.toFixed(i===0?0:n>=100?0:n>=10?1:2)+' '+u[i]+(rate?'/s':'')};
const logText=v=>{if(typeof v==='string')return v;if(!v)return'';const message=v.line||v.message||v.text;if(message&&v.timestamp)return v.timestamp+'  '+message;return message||JSON.stringify(v)};
const tone=v=>{const s=String(v||'').toLowerCase();if(/active|ready|connected|online|approved|success|直连/.test(s))return'ok';if(/error|failed|reject|deny|offline|失败|拒绝/.test(s))return'danger';if(/connecting|pending|wait|准备|协商/.test(s))return'warn';return'neutral'};
const exitName=(exits,id)=>{if(!id)return'自动选择';const e=arr(exits).find(x=>(x.deviceId||x.id)===id);return(e&&(e.name||e.deviceName))||id};

function Button({children,primary,danger,quiet,className,...p}){return <button className={cx('btn',primary&&'primary',danger&&'danger',quiet&&'quiet',className)} {...p}>{children}</button>}
function Badge({children,tone:t='neutral'}){return <span className={cx('badge',t)}>{children}</span>}
function Switch({checked,onChange,disabled,label}){return <button type="button" className={cx('switch',checked&&'on')} role="switch" aria-checked={!!checked} aria-label={label} disabled={disabled} onClick={()=>onChange&&onChange(!checked)}><span/></button>}
function Card({title,eyebrow,action,children,className}){return <section className={cx('card',className)}>{(title||eyebrow||action)&&<div className="card-head"><div>{eyebrow&&<div className="eyebrow">{eyebrow}</div>}{title&&<h2>{title}</h2>}</div>{action}</div>}{children}</section>}
function PageHead({title,desc,actions}){return <div className="page-head"><div><h1>{title}</h1>{desc&&<p>{desc}</p>}</div>{actions&&<div className="actions">{actions}</div>}</div>}
function Field({label,help,children,className}){return <label className={cx('field',className)}><span>{label}</span>{children}{help&&<small>{help}</small>}</label>}
const Input=p=><input className="control" {...p}/>;
const Select=p=><select className="control" {...p}/>;
const Textarea=p=><textarea className="control textarea" {...p}/>;
function Setting({title,desc,children}){return <div className="setting"><div className="grow"><b>{title}</b>{desc&&<div className="mini">{desc}</div>}</div>{children}</div>}
const SPARK_POINTS=24;
function Spark({series}){const pts=arr(series).slice(-SPARK_POINTS),max=Math.max(0,...pts);if(!pts.length)return <div className="spark idle" aria-hidden="true"/>;return <div className="spark" title={'最近 '+pts.length+' 次采样'}>{pts.map((v,i)=><i key={i} style={{height:(max>0?Math.max(8,Math.round(v/max*100)):8)+'%'}}/>)}</div>}
function Metric({label,value,hint,series}){return <div className="metric"><div className="eyebrow">{label}</div><div className="metric-value">{value}</div><div className="mini">{hint}</div>{series&&<Spark series={series}/>}</div>}
function SaveBar({dirty,label,onSave,hint,children}){return <div className={cx('save-bar',dirty&&'dirty')}><div className="grow"><b>{dirty?'存在未保存的修改':'所有修改已保存'}</b>{hint&&<div className="mini">{hint}</div>}</div>{children}<Button primary onClick={onSave}>{label}</Button></div>}
function Modal({open,title,children,footer,onClose,wide}){if(!open)return null;return <div className="overlay show" onMouseDown={e=>e.target===e.currentTarget&&onClose&&onClose()}><div className={cx('modal',wide&&'wide')}><div className="modal-head"><h2>{title}</h2><button className="close" onClick={onClose}>×</button></div><div className="modal-body">{children}</div>{footer&&<div className="modal-foot">{footer}</div>}</div></div>}

function Overview({status,config,exits,traffic,history,onRefresh,onGoto}){
 const connected=!!status.connected,rawPath=status.directPath||status.p2pPath||status.transport||'',path=connected?(rawPath||'等待路径'):'未连接',rtt=connected?(status.directRttMs||status.p2pRttMs||status.latency||0):0;
 const approval=String(status.approvalState||''),approvalLower=approval.toLowerCase(),approvalLabel=connected?(approval||'已连接'):(/pending|wait|approval|待审批/.test(approvalLower)||/reject|denied|revoked|撤销|拒绝/.test(approvalLower)?approval:'未连接');
 const directActive=connected&&/public-direct/i.test(String(status.directPath||'')),p2pActive=connected&&/p2p/i.test(String(status.p2pPath||'')),relayPath=connected&&/relay/i.test(String(rawPath||''));
 const udp=Number(status.nativeUdp?.associations||0);
 return <><PageHead title="运行概览" desc="连接状态、当前出口、数据路径、本机代理与实时流量。" actions={<><Button onClick={()=>onGoto('diagnostics')}>诊断</Button><Button onClick={onRefresh}>刷新</Button><Button primary onClick={()=>onGoto('exits')}>切换出口</Button></>}/>
 <div className="cards-2">
  <Card className="hero"><div className="hero-glow"/><div className="hero-status"><span className={cx('dot',connected?'online':'offline')}/><div><div className="eyebrow">{connected?'CONNECTED':'DISCONNECTED'}</div><div className="big">{connected?'RelayProxy 已连接':'等待连接到 RelayProxy Server'}</div><div className="muted">{((connected?(config.runtime?.serverAddress||config.serverAddress):config.serverAddress)||'尚未配置服务器')+' · '+(status.transport||config.runtime?.transport||config.transport||'auto')}{connected&&config.runtime?.serverAddress&&config.runtime.serverAddress!==config.serverAddress?' · 已保存新 Server，待重启':''}</div></div><Badge tone={connected?'ok':/reject|denied|revoked|撤销|拒绝/i.test(approvalLabel)?'danger':'neutral'}>{approvalLabel}</Badge></div>
   <div className="exit-mini"><div className="flag">↗</div><div className="grow"><div className="mini">当前出口</div><strong>{exitName(exits,status.selectedExit||config.defaultExitId)}</strong><div className="mini">{path+' · '+(rtt?rtt+' ms':'等待延迟数据')}</div><div className="chips top-gap">{directActive&&<Badge tone="ok">公网直连</Badge>}{p2pActive&&<Badge tone="blue">P2P</Badge>}{udp>0&&<Badge tone="blue">UDP 原生数据报</Badge>}</div></div><Button quiet onClick={()=>onGoto('exits')}>›</Button></div>
   <svg className="map-line" viewBox="0 0 520 160"><path d="M18 112 C122 40 205 138 302 68 S442 54 510 22"/><circle cx="18" cy="112" r="5"/><circle cx="302" cy="68" r="5"/><circle cx="510" cy="22" r="5"/></svg>
  </Card>
  <Card title="路径状态" eyebrow="DATA PATH" action={<Badge tone={connected?'ok':'neutral'}>{connected?'在线':'离线'}</Badge>}>
   <Setting title="Public Direct" desc={connected?(status.directEndpoint||'Server 验证公网端点'):'等待 Relay 重连后重新确认'}><Badge tone={directActive?'ok':connected&&status.directState?'blue':'neutral'}>{connected?(directActive?'Active':status.directState||'Idle'):'Offline'}</Badge></Setting>
   <Setting title="UPnP 网关映射" desc={status.upnpError||status.upnpAddress||'仅在本机与 Server 同时允许时使用，映射成功不代表公网可达。'}><Badge tone={status.upnpState==='MAPPED'?'ok':status.upnpState==='DEGRADED'||status.upnpState==='FAILED'?'danger':status.upnpState==='CGNAT'?'warn':'neutral'}>{status.upnpState||'DISABLED'}</Badge></Setting><Setting title="P2P QUIC" desc={connected?(status.p2pCandidateSummary||'公网直连不可用时尝试候选路径'):'等待 Relay 重连后重新协商'}><Badge tone={p2pActive?'ok':connected&&status.p2pState?'blue':'neutral'}>{connected?(p2pActive?'Active':status.p2pState||'Idle'):'Offline'}</Badge></Setting>
   <Setting title="Relay" desc="控制隧道与最终回退路径"><Badge tone={relayPath?'warn':connected?'blue':'neutral'}>{relayPath?'Active':connected?'Standby':'Offline'}</Badge></Setting>
   <Setting title="Native UDP" desc="进程级原生数据报资源"><span>{udp?udp+' associations':'空闲'}</span></Setting>
  </Card>
 </div>
 <div className="cards-4"><Metric label="下载速度" value={fmtBytes(traffic?.download_rate||0,true)} hint={fmtBytes(traffic?.download||0)+' 累计'} series={history.down}/><Metric label="上传速度" value={fmtBytes(traffic?.upload_rate||0,true)} hint={fmtBytes(traffic?.upload||0)+' 累计'} series={history.up}/><Metric label="往返延迟" value={connected?((status.latency||0)+' ms'):'—'} hint="Server RTT" series={history.latency}/><Metric label="活跃连接" value={traffic?.active??status.activeStreams??0} hint={(traffic?.total||0)+' 累计连接'} series={history.active}/></div>
 <div className="cards-3">
  <Card title="系统流量接管" eyebrow="WINDOWS" action={<Badge tone={status.divertRunning?'ok':'neutral'}>{status.divertRunning?'已接管':'未接管'}</Badge>}><div className="mini">{status.divertRunning?'按分流规则处理 TCP / UDP':'可启用 WinDivert 透明代理'}</div><div className="actions end top-gap"><Button onClick={()=>onGoto('proxy')}>管理透明代理</Button></div></Card>
  <Card title="网络出口" eyebrow="EXIT" action={<Badge tone={status.exitRunning?'ok':'neutral'}>{status.exitRunning?'正在共享':'未运行'}</Badge>}><div className="mini">{config.allowInternet?'允许 Internet':'Internet 已关闭'} · {config.allowPrivateNetwork?'允许私有网络':'私有网络已关闭'}</div><div className="actions end top-gap"><Button onClick={()=>onGoto('exitshare')}>管理出口</Button></div></Card>
  <Card title="本机代理" eyebrow="LOOPBACK" action={<Badge tone={status.socks5Running||status.httpRunning?'ok':'neutral'}>{status.socks5Running||status.httpRunning?'运行中':'已停止'}</Badge>}><div className="mini">SOCKS5 :{config.runtime?.socks5?.port||config.socks5?.port||1080} · HTTP :{config.runtime?.http?.port||config.http?.port||8080}{config.restartRequired?' · 存在待重启配置':''}</div><div className="actions end top-gap"><Button onClick={()=>onGoto('proxy')}>管理代理</Button></div></Card>
 </div>
 <Card title="身份状态" eyebrow="IDENTITY"><div className="cards-3 compact"><div><div className="mini">设备</div><strong>{status.deviceName||config.deviceName||'本设备'}</strong><div className="mono mini">{status.deviceId||'等待设备 ID'}</div></div><div><div className="mini">{connected?'身份':'上次批准身份'}</div><strong>{status.identityName||config.identityId||'尚未绑定'}</strong><div className="mini">{status.policyRevision?(connected?'策略 r':'上次策略 r')+status.policyRevision:'—'}</div></div><div><div className="mini">服务端授权模式</div><Badge tone={status.connected&&status.mode?'blue':'neutral'}>{status.connected&&status.mode?status.mode:'未授权 / 未连接'}</Badge></div></div></Card>
 <Card title="最近连接" eyebrow="LIVE TRAFFIC" action={<Button onClick={()=>onGoto('monitor')}>查看全部</Button>}><div className="recent-list">{arr(traffic?.connections).slice(0,3).map((x,i)=><div className="recent-row" key={x.id||i}><div className="app-mark">{String(x.process_name||x.process||'?').slice(0,1).toUpperCase()}</div><div className="grow"><strong>{x.process_name||x.process||'未知进程'}</strong><div className="mono mini">{(x.host||x.ip||'—')+(x.port?':'+x.port:'')}</div></div><div className="recent-rate"><b>{fmtBytes(x.download_rate||0,true)} ↓</b><span>{fmtBytes(x.upload_rate||0,true)} ↑</span></div><Badge tone={tone(x.state)}>{x.path||x.action||x.state||'—'}</Badge></div>)}{!arr(traffic?.connections).length&&<div className="empty compact-empty">暂无连接记录。</div>}</div></Card></>
}

function Devices({status,config,setv,save,dirty,onRefresh,toast}){
 const copy=async(text,label)=>{if(!text)return;if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',String(text));else await navigator.clipboard.writeText(String(text));toast('已复制'+label)};
 const capabilities=arr(status.approvedCapabilities),approval=String(status.approvalState||'').toLowerCase(),grantsActive=!!status.connected&&approval==='approved';
 const capMeta={
  'proxy.client':['代理客户端','使用 Server 授权的出口代理本机流量'],
  'proxy.exit':['网络出口','承载其他已授权设备的代理流量'],
  'rdp.controller':['RDP 控制端','连接 Server 授权的远程桌面设备'],
  'rdp.host':['RDP 主机','允许已授权控制端连接本机 RDP'],
  'rdp.public':['RDP 公网入口','使用 Server 公网 RDP ingress 能力']
 };
 return <><PageHead title="身份与设备" desc="设备身份由 Server 审批并控制可用能力；此处管理本机标识与连接身份。" actions={<Button onClick={onRefresh}>刷新状态</Button>}/>
 <div className="cards-2">
  <Card title="设备状态" eyebrow="DEVICE"><div className="identity-tile"><div className="identity-icon">▱</div><div className="grow"><div className="big small-big">{status.deviceName||config.deviceName||'RelayProxy Device'}</div><div className="mono muted">{status.deviceId||'等待 Server 分配设备 ID'}</div></div><Badge tone={tone(status.approvalState)}>{status.approvalState||'待连接'}</Badge></div>{status.deviceId&&<div className="actions end top-gap"><Button quiet onClick={()=>copy(status.deviceId,'设备 ID')}>复制设备 ID</Button></div>}<div className="divider"/><Setting title={grantsActive?'身份名称':'上次批准身份'} desc={grantsActive?'当前身份隔离范围':'缓存自上一次成功审批'}>{status.identityName||'—'}</Setting><Setting title={grantsActive?'策略版本':'上次策略版本'} desc="Server 下发的权限策略">{status.policyRevision||'—'}</Setting><Setting title={grantsActive?'当前模式':'上次批准模式'} desc="proxy.client / proxy.exit 聚合结果"><Badge tone={grantsActive?'blue':'neutral'}>{status.mode||'—'}</Badge></Setting></Card>
  <Card title={grantsActive?'当前设备能力':'设备能力'} eyebrow="SERVER GRANTS">{!grantsActive&&capabilities.length>0&&<div className="notice warn">当前没有已认证的 Relay 会话。下面仅显示上一次成功审批的能力，用于排障；在重新连接并完成审批前不视为有效授权。</div>}<div className="capability-list">{capabilities.map(cap=>{const meta=capMeta[cap]||[cap,'Server 批准的设备能力'];return <div className="capability-row" key={cap}><div className="grow"><b>{meta[0]}</b><div className="mini">{meta[1]}</div><div className="mono mini">{cap}</div></div><Badge tone={grantsActive?'ok':'neutral'}>{grantsActive?'允许':'上次批准'}</Badge></div>})}{grantsActive&&<div className="capability-row"><div className="grow"><b>消息接收</b><div className="mini">所有已审批 Agent 都能接收发往本设备的消息；渠道仍按身份隔离。</div><div className="mono mini">messages · approved-device</div></div><Badge tone="ok">启用</Badge></div>}{!capabilities.length&&!grantsActive&&<div className="empty compact-empty">等待设备连接并审批后显示 Server 批准能力。</div>}</div></Card>
 </div>
 <Card title="本机身份配置" eyebrow="CONFIGURATION"><div className="form-grid"><Field label="设备名称"><Input value={config.deviceName||''} onChange={e=>setv('deviceName',e.target.value)}/></Field><Field label="身份 ID" help="由 Server 创建的身份 ID。"><div className="row"><Input value={config.identityId||''} onChange={e=>setv('identityId',e.target.value)}/><Button onClick={()=>copy(config.identityId,' Identity ID')}>复制</Button></div></Field><Field label="Server 地址" className="full"><Input value={config.serverAddress||''} onChange={e=>setv('serverAddress',e.target.value)}/></Field></div></Card>
 <Card title="审批说明" eyebrow="SERVER AUTHORIZATION"><div className="notice">设备首次连接后需要在 Server 管理端审批。出口、RDP、消息渠道等能力均按身份与设备授权隔离，客户端不会展示未授权资源。</div></Card>
 <SaveBar dirty={dirty} label="保存身份配置" hint="设备名称、身份 ID 与 Server 地址均为启动参数，保存后需重启客户端生效。" onSave={()=>save({device:{name:config.deviceName,identityId:config.identityId},server:{address:config.serverAddress}})}/></>
}

function Connection({status,config,setv,save,dirty,onReload}){
 const p=config.p2p||EMPTY.p2p,[tlsRisk,setTlsRisk]=useState(false),rt=config.runtime||{};
 const runtimeServer=[rt.serverAddress||config.serverAddress,rt.quicPort||config.quicPort,rt.tcpPort||config.tcpPort].join(' · ');
 const savedServer=[config.serverAddress,config.quicPort,config.tcpPort].join(' · ');
 const serverPending=!!config.restartRequired&&(rt.serverAddress!==undefined&&(rt.serverAddress!==config.serverAddress||Number(rt.quicPort)!==Number(config.quicPort)||Number(rt.tcpPort)!==Number(config.tcpPort)||rt.transport!==config.transport||!!rt.tlsEnabled!==!!config.tlsEnabled||!!rt.insecureTls!==!!config.insecureTls));
 return <><PageHead title="连接与路径" desc="控制 QUIC/TCP 传输偏好、P2P 与公网直连策略，并观察实时路径。" actions={<Button onClick={onReload}>重载配置</Button>}/><div className="cards-3"><Card title="当前传输"><div className="big small-big">{status.connected?(status.transport||'—'):'offline'}</div><div className="mini">{status.connected?'Server RTT '+(status.latency||0)+' ms':'等待 Relay 重连'}</div></Card><Card title="Direct Path"><div className="big small-big">{status.connected?(status.directState||'idle'):'offline'}</div><div className="mini">{status.connected?(status.directPath||status.directEndpoint||'未建立'):'当前无有效直连会话'}</div></Card><Card title="P2P"><div className="big small-big">{status.connected?(status.p2pState||'idle'):'offline'}</div><div className="mini">{status.connected?(status.p2pPath||status.p2pCandidateSummary||'自动协商'):'当前无有效 P2P 会话'}</div></Card></div>
 {serverPending&&<div className="notice warn"><b>连接启动参数已保存，当前进程仍使用旧值。</b><br/>运行中：<span className="mono">{runtimeServer}</span> · {rt.transport||'auto'}<br/>重启后：<span className="mono">{savedServer}</span> · {config.transport||'auto'}</div>}
 <Card title="服务器与端口" eyebrow="SERVER"><div className="form-grid"><Field label="Relay Server"><Input value={config.serverAddress||''} onChange={e=>setv('serverAddress',e.target.value)}/></Field><Field label="TLS"><Select value={config.tlsEnabled===false?'off':'on'} onChange={e=>{const enabled=e.target.value==='on';setv('tlsEnabled',enabled);if(!enabled){setv('insecureTls',false);setv('transport','tcp_only')}}}><option value="on">启用 TLS</option><option value="off">关闭 TLS</option></Select></Field><Field label="QUIC / UDP 端口"><Input type="number" value={config.quicPort||35820} onChange={e=>setv('quicPort',Number(e.target.value))}/></Field><Field label="TCP / TLS 端口"><Input type="number" value={config.tcpPort||35821} onChange={e=>setv('tcpPort',Number(e.target.value))}/></Field></div><Setting title="允许自签名 / 不受信任证书" desc="仅用于可信开发环境；生产环境应保持关闭。"><Switch checked={!!config.insecureTls} disabled={config.tlsEnabled===false} onChange={v=>{if(v)setTlsRisk(true);else setv('insecureTls',false)}}/></Setting></Card>
 <Card title="传输策略" eyebrow="TRANSPORT"><div className="option-grid">{[['auto','自动','优先 QUIC，必要时回落 TCP'],['quic_only','仅 QUIC','固定使用 QUIC 传输'],['tcp_only','仅 TCP','固定使用 TLS/TCP']].map(x=><button key={x[0]} disabled={config.tlsEnabled===false&&x[0]!=='tcp_only'} className={cx('option',config.transport===x[0]&&'selected')} onClick={()=>setv('transport',x[0])}><div><strong>{x[1]}</strong><div className="mini">{x[2]}</div></div><span className="radio"/></button>)}</div></Card>
 <Card title="Direct / P2P" eyebrow="PATH POLICY"><Setting title="启用 P2P 直连" desc="首个请求可先走 Relay，后台建立可复用直连。"><Switch checked={p.enabled!==false} onChange={v=>setv('p2p.enabled',v)}/></Setting><Setting title="本机允许 UPnP 映射" desc="默认关闭。只有本机和 Server 同时允许才会向路由器申请 UDP 端口，保存后重启生效。"><Switch checked={p.upnpAllowed===true} onChange={v=>setv('p2p.upnpAllowed',v)}/></Setting><div className="notice">UPnP：{status.upnpState||'DISABLED'}{status.upnpAddress?' · '+status.upnpAddress:''}{status.upnpError?' · '+status.upnpError:''}</div><div className="form-grid top-gap"><Field label="路径模式"><Select value={p.mode||'auto'} onChange={e=>setv('p2p.mode',e.target.value)}><option value="auto">auto · 自动</option><option value="direct_only">direct_only · 仅直连（Public Direct / P2P）</option><option value="p2p_only">p2p_only · 仅 P2P</option><option value="relay_only">relay_only · 仅 Relay</option></Select></Field><Field label="打洞超时 (ms)"><Input type="number" value={p.punchTimeoutMs??1200} onChange={e=>setv('p2p.punchTimeoutMs',Number(e.target.value))}/></Field><Field label="Keepalive (s)"><Input type="number" value={p.keepaliveSec??10} onChange={e=>setv('p2p.keepaliveSec',Number(e.target.value))}/></Field><Field label="空闲超时 (s)"><Input type="number" value={p.idleTimeoutSec??120} onChange={e=>setv('p2p.idleTimeoutSec',Number(e.target.value))}/></Field><Field label="最大出口会话"><Input type="number" value={p.maxExitSessions??4} onChange={e=>setv('p2p.maxExitSessions',Number(e.target.value))}/></Field><Field label="失败回落" help={p.mode==='auto'?'仅自动模式使用；直连失败后回落 Relay。':'当前路径模式固定，不使用 Relay 回落开关。'}><Select disabled={p.mode!=='auto'} value={p.fallback===false?'off':'on'} onChange={e=>setv('p2p.fallback',e.target.value==='on')}><option value="on">允许回落 Relay</option><option value="off">不允许回落</option></Select></Field><Field label="手动公网地址（可选）" help="用于端口转发 / 云主机 DNS 等场景，Server 会主动验证。"><Input value={config.publicDirectAdvertise||''} onChange={e=>setv('publicDirectAdvertise',e.target.value)} placeholder="edge.example.com:35820"/></Field></div></Card>{(status.directError||status.p2pError)&&<div className="notice danger">{status.directError||status.p2pError}</div>}
 <SaveBar dirty={dirty} label="保存连接策略" hint="同时保存服务器与端口、传输策略、Direct / P2P 三组设置；这些均为启动参数，保存后需重启客户端生效。" onSave={()=>save({server:{address:config.serverAddress,quicPort:Number(config.quicPort),tcpPort:Number(config.tcpPort),tlsEnabled:config.tlsEnabled,insecureTls:!!config.insecureTls},transport:config.transport,p2p:config.p2p,direct:{public:{advertise:config.publicDirectAdvertise||''}}})}/><Modal open={tlsRisk} title="允许不受信任的 TLS 证书？" onClose={()=>setTlsRisk(false)} footer={<><Button onClick={()=>setTlsRisk(false)}>保持安全设置</Button><Button danger onClick={()=>{setv('insecureTls',true);setTlsRisk(false)}}>我了解风险，继续</Button></>}><div className="notice danger"><b>此设置会跳过服务端证书验证。</b><br/>仅在你完全信任的自建测试环境中使用。生产环境应使用受信任证书并保持关闭。</div></Modal></>
}


function CustomExitManager({config,save,onSelect,selected,toast}){
 const items=arr(config.customExits);
 const [draft,setDraft]=useState(null),[busy,setBusy]=useState(false);
 const refs=id=>[
  config.defaultExitId===id&&'默认出口',
  config.upstreamExitId===id&&'本机出口共享',
  config.routing?.dns_exit_id===id&&'DNS 专用出口',
  ...arr(config.routing?.rules).filter(r=>r.exit_id===id).map(r=>'分流规则：'+(r.name||'未命名'))
 ].filter(Boolean);
 const update=(key,val)=>setDraft(x=>({...x,[key]:val}));
 const persist=async next=>{setBusy(true);try{return await save({proxy:{customExits:next}})}finally{setBusy(false)}};
 const edit=item=>setDraft(item?{...item,password:'',clearPassword:false}:{id:'local:'+Date.now().toString(36)+'-'+Math.random().toString(36).slice(2),name:'',protocol:'socks5',address:'',enabled:true,username:'',password:'',clearPassword:false});
 const saveDraft=async()=>{
  if(!draft)return;
  if(!draft.name.trim()||!draft.address.trim()){toast('请输入名称和 host:port 地址','danger');return}
  const {hasPassword,clearPassword,...rest}=draft;
  const value={...rest,name:rest.name.trim(),address:rest.address.trim()};
  if(clearPassword)value.password='';
  else if(!value.password)delete value.password;
  const next=items.some(x=>x.id===value.id)?items.map(x=>x.id===value.id?value:x):[value,...items];
  if(await persist(next))setDraft(null);
 };
 const toggle=async item=>{const using=refs(item.id);if(item.enabled&&using.length){toast('该出口正在被引用：'+using.join('、'),'danger');return}await persist(items.map(x=>x.id===item.id?{...x,enabled:!x.enabled}:x))};
 const remove=async item=>{const using=refs(item.id);if(using.length){toast('请先解除引用：'+using.join('、'),'danger');return}if(window.confirm('确认删除 '+item.name+'？'))await persist(items.filter(x=>x.id!==item.id))};
 const test=async item=>{
  setBusy(true);
  try{
   const result=parseMutation(await call('goTestCustomExit',item.id));
   if(!result.ok)throw new Error(result.message||'连接测试失败');
   const tcpText=item.name+' TCP CONNECT 成功 · '+(result.latencyMs??'—')+' ms';
   if(result.udpSupported){
    if(result.udpOk)toast(tcpText+' · UDP 收发成功 '+(result.udpLatencyMs??'—')+' ms');
    else toast(tcpText+'；UDP 检测失败：'+(result.udpError||'超时或上游不支持 UDP'),'warn');
   }else toast(tcpText+' · 不支持 UDP');
  }catch(e){toast(item.name+' 测试失败：'+(e.message||String(e)),'danger')}
  finally{setBusy(false)}
 };
 return <>
  <Card title="自定义出口" eyebrow={items.length+' LOCAL EXITS'} action={<Button primary onClick={()=>edit(null)}>＋ 添加出口</Button>}>
   <div className="mini">本机 SOCKS5 / HTTP CONNECT，不依赖 Server 授权。可作为默认出口、分流规则目标和本机出口共享上游。点击「测试」会通过代理建立 TCP CONNECT；SOCKS5 还会经该代理向 Quad9 (9.9.9.9) 发送一次 example.com 的 UDP DNS 查询，分别显示结果。</div>
   {!items.length&&<div className="empty">尚未创建自定义出口。点击「添加出口」创建 SOCKS5 或 HTTP 代理。</div>}
   <div className="target-list top-gap">
    {items.map(item=><div className="target-row" key={item.id}>
     <div className="identity-icon small">↗</div>
     <div className="grow">
      <div className="row"><strong>{item.name}</strong><Badge tone={item.enabled?'ok':'neutral'}>{item.enabled?'已启用':'已停用'}</Badge>{config.upstreamExitId===item.id&&<Badge tone="blue">共享上游</Badge>}</div>
      <div className="mono mini">{item.protocol.toUpperCase()} · {item.address} · {item.protocol==='socks5'?'支持 UDP ASSOCIATE':'仅 TCP'}</div>
     </div>
     <Button quiet onClick={()=>onSelect(item.id)} disabled={!item.enabled||selected===item.id}>{selected===item.id?'默认出口':'设为默认'}</Button>
     <Button quiet disabled={!item.enabled||busy} onClick={()=>test(item)}>测试</Button>
     <Button quiet onClick={()=>edit(item)}>编辑</Button>
     <Switch checked={item.enabled} disabled={busy} label={'启停 '+item.name} onChange={()=>toggle(item)}/>
     <Button quiet danger disabled={busy} onClick={()=>remove(item)}>删除</Button>
    </div>)}
   </div>
  </Card>
  <Modal open={!!draft} title={items.some(x=>x.id===draft?.id)?'编辑自定义出口':'新增自定义出口'} onClose={()=>setDraft(null)} footer={<><Button onClick={()=>setDraft(null)}>取消</Button><Button primary disabled={busy} onClick={saveDraft}>{busy?'保存中…':'保存出口'}</Button></>}>
   {draft&&<div className="form-grid">
    <Field label="名称"><Input value={draft.name} onChange={e=>update('name',e.target.value)} placeholder="办公 SOCKS5"/></Field>
    <Field label="协议"><Select value={draft.protocol} onChange={e=>update('protocol',e.target.value)}><option value="socks5">SOCKS5</option><option value="http">HTTP CONNECT</option><option value="https">HTTPS CONNECT</option></Select></Field>
    <Field label="代理地址"><Input value={draft.address} onChange={e=>update('address',e.target.value)} placeholder="proxy.example.com:1080"/></Field>
    <Field label="用户名（可选）"><Input value={draft.username||''} onChange={e=>update('username',e.target.value)}/></Field>
    <Field label="密码（留空保持原值）"><Input type="password" autoComplete="new-password" disabled={!!draft.clearPassword} value={draft.password||''} onChange={e=>update('password',e.target.value)} placeholder={draft.hasPassword?'已设置密码':'可选'}/></Field>
    {draft.hasPassword&&<Field label="清除已保存密码"><Switch checked={!!draft.clearPassword} label="保存时清除原密码" onChange={v=>update('clearPassword',v)}/></Field>}
    <div className="notice">HTTP CONNECT 不支持 UDP；代理断开或不支持协议时，连接会失败，不会自动改为本机直连。</div>
   </div>}
  </Modal>
 </>;
}

function Exits({status,exits,selected,onSelect,onSpeed,onSpeedAll,config,save,toast}){
 const publicMeta=e=>{const p=e?.direct?.public,endpoints=arr(p?.endpoints),verified=endpoints.filter(x=>x.verified),first=verified[0]||endpoints[0];return {available:!!p?.available,transport:p?.transport||'',count:verified.length,endpoint:first?.dialAddress||first?.address||'',source:first?.source||''}};
 const staleSelected=!!selected&&!selected.startsWith('local:')&&!exits.some(e=>(e.deviceId||e.id)===selected),inventoryLive=!!status.connected;
 return <><PageHead title="出口选择" desc="管理 Server 授权出口与本机自定义 SOCKS5 / HTTP 出口。" actions={<><Button onClick={onSpeedAll}>全部测速</Button><Button primary onClick={()=>onSpeed(selected)}>单出口测速</Button></>}/>
 <div className={cx('notice',!inventoryLive&&'warn')}>{inventoryLive?'自动选择只在仅有一个授权在线出口时直接生效。原选择被撤销、删除或不再授权时会保留旧设备 ID，不会静默改选其他出口或 DIRECT。':'Relay 当前未连接。下面保留的是上一次成功审批得到的权威出口库存，用于排障和预选；在线状态需重连后重新确认。'}</div>
 <div className="exit-grid top-gap"><button className={cx('exit-card',!selected&&'selected')} onClick={()=>onSelect('')}><div className="flag">◎</div><div className="grow"><strong>自动选择</strong><div className="mini">仅一个授权在线出口时自动选择；不会绕过既定策略。</div></div><span className="radio"/></button>{staleSelected&&<button className="exit-card selected disabled" disabled><div className="flag">!</div><div className="grow"><div className="row"><strong>不可用旧选择</strong><Badge tone="danger">已撤销 / 已删除</Badge></div><div className="mono mini">{selected}</div><div className="mini">Server 当前授权库存中已不存在此出口。请手动选择新的出口或切回自动选择。</div></div><span className="radio"/></button>}{exits.map(e=>{const id=e.deviceId||e.id,m=publicMeta(e),offline=inventoryLive&&e.online===false;return <button key={id} className={cx('exit-card',selected===id&&'selected',offline&&'disabled')} onClick={()=>!offline&&onSelect(id)}><div className="flag">↗</div><div className="grow"><div className="row"><strong>{e.name||id}</strong><Badge tone={!inventoryLive?'neutral':offline?'neutral':'ok'}>{!inventoryLive?'上次库存':offline?'离线':'在线'}</Badge>{m.available&&<Badge tone="blue">Public Direct</Badge>}</div><div className="mini">{e.identityName||e.authorizationSource||id}</div>{m.available&&<div className="exit-path-meta"><span>{m.transport||'QUIC'}</span><span>{m.count?m.count+' 个已验证端点':'端点可用'}</span>{m.endpoint&&<span className="mono">{m.endpoint}</span>}</div>}</div><span className="radio"/></button>})}</div>
 {!exits.length&&<Card><div className="empty">当前没有已授权出口。请在 Server 管理端为此身份授权可用出口。</div></Card>}
 <CustomExitManager config={config} save={save} onSelect={onSelect} selected={selected} toast={toast}/>
 <Card title="当前路径" eyebrow="LIVE PATH"><div className="cards-3 compact"><div><div className="mini">选择出口</div><strong>{exitName(exits,selected)}</strong></div><div><div className="mini">Direct</div><strong>{status.connected?(status.directPath||status.directState||'—'):'offline'}</strong>{status.connected&&status.directEndpoint&&<div className="mono mini">{status.directEndpoint}</div>}</div><div><div className="mini">P2P</div><strong>{status.connected?(status.p2pPath||status.p2pState||'—'):'offline'}</strong>{status.connected&&status.p2pCandidateSummary&&<div className="mini">{status.p2pCandidateSummary}</div>}</div></div></Card></>
}

function ProxyPage({status,config,setv,save,dirty,onService,onRefreshCapabilities,toast}){
 const s=config.socks5||EMPTY.socks5,h=config.http||EMPTY.http,rs=config.runtime?.socks5||s,rh=config.runtime?.http||h,caps=config.networkCapabilities||{},[service,setService]=useState(null),[repairing,setRepairing]=useState(false);
 const windowsCaptureAvailable=hasBridge('goGetNetworkServiceStatus');
 const refreshService=useCallback(async()=>{if(!hasBridge('goGetNetworkServiceStatus'))return null;try{const x=await callJSON('goGetNetworkServiceStatus',{});setService(x||{});return x}catch{return null}},[]);
 useEffect(()=>{onRefreshCapabilities?.();refreshService()},[onRefreshCapabilities,refreshService]);
 const interceptReady=!!(caps.tcp&&caps.udp&&caps.ipv6&&caps.process_identity&&caps.original_destination&&caps.reply_injection&&caps.loop_bypass&&!caps.unavailable_reason);
 const canToggleIntercept=interceptReady||config.networkMode==='divert';
 const socksAddr=(s.listen||'127.0.0.1')+':'+(s.port||1080),httpAddr=(h.listen||'127.0.0.1')+':'+(h.port||8080),runtimeSocksAddr=(rs.listen||'127.0.0.1')+':'+(rs.port||1080),runtimeHttpAddr=(rh.listen||'127.0.0.1')+':'+(rh.port||8080);
 const socksURI='socks5h://'+socksAddr,httpURI='http://'+httpAddr,runtimeSocksURI='socks5h://'+runtimeSocksAddr,runtimeHttpURI='http://'+runtimeHttpAddr,socksPending=socksAddr!==runtimeSocksAddr||!!s.enabled!==!!rs.enabled,httpPending=httpAddr!==runtimeHttpAddr||!!h.enabled!==!!rh.enabled;
 const exposed=v=>{const x=String(v||'').trim().toLowerCase();return x==='0.0.0.0'||x==='::'||x==='[::]'||x==='*'};
 const repair=async()=>{setRepairing(true);try{const r=parseMutation(await call('goRepairNetworkService'));if(!r.ok&&r.message)throw new Error(r.message);await Promise.all([refreshService(),onRefreshCapabilities?.()]);toast(r.message||'Network Service 修复完成')}catch(e){toast(e.message,'danger')}finally{setRepairing(false)}};
 const copy=async(value,label)=>{if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',value);else await navigator.clipboard.writeText(value);toast('已复制 '+label)};
 return <><PageHead title="本机代理" desc={windowsCaptureAvailable?"管理 SOCKS5、HTTP Proxy 与 Windows 透明代理接管。":"管理 SOCKS5 与 HTTP Proxy 的本地监听及网络访问。"} actions={<Button onClick={onService}>高级维护</Button>}/>
 <div className="cards-2">
  <Card title="SOCKS5" eyebrow="LOCAL PROXY" action={<Switch checked={s.enabled!==false} onChange={v=>setv('socks5.enabled',v)}/>}>
   <div className="row"><Badge tone={status.socks5Running?'ok':'neutral'}>{status.socks5Running?'运行中':'未运行'}</Badge><span className="mini">TCP + UDP ASSOCIATE · socks5h 由出口解析域名</span></div>
   <div className="form-grid top-gap"><Field label="监听地址"><Input value={s.listen||''} onChange={e=>setv('socks5.listen',e.target.value)}/></Field><Field label="端口"><Input type="number" value={s.port||1080} onChange={e=>setv('socks5.port',Number(e.target.value))}/></Field></div>
   {exposed(s.listen)&&<div className="notice warn top-gap">SOCKS5 正绑定所有网卡，局域网设备可能访问此代理。请确认系统防火墙与网络环境可信。</div>}
   <div className="setting top-gap"><div className="grow"><b>{status.socks5Running?'当前运行地址':'保存后地址'}</b><div className="mono mini">{status.socks5Running?runtimeSocksURI:socksURI}</div></div><Button quiet onClick={()=>copy(status.socks5Running?runtimeSocksURI:socksURI,'SOCKS5 地址')}>复制</Button></div>{socksPending&&<div className="notice warn top-gap">保存配置为 <span className="mono">{socksURI}</span>，当前进程仍使用 <span className="mono">{runtimeSocksURI}</span>；重启后切换。</div>}
  </Card>
  <Card title="HTTP / HTTPS CONNECT" eyebrow="LOCAL PROXY" action={<Switch checked={h.enabled!==false} onChange={v=>setv('http.enabled',v)}/>}>
   <div className="row"><Badge tone={status.httpRunning?'ok':'neutral'}>{status.httpRunning?'运行中':'未运行'}</Badge><span className="mini">普通 HTTP 代理与 HTTPS CONNECT 隧道</span></div>
   <div className="form-grid top-gap"><Field label="监听地址"><Input value={h.listen||''} onChange={e=>setv('http.listen',e.target.value)}/></Field><Field label="端口"><Input type="number" value={h.port||8080} onChange={e=>setv('http.port',Number(e.target.value))}/></Field></div>
   {exposed(h.listen)&&<div className="notice warn top-gap">HTTP Proxy 正绑定所有网卡，局域网设备可能访问此代理。请确认 Windows Firewall 与网络环境可信。</div>}
   <div className="setting top-gap"><div className="grow"><b>{status.httpRunning?'当前运行地址':'保存后地址'}</b><div className="mono mini">{status.httpRunning?runtimeHttpURI:httpURI}</div></div><Button quiet onClick={()=>copy(status.httpRunning?runtimeHttpURI:httpURI,'HTTP Proxy 地址')}>复制</Button></div>{httpPending&&<div className="notice warn top-gap">保存配置为 <span className="mono">{httpURI}</span>，当前进程仍使用 <span className="mono">{runtimeHttpURI}</span>；重启后切换。</div>}
  </Card>
 </div>
 {windowsCaptureAvailable&&<Card title="Windows 系统透明代理" eyebrow="WINDOWS NETWORK CAPTURE" action={<Badge tone={service?.ready?'ok':service?.installed?'warn':'neutral'}>{service?.ready?'运行条件已满足':service?.installed?'服务需修复':'服务未安装'}</Badge>}>
  <Setting title="接管系统流量" desc="使用 WinDivert 按进程与分流规则处理本机 TCP / UDP。"><Switch checked={config.networkMode==='divert'} disabled={!canToggleIntercept} onChange={v=>{setv('networkMode',v?'divert':'');setv('network.mode',v?'divert':'')}}/></Setting>
  {!interceptReady&&<div className="notice warn top-gap"><b>当前系统透明代理尚不可用。</b> {caps.unavailable_reason||'此构建缺少完整的 TCP / UDP / IPv6 / 进程识别或回注能力。'}{config.networkMode==='divert'?' 你仍可关闭已保存的透明代理模式。':' 请修复 Network Service，或继续使用 SOCKS5 / HTTP Proxy。'}</div>}
  <div className="cards-3 compact top-gap">
   <div><div className="mini">Network Service</div><strong>{service?.ready?'就绪':service?.running?'运行中':service?.installed?'已安装':'未安装'}</strong><div className="mini">{service?.autoStartKnown?(service?.autoStart?'Automatic':'非自动启动'):'启动类型未知'}</div></div>
   <div><div className="mini">抓包后端</div><strong>{status.divertDiagnostics?.captureBackend||'WinDivert'}</strong><div className="mini">{status.divertDiagnostics?.captureState||status.divertStage||'idle'}</div></div>
   <div><div className="mini">数据面</div><strong>{Number(status.divertDiagnostics?.captured||0)} / {Number(status.divertDiagnostics?.classified||0)}</strong><div className="mini">抓包 / 分类 · PROXY {Number(status.divertDiagnostics?.proxy||0)}</div></div>
  </div>
  {(status.divertError||service?.message)&&<div className={cx('notice','top-gap',status.divertError&&'danger')}>{status.divertError||service?.message}</div>}
  <Field label="排除进程" help="这些进程的流量不经透明代理；可用换行、逗号或分号分隔，如 steam.exe, Teams.exe"><Textarea rows="5" value={ruleListText(config.network?.exclude_processes)} onChange={e=>setv('network.exclude_processes',e.target.value)}/></Field>
  <div className="actions top-gap"><Button disabled={repairing} onClick={repair}>{repairing?'修复中…':'安装 / 修复服务'}</Button><Button onClick={onService}>高级维护 / 卸载</Button></div>
 </Card>}
 <SaveBar dirty={dirty} label="保存代理设置" hint="同时保存 SOCKS5、HTTP Proxy 与系统透明代理设置；监听开关、地址、端口与透明代理开关需重启生效，排除进程立即生效。" onSave={()=>save({proxy:{socks5Enabled:s.enabled,socks5Listen:s.listen,socks5Port:Number(s.port),httpEnabled:h.enabled,httpListen:h.listen,httpPort:Number(h.port),defaultExitId:config.defaultExitId||''},network:{mode:config.networkMode||'',excludeProcesses:splitRuleList(config.network?.exclude_processes)}})}/></>
}

function ExitShare({status,config,setv,save,dirty}){
 const u=config.exitUpstream||EMPTY.exitUpstream,[loopbackRisk,setLoopbackRisk]=useState(false);
 const customs=arr(config.customExits),chosen=customs.find(x=>x.id===config.upstreamExitId);
 const legacy=!config.upstreamExitId&&u.mode&&u.mode!=='direct';
 const selected=config.upstreamExitId||(legacy?'__legacy__':'__direct__');
 return <><PageHead title="本机出口共享" desc="允许 Server 将授权流量通过此设备访问 Internet 或指定网络。"/>
 <Card title="出口能力" eyebrow="EXIT NODE" action={<Switch checked={config.exitEnabled!==false} onChange={v=>setv('exitEnabled',v)}/>}><Setting title="当前运行状态" desc="Agent 出口处理器"><Badge tone={status.exitRunning?'ok':'neutral'}>{status.exitRunning?'正在提供出口':'未运行'}</Badge></Setting><Setting title="允许访问 Internet" desc="允许作为公网出口"><Switch checked={!!config.allowInternet} onChange={v=>setv('allowInternet',v)}/></Setting><Setting title="允许访问私有网络" desc="访问 RFC1918 / 内网地址"><Switch checked={!!config.allowPrivateNetwork} onChange={v=>setv('allowPrivateNetwork',v)}/></Setting><Setting title="允许访问 Loopback" desc="允许访问 127.0.0.0/8 / ::1 上的本机服务；高风险，默认关闭。"><Switch checked={!!config.allowLoopback} onChange={v=>v?setLoopbackRisk(true):setv('allowLoopback',false)}/></Setting></Card>
 <div className="cards-2"><Card title="共享上游出口" eyebrow="UPSTREAM">
  <Field label="上游连接方式">
   <Select value={selected} onChange={e=>{
    const id=e.target.value;
    setv('upstreamExitId',id.startsWith('local:')?id:'');
    if(id==='__direct__')setv('exitUpstream.mode','direct');
   }}>
    <option value="__direct__">本机直连</option>
    {legacy&&<option value="__legacy__">旧配置：{u.mode.toUpperCase()} · {u.address||'地址未知'}（保留原行为）</option>}
    {customs.map(x=><option key={x.id} value={x.id} disabled={!x.enabled&&x.id!==config.upstreamExitId}>{x.name} · {x.protocol.toUpperCase()} · {x.address}{!x.enabled?'（已停用）':''}</option>)}
   </Select>
  </Field>
  {chosen&&<div className="setting top-gap"><div className="grow"><b>{chosen.name}</b><div className="mini">{chosen.address} · {chosen.protocol.toUpperCase()}</div></div><Badge tone={chosen.enabled?'ok':'danger'}>{chosen.enabled?'可用':'不可用'}</Badge></div>}
  {chosen&&chosen.protocol!=='socks5'&&<div className="notice warn top-gap">HTTP(S) CONNECT 仅支持 TCP。共享出口的 UDP 请求会失败，不会回退本机直连。</div>}
  {config.upstreamExitId&&!chosen&&<div className="notice danger top-gap">所选上游出口已丢失，保存将被阻止。请重新选择或切换到本机直连。</div>}
  {legacy&&<div className="notice warn top-gap">当前仍使用旧版上游 {u.mode}。请创建自定义出口并在此引用；切换上游前保留原有工作方式。</div>}
  {!customs.length&&<div className="notice top-gap">暂无自定义出口。请前往「出口选择」创建。</div>}
 </Card><Card title="出口访问控制" eyebrow="ACCESS POLICY"><Field label="策略模式"><Select value={config.accessMode||''} onChange={e=>setv('accessMode',e.target.value)}><option value="">关闭</option><option value="allow">仅允许列表</option><option value="deny">拒绝列表</option></Select></Field><Field label="域名规则" help="可用换行、逗号或分号分隔，支持通配符，如 *.example.com"><Textarea rows="4" value={ruleListText(config.accessDomains)} onChange={e=>setv('accessDomains',e.target.value)}/></Field><Field label="CIDR / IP" help="可用换行、逗号或分号分隔，如 10.0.0.0/8, 192.168.1.1"><Textarea rows="4" value={ruleListText(config.accessCidrs)} onChange={e=>setv('accessCidrs',e.target.value)}/></Field></Card></div>
 <SaveBar dirty={dirty} label="保存出口共享设置" hint="同时保存出口能力、上游代理与访问控制；出口相关设置均为启动参数，保存后需重启客户端生效。" onSave={()=>save({exit:{enabled:config.exitEnabled,allowInternet:config.allowInternet,allowPrivateNetwork:config.allowPrivateNetwork,allowLoopback:config.allowLoopback,upstreamExitId:config.upstreamExitId||'',upstream:config.upstreamExitId?undefined:u,access:{mode:config.accessMode||'',domains:splitRuleList(config.accessDomains),cidrs:splitRuleList(config.accessCidrs)}}})}/>
 <Modal open={loopbackRisk} title="允许出口流量访问本机 Loopback？" onClose={()=>setLoopbackRisk(false)} footer={<><Button onClick={()=>setLoopbackRisk(false)}>保持关闭</Button><Button danger onClick={()=>{setv('allowLoopback',true);setLoopbackRisk(false)}}>我了解风险，允许访问</Button></>}><div className="notice danger"><b>启用后，经 Server 授权到达此出口的流量可以访问本机 Loopback 服务。</b><br/>这可能包含只监听 127.0.0.1 / ::1 的管理接口、开发服务或其他原本不面向网络开放的进程。仅在你明确需要且访问策略足够严格时开启。</div></Modal></>
}

function RuleEditor({value,exits,onChange}){
 const set=(k,v)=>onChange({...value,[k]:v});
 const setAction=action=>onChange({...value,action,exit_id:action==='PROXY'?(value.exit_id||''):'',handle_direct:action==='DIRECT'?!!value.handle_direct:false});
 const sep='可用换行、逗号或分号分隔多个条目';
 return <div className="form-grid"><Field label="规则名称"><Input value={value.name||''} onChange={e=>set('name',e.target.value)}/></Field><Field label="动作"><Select value={value.action||'PROXY'} onChange={e=>setAction(e.target.value)}><option value="PROXY">PROXY</option><option value="DIRECT">DIRECT</option><option value="REJECT">REJECT</option></Select></Field><Field label="指定出口"><Select value={value.exit_id||''} disabled={value.action!=='PROXY'} onChange={e=>set('exit_id',e.target.value)}><option value="">跟随默认出口</option>{exits.map(x=><option key={x.deviceId||x.id} value={x.deviceId||x.id} disabled={x.online===false}>{x.name||x.deviceId}{x.online===false?' · 离线':''}</option>)}</Select></Field><Field label="协议"><Select value={protocolChoice(value.protocols)} onChange={e=>set('protocols',protocolsFromChoice(e.target.value))}>{PROTOCOL_CHOICES.map(([v,l])=><option key={v} value={v}>{l}</option>)}</Select></Field><Field label="进程" help={sep+'，如 chrome.exe, steam.exe'} className="full"><Textarea rows="3" value={ruleListText(value.processes)} onChange={e=>set('processes',e.target.value)}/></Field><Field label="域名 / IP / CIDR" help={sep+'，支持通配符，如 *.example.com; 10.0.0.0/8'} className="full"><Textarea rows="3" value={ruleListText(value.targets)} onChange={e=>set('targets',e.target.value)}/></Field><Field label="端口" help={sep+'，留空表示任意端口'} className="full"><Textarea rows="2" value={ruleListText(value.ports)} onChange={e=>set('ports',e.target.value)}/></Field><label className="check-line"><input type="checkbox" checked={!!value.datagram_required} disabled={!!value.handle_direct} onChange={e=>set('datagram_required',e.target.checked)}/><span><strong>要求原生 UDP</strong><small>{value.handle_direct?'托管 DIRECT 与 Relay Datagram 互斥。':'仅在支持 datagram 的代理路径上使用。'}</small></span></label><label className="check-line"><input type="checkbox" checked={!!value.handle_direct} disabled={value.action!=='DIRECT'||!!value.datagram_required} onChange={e=>set('handle_direct',e.target.checked)}/><span><strong>RelayProxy 托管 DIRECT</strong><small>{value.datagram_required?'要求原生 UDP 时不能同时托管 DIRECT。':'由 RelayProxy 建立本地直连并纳入连接监控。'}</small></span></label></div>
}

function RoutingPage({config,exits,setv,save,dirty,onDiscard,onGoto}){
 exits=[...exits,...arr(config.customExits).map(x=>({id:x.id,name:x.name,online:x.enabled}))];
 const r=config.routing||EMPTY.routing,[editing,setEditing]=useState(null),rules=arr(r.rules);
 const commit=x=>setv('routing.rules',x);
 const move=(i,d)=>{const j=i+d;if(j<0||j>=rules.length)return;commit(moveRule(rules,i,j))};
 const toggle=(i,on)=>commit(rules.map((x,n)=>n===i?{...x,enabled:on}:x));
 // Drag-to-reorder: the handle arms the drag so text selection and buttons inside the row keep working.
 const armed=useRef(false),dragFrom=useRef(null),[dragging,setDragging]=useState(null),[dropAt,setDropAt]=useState(null);
 const endDrag=()=>{armed.current=false;dragFrom.current=null;setDragging(null);setDropAt(null)};
 const dragProps=i=>({draggable:true,
  onDragStart:e=>{if(!armed.current){e.preventDefault();return}dragFrom.current=i;setDragging(i);e.dataTransfer.effectAllowed='move';e.dataTransfer.setData('text/plain',String(i))},
  onDragOver:e=>{if(dragFrom.current===null)return;e.preventDefault();e.dataTransfer.dropEffect='move';const b=e.currentTarget.getBoundingClientRect(),after=e.clientY>b.top+b.height/2;if(!dropAt||dropAt.i!==i||dropAt.after!==after)setDropAt({i,after})},
  onDragLeave:e=>{if(!e.currentTarget.contains(e.relatedTarget))setDropAt(d=>d&&d.i===i?null:d)},
  onDrop:e=>{e.preventDefault();const from=dragFrom.current;if(from!==null){const b=e.currentTarget.getBoundingClientRect();commit(moveRule(rules,from,dropTargetIndex(from,i,e.clientY>b.top+b.height/2)))}endDrag()},
  onDragEnd:endDrag,onMouseUp:()=>{armed.current=false}});
 const edit=i=>setEditing({index:i,rule:JSON.parse(JSON.stringify(i>=0?rules[i]:{name:'新规则',enabled:true,action:'PROXY',exit_id:'',datagram_required:false,handle_direct:false,processes:[],targets:[],ports:[],protocols:['tcp','udp']}))});
 const saveEditor=()=>{const n=[...rules],rule=normalizeRuleLists(editing.rule);if(editing.index<0)n.push(rule);else n[editing.index]=rule;commit(n);setEditing(null)};
 return <><PageHead title="分流规则" desc="按进程、域名/IP、端口和协议从上到下匹配，第一条命中的规则生效。DNS 解析与防泄漏已归入独立页面。" actions={<><Button onClick={()=>onGoto('dns')}>DNS 设置 ›</Button><Button primary onClick={()=>edit(-1)}>＋ 新建规则</Button></>}/>
 <Card title="全局路由策略" eyebrow="ROUTING MODE"><div className="form-grid"><Field label="路由模式"><Select value={r.mode||'global_proxy'} onChange={e=>setv('routing.mode',e.target.value)}><option value="global_proxy">全局代理</option><option value="rule">规则模式</option><option value="direct">全局直连</option></Select></Field><Field label="规则未命中时"><Select value={r.default_action||'PROXY'} onChange={e=>setv('routing.default_action',e.target.value)}><option value="PROXY">PROXY</option><option value="DIRECT">DIRECT</option><option value="REJECT">REJECT</option></Select></Field></div></Card>
 <Card title="规则列表" eyebrow={rules.length+' RULES'}><div className="rule-table"><div className="rule-row rule-head"><span>#</span><span>规则</span><span>匹配条件</span><span>动作</span><span>启用</span><span>操作</span></div>{rules.map((x,i)=><div className={cx('rule-row',!x.enabled&&'disabled',dragging===i&&'dragging',dropAt&&dropAt.i===i&&dragging!==i&&(dropAt.after?'drop-after':'drop-before'))} key={(x.name||'rule')+'-'+i} {...dragProps(i)}><span className="drag" role="button" tabIndex={-1} aria-label={'拖动调整顺序：'+(x.name||'规则 '+(i+1))} title="拖动调整顺序" onMouseDown={()=>{armed.current=true}}>⋮⋮</span><div><div className="row"><strong>{x.name||'规则 '+(i+1)}</strong></div><div className="mini">优先级 {i+1}</div></div><div className="chips">{arr(x.processes).slice(0,2).map(v=><span className="chip" key={'p'+v}>{v}</span>)}{arr(x.targets).slice(0,2).map(v=><span className="chip" key={'t'+v}>{v}</span>)}{arr(x.ports).slice(0,1).map(v=><span className="chip" key={'o'+v}>{v}</span>)}{!arr(x.processes).length&&!arr(x.targets).length&&!arr(x.ports).length&&<span className="mini">任意流量</span>}</div><div><Badge tone={x.action==='REJECT'?'danger':x.action==='DIRECT'?'blue':'ok'}>{x.action||'PROXY'}</Badge>{x.exit_id&&<div className="mini">{exitName(exits,x.exit_id)}</div>}</div><div><Switch checked={!!x.enabled} label={(x.enabled?'停用':'启用')+' '+(x.name||'规则 '+(i+1))} onChange={v=>toggle(i,v)}/></div><div className="rule-ops"><Button quiet disabled={i===0} onClick={()=>move(i,-1)}>↑</Button><Button quiet disabled={i===rules.length-1} onClick={()=>move(i,1)}>↓</Button><Button quiet onClick={()=>edit(i)}>编辑</Button><Button quiet danger onClick={()=>window.confirm('删除这条分流规则？')&&commit(rules.filter((_,n)=>n!==i))}>删除</Button></div></div>)}{!rules.length&&<div className="empty">尚无规则。切换到规则模式前请先添加规则。</div>}</div></Card>
 <SaveBar dirty={dirty} label="保存分流规则" hint="保存全局路由策略与分流规则；DNS 选项请在 DNS 与防泄漏页修改。" onSave={()=>save({routing:routingPagePatch(r)})}><Button disabled={!dirty} onClick={onDiscard}>放弃修改</Button></SaveBar>
 <Modal open={!!editing} title={editing&&editing.index>=0?'编辑分流规则':'新建分流规则'} wide onClose={()=>setEditing(null)} footer={<><Button onClick={()=>setEditing(null)}>取消</Button><Button primary onClick={saveEditor}>保存规则</Button></>}>{editing&&<RuleEditor value={editing.rule} exits={exits} onChange={rule=>setEditing({...editing,rule})}/>}</Modal></>
}

// DNS is a device-wide network policy, not an application-specific
// classification rule. Keep the two Proxifier-style name resolution controls
// independent from the advanced transparent-interception protections.
function DNSPage({status,config,exits,setv,save,dirty,onGoto,onDiscard}){
 const r=config.routing||EMPTY.routing;
 const inventory=[...arr(exits).map(x=>({id:x.deviceId||x.id,name:x.name||x.deviceName||x.deviceId||x.id})),...arr(config.customExits).filter(x=>x.enabled).map(x=>({id:x.id,name:x.name||x.id}))];
 const change=next=>setv('routing',next);
 const confirmDisable=()=>!r.fake_ip_enabled||window.confirm('自动检测或本机解析会关闭 FakeIP、DoH 阻断和 TXT/SRV 代理查询。确定要切换为可能使用本机 DNS 的模式吗？');
 const proxy=(value)=>{if(!value&&!confirmDisable())return;change(setProxyDNS(r,value))};
 const automatic=(value)=>{if(value&&!confirmDisable())return;change(setAutoDNS(r,value))};
 const protection=status.dnsProtection||{};
 const divertRunning=status.divertRunning===true;
 const captureMode=protection.capture||'unavailable';
 const captureObserved=divertRunning&&captureMode!=='unavailable';
 const strictCapture=captureObserved&&captureMode==='nfqueue-no-bypass';
 const fakeConfigured=!!r.fake_ip_enabled;
 const fakeApplied=divertRunning&&protection.fakeIpEnabled===true;
 return <><PageHead title="DNS 与防泄漏" desc="独立管理域名解析位置、自动检测、FakeIP DNS 接管与已知加密 DNS 绕过防护。" actions={<Button onClick={()=>onGoto('routing')}>分流规则 ›</Button>}/>
 <Card title="主机名解析" eyebrow="NAME RESOLUTION">
  <Setting title="自动检测 DNS 状态" desc="开启时自动切换到本机优先模式：先尝试系统 DNS，本机查询失败再使用代理解析。此模式可能产生本机 DNS 查询，不适用于严格防泄漏。"><Switch checked={!!r.auto_detect_dns} label="自动检测 DNS 状态" onChange={automatic}/></Setting>
  <Setting title="通过代理解析主机名" desc="手动模式：开启时将原始域名交给代理解析；关闭时先使用系统 DNS。修改此项会关闭自动检测；启用代理解析优先于自动检测。"><Switch checked={(r.dns_mode||'proxy')==='proxy'} label="通过代理解析主机名" onChange={proxy}/></Setting>
  <div className="notice top-gap">{r.auto_detect_dns?((r.dns_mode||'proxy')==='proxy'?'自动检测已保存，但当前被手动代理解析覆盖；重新关闭再开启自动检测可进入本机优先模式。':'自动检测生效：系统 DNS 优先，失败后交给代理；可能产生本机 DNS 查询。'):(r.dns_mode||'proxy')==='proxy'?'手动代理解析：代理连接的原始域名始终交给远端。':'手动本机解析：系统 DNS 查询可能离开代理。'}</div>
  <div className="mini top-gap">只有原始连接包含域名时，这两个选项才影响代理侧解析。已经被操作系统解析为 IP 的透明连接不会被自动还原为域名；需要 FakeIP 接管系统 DNS 时，请启用下方独立选项。</div>
 </Card>
 <Card title="DNS 接管与加密解析器防护" eyebrow="DNS INTERCEPTION">
  <Setting title="FakeIP 接管 DNS（实验性）" desc="在透明代理内处理可捕获的 A/AAAA 查询并建立 FakeIP 映射。启用时自动关闭 DNS 自动检测，切换为代理解析；仅保存选项不能代替启用透明代理。"><Switch checked={!!r.fake_ip_enabled} label="FakeIP 接管 DNS" onChange={v=>change(enableFakeIP(r,v))}/></Setting>
  <Setting title="拦截已知 DoH 解析器" desc="开启时自动满足 FakeIP 与代理解析前置条件。仅阻断已知解析器域名上的 HTTPS/443 连接，不能识别全部私有 DoH、ECH 或 IP 直连。"><Switch checked={!!r.block_doh_endpoints} label="拦截已知 DoH 解析器" onChange={v=>change(setDoHBlocking(r,v))}/></Setting>
  <Setting title="通过代理补充查询 TXT / SRV" desc="开启时自动满足 FakeIP 与代理解析前置条件。使用选定出口经 TLS 访问 Quad9；查询域名会发送给第三方，失败时不回落明文 DNS。"><Switch checked={!!r.forward_other_dns} label="通过代理补充查询 TXT / SRV" onChange={v=>change(setOtherDNSForwarding(r,v))}/></Setting>
  <div className="form-grid top-gap">
   <Field label="加密 DNS 查询出口" help="独立指定 TXT / SRV 查询使用的代理出口；留空跟随默认出口。"><Select value={r.dns_exit_id||''} onChange={e=>setv('routing.dns_exit_id',e.target.value)}><option value="">跟随当前默认出口</option>{inventory.filter(x=>x.id).map(x=><option value={x.id} key={x.id}>{x.name}</option>)}{r.dns_exit_id&&!inventory.some(x=>x.id===r.dns_exit_id)&&<option value={r.dns_exit_id}>{r.dns_exit_id}（不可用）</option>}</Select></Field>
   <Field label="额外阻断的 DoH IP / CIDR" className="full" help="一行一个公网 IP 或窄 CIDR；仅 DoH 阻断开启时生效，拦截对应地址的全部 TCP/UDP 443，可能影响同 IP 上的其他服务。"><Textarea rows="3" placeholder={'9.9.9.9\n1.1.1.1'} value={arr(r.doh_blocked_ips).join('\n')} onChange={e=>setv('routing.doh_blocked_ips',e.target.value.split(/[\n,;]+/).map(x=>x.trim()).filter(Boolean))}/></Field>
  </div>
  <div className="notice warn top-gap">当前开关只代表配置意图。FakeIP、DoH 阻断依赖已运行的透明代理拦截能力；环回 DNS、应用内私有 DoH 与驱动/Agent 退出后的系统防泄漏仍需实机验证。HTTPS/SVCB 暂不经 TXT/SRV 查询通道转发。</div>
  <div className="actions end top-gap"><Button onClick={()=>onGoto('proxy')}>透明代理设置 ›</Button><Button onClick={()=>onGoto('diagnostics')}>诊断与日志 ›</Button></div>
 </Card>
 <Card title="实际运行状态" eyebrow="ACTIVE DNS PROTECTION">
  <Setting title="透明代理运行状态" desc={'DNS 捕获报告：'+captureMode+'（仅反映 Agent / 内核上报，不能证明所有 DNS 均已接管）'}><Badge tone={divertRunning?'blue':'warn'}>{divertRunning?'透明代理运行中':'透明代理未运行'}</Badge></Setting>
  <Setting title="FakeIP 配置与捕获证据" desc={fakeConfigured?'已保存 FakeIP 策略；'+(captureObserved?'观察到捕获报告，但环回 DNS、私有 DoH 和防火墙旁路仍需验证。':'目前未观察到有效的 DNS 捕获报告。'):'FakeIP 策略未启用'}><Badge tone={strictCapture&&fakeApplied?'blue':fakeConfigured?'warn':'neutral'}>{!fakeConfigured?'未启用':!fakeApplied?'已配置 · 未确认应用':strictCapture?'内核捕获报告可用 · 仍需实测':'策略已应用 · DNS 接管未独立验证'}</Badge></Setting>
  <Setting title="独立 DNS Kill Switch" desc={protection.detail||'当前没有足够证据证明系统级防泄漏'}><Badge tone={protection.independentGuard==='rules-present'?'blue':'warn'}>{protection.independentGuard||'未验证'}</Badge></Setting>
 </Card>
 <SaveBar dirty={dirty} label="保存 DNS 设置" hint="解析模式与路由 DNS 策略支持热更新；如果透明代理本身未启用，请先完成对应安装与启动。" onSave={()=>save({routing:dnsPagePatch(r)})}><Button disabled={!dirty} onClick={onDiscard}>放弃修改</Button></SaveBar>
 </>;
}

function RDPPage({status,config,setv,save,dirty,targets,refreshTargets,refreshStatus,toast}){
 const[busy,setBusy]=useState('');
 const desiredEnabled=config.rdp?.enabled!==false,runtimeEnabled=config.runtime?.rdp?.enabled!==false;
 const controllerApproved=arr(status.approvedCapabilities).includes('rdp.controller');
 const saveRDP=async()=>{await save({rdp:{enabled:desiredEnabled}})};
 const restart=async()=>{try{const r=parseMutation(await call('goRestart'));if(!r.ok)throw new Error(r.message||'重启客户端失败')}catch(e){toast(e.message,'danger')}};
 const connect=async id=>{setBusy(id);try{const r=parseMutation(await call('goConnectRDP',id,true));if(!r.ok)throw new Error(r.message||'RDP 连接失败');toast('RDP 本地入口已建立');await refreshStatus()}catch(e){toast(e.message,'danger')}finally{setBusy('')}};
 const disconnect=async()=>{try{const r=parseMutation(await call('goDisconnectRDP'));if(!r.ok)throw new Error(r.message||'RDP 断开失败');await refreshStatus();toast('RDP 已断开')}catch(e){toast(e.message,'danger')}};
 const copy=async text=>{if(!text)return;if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',String(text));else await navigator.clipboard.writeText(String(text));toast('已复制 RDP 本地入口')};
 const activeTarget=targets.find(x=>(x.deviceId||x.id)===status.rdpTargetId),activeName=activeTarget?.name||status.rdpTargetId;
 return <><PageHead title="远程桌面" desc="仅显示 Server 授权的 RDP 目标，并优先建立 P2P TCP / UDP 辅助路径。" actions={<Button onClick={refreshTargets}>刷新设备</Button>}/>
 <Card title="RDP 客户端能力" eyebrow="CONTROLLER"><Setting title="启用远程桌面" desc="关闭时 Agent 不会向 Server 申请 rdp.controller，因此授权设备列表会为空。"><Switch checked={desiredEnabled} onChange={v=>setv('rdp.enabled',v)} label="启用远程桌面"/></Setting><Setting title="当前运行状态" desc={runtimeEnabled?'Agent 正在申请 RDP 能力':'当前进程未启用 RDP；保存后需要重启客户端'}><Badge tone={runtimeEnabled?'ok':'neutral'}>{runtimeEnabled?'已启用':'未启用'}</Badge></Setting><Setting title="Server 控制端授权" desc={status.connected?(controllerApproved?'当前会话已获得 rdp.controller':'当前会话没有 rdp.controller；请检查 Server 设备能力审批'):'连接 Server 后才能确认当前授权'}><Badge tone={controllerApproved?'ok':status.connected?'warn':'neutral'}>{controllerApproved?'已授权':status.connected?'未授权':'未连接'}</Badge></Setting>{dirty&&<SaveBar dirty label="保存 RDP 设置" hint="RDP 开关属于启动设置；保存后需重启客户端重新申请 Server 能力。" onSave={saveRDP}/>} {!dirty&&config.restartRequired&&arr(config.restartFields).some(x=>String(x).includes('RDP'))&&<div className="actions end top-gap"><Button primary onClick={restart}>重启客户端应用 RDP 设置</Button></div>}</Card>
 {!desiredEnabled&&<div className="notice warn"><b>RDP 已在本机配置中关闭。</b> 开启并保存、重启后，Agent 才会重新申请 rdp.controller 并获取已授权设备。</div>}
 {desiredEnabled&&runtimeEnabled&&status.connected&&!controllerApproved&&<div className="notice warn"><b>Server 未授予当前设备 rdp.controller。</b> 请在 Server 设备授权中启用“RDP 控制端”；授权变更后 Agent 会重新连接并刷新列表。</div>}
 {status.rdpTargetId&&<Card title="当前 RDP 会话" eyebrow="ACTIVE SESSION"><div className="rdp-session"><div className="identity-icon">▣</div><div className="grow"><strong>{activeName}</strong><div className="mono mini">{status.rdpTargetId}</div></div><Badge tone="ok">已连接</Badge></div><div className="setting top-gap"><div className="grow"><b>本地入口</b><div className="mono mini">{status.rdpListenAddr||'准备中'}</div></div><Button quiet disabled={!status.rdpListenAddr} onClick={()=>copy(status.rdpListenAddr)}>复制地址</Button></div><div className="cards-3 compact top-gap"><div><div className="mini">TCP Path</div><strong>{status.rdpPathTcp||'Relay'}</strong></div><div><div className="mini">UDP Path</div><strong>{status.rdpPathUdp||(status.rdpUdpActive?'Active':'—')}</strong></div><div><div className="mini">UDP</div><strong>{status.rdpUdpActive?'Active':status.rdpUdpEnabled?'Ready':'Off'}</strong></div></div>{status.rdpUdpReason&&<div className={cx('notice','top-gap',!status.rdpUdpEnabled&&'warn')}>{status.rdpUdpReason}</div>}<div className="actions end top-gap"><Button danger onClick={disconnect}>断开会话</Button></div></Card>}
 <Card title="已授权设备" eyebrow={targets.length+' TARGETS'}><div className="target-list">{targets.map(x=>{const id=x.deviceId||x.id,active=id===status.rdpTargetId;return <div className="target-row" key={id}><div className="identity-icon small">▣</div><div className="grow"><strong>{x.name||id}</strong><div className="mono mini">{id}</div></div>{active&&<Badge tone="blue">当前会话</Badge>}<Badge tone={x.online?'ok':'neutral'}>{x.online?'在线':'离线'}</Badge><Button primary disabled={!x.online||!!busy||active} onClick={()=>connect(id)}>{active?'已连接':busy===id?'连接中…':'连接'}</Button></div>})}{!targets.length&&<div className="empty">{!desiredEnabled?'本机 RDP 功能已关闭。':!runtimeEnabled?'RDP 设置等待重启生效。':status.connected&&!controllerApproved?'当前设备未获得 Server 的 rdp.controller 授权。':'当前没有 Server 授权的 RDP 目标设备。'}</div>}</div></Card></>
}

function MessagesPage({messages,refresh,clear,toast,config,setv,save}){
 const[filter,setFilter]=useState('all'),[query,setQuery]=useState(''),[preview,setPreview]=useState('');
 const classified=m=>({code:!!m.verificationCode||m.messageType==='verification_code'||m.popupType==='verification_code',important:m.messageType==='important'||m.popupType==='important'});
 const q=query.trim().toLowerCase();
 const filtered=messages.filter(m=>{const type=classified(m),typeOK=filter==='all'||(filter==='code'&&type.code)||(filter==='important'&&type.important)||(filter==='normal'&&!type.code&&!type.important);if(!typeOK)return false;if(!q)return true;return [m.title,m.content,m.source,m.verificationCode,m.messageType,m.popupType,m.messageRule,m.verificationRule].some(v=>String(v||'').toLowerCase().includes(q))});
 const copy=async t=>{if(!t)return;if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',String(t));else await navigator.clipboard.writeText(String(t));toast('已复制')};
 const timeout=Number(config?.verificationPopupTimeoutSec??15);
 return <><PageHead title="消息中心" desc="验证码、普通消息与重要提醒；匹配规则由 Server 管理，Agent 负责展示、弹窗和历史。" actions={<><Button onClick={refresh}>刷新</Button><Button danger onClick={clear}>清空历史</Button></>}/>
 <div className="cards-2">
  <Card title="弹窗策略" eyebrow="LOCAL PRESENTATION">
   <Setting title="消息弹窗自动关闭" desc="0 表示不自动关闭；支持的桌面端会使用系统原生弹窗。"><div className="row"><Input type="number" min="0" max="3600" value={timeout} onChange={e=>setv('verificationPopupTimeoutSec',Number(e.target.value))}/><span className="mini">秒</span></div></Setting>
   <Setting title="匹配规则" desc="规则名称、消息类型、popup 决策与验证码提取由 Relay Server 下发。"><Badge tone="blue">Server 管理</Badge></Setting>
   <div className="actions end top-gap"><Button primary onClick={()=>save({gui:{verificationPopupTimeoutSec:Number(config.verificationPopupTimeoutSec)}})}>保存弹窗设置</Button></div>
  </Card>
  <Card title="测试弹窗" eyebrow="PREVIEW"><div className="actions"><Button onClick={()=>setPreview('verification')}>验证码</Button><Button onClick={()=>setPreview('message')}>普通消息</Button><Button onClick={()=>setPreview('important')}>重要提醒</Button></div><div className="notice top-gap">弹窗通过图标、Badge、按钮和轻量边框区分消息类型，具体呈现由当前平台负责。</div></Card>
 </div>
 <div className="filter-bar"><div className="segment">{[['all','全部'],['code','验证码'],['normal','普通消息'],['important','重要提醒']].map(x=><button className={filter===x[0]?'active':''} key={x[0]} onClick={()=>setFilter(x[0])}>{x[1]}</button>)}</div><div className="message-search"><Input value={query} onChange={e=>setQuery(e.target.value)} placeholder="搜索标题、内容、来源、规则或验证码"/><span className="mini">{filtered.length} 条</span></div></div>
 <div className="message-list">{filtered.slice().reverse().map((m,i)=>{const type=classified(m),code=type.code,important=type.important,msgType=m.messageType||m.popupType||(code?'verification_code':important?'important':'message'),rule=m.messageRule||m.verificationRule||'未标注规则';return <article className={cx('message-card',important&&'important')} key={m.id||i}><div className={cx('message-icon',code?'code':important?'important':'')}>{code?'#':important?'!':'✦'}</div><div className="grow"><div className="row between"><strong>{m.title||(code?'验证码':important?'重要消息':'消息')}</strong><div className="actions"><Badge tone={code?'ok':important?'danger':'blue'}>{code?'验证码':important?'重要':'通知'}</Badge><Badge tone={m.popup?'ok':'neutral'}>{m.popup?'弹窗':'仅历史'}</Badge></div></div><div className="mini">{[m.source||'RelayProxy',rule,fmtTime(m.createdAt)].filter(Boolean).join(' · ')}</div><div className="chips top-gap"><span className="chip mono">{msgType}</span>{m.popupType&&m.popupType!==msgType&&<span className="chip mono">compat · {m.popupType}</span>}</div><p>{m.content||''}</p>{m.verificationCode&&<button className="code-box" onClick={()=>copy(m.verificationCode)}>{m.verificationCode}<small>点击复制</small></button>}</div></article>})}{!filtered.length&&<Card><div className="empty">{messages.length?'没有符合当前筛选条件的消息。':'暂无消息。'}</div></Card>}</div>
 <Modal open={!!preview} title={preview==='verification'?'验证码弹窗':preview==='important'?'重要提醒弹窗':'普通消息弹窗'} onClose={()=>setPreview('')} footer={<Button primary onClick={()=>setPreview('')}>关闭预览</Button>}><PopupPreview type={preview}/></Modal></>
}

function MonitorPage({connections: snapshot,history,refresh,clear,openNative}){
 const[query,setQuery]=useState(''),[stateFilter,setStateFilter]=useState('all'),[protocolFilter,setProtocolFilter]=useState('all'),[actionFilter,setActionFilter]=useState('all');
 const allRows=arr(snapshot?.connections);
 const q=query.trim().toLowerCase();
 const rows=allRows.filter(x=>{const state=String(x.state||'').toLowerCase(),protocol=String(x.protocol||'').toLowerCase(),action=String(x.action||'').toUpperCase();const stateOK=stateFilter==='all'||(stateFilter==='active'&&state==='active')||(stateFilter==='connecting'&&state==='connecting')||(stateFilter==='ended'&&/closed|ended|done/.test(state))||(stateFilter==='error'&&(/error|failed|reject|blocked/.test(state)||!!x.error));const protocolOK=protocolFilter==='all'||protocol===protocolFilter;const actionOK=actionFilter==='all'||action===actionFilter;if(!stateOK||!protocolOK||!actionOK)return false;if(!q)return true;return [x.process_name,x.process,x.host,x.ip,x.rule,x.exit_id,x.path,x.source,x.entry].some(v=>String(v||'').toLowerCase().includes(q))});
 const active=Number(snapshot?.active??allRows.filter(x=>/active|connecting/i.test(x.state||'')).length);
 const total=Number(snapshot?.total??allRows.length),omitted=Number(snapshot?.omitted||0);
 const totalBytes=Number(snapshot?.upload||0)+Number(snapshot?.download||0),rate=Number(snapshot?.upload_rate||0)+Number(snapshot?.download_rate||0);
 const targetText=x=>{const host=x.host||x.ip||'—',port=Number(x.port||0);return port?host+':'+port:host};
 const pathText=x=>x.path||(x.action==='DIRECT'?'DIRECT':x.exit_id?('PROXY · '+x.exit_id):(x.action||'—'));
 const hitText=x=>({'global_proxy':'全局代理','direct':'全局直连','default':'默认动作','loop-guard':'回环保护','relay-unavailable':'Relay 不可用','fakeip-dns':'本机 FakeIP DNS','fakeip-expired':'FakeIP 映射失效','fakeip-dns-exit-changed':'DNS 出口变更，FakeIP 缓存已失效','fakeip-disabled':'FakeIP 已关闭','fakeip-direct-unsupported':'FakeIP 不支持直连','fakeip-proxy-unavailable':'FakeIP 上游不可用','doh-endpoint-blocked':'已知 DoH 解析器阻断','doh-ip-endpoint-blocked':'DoH 目标 IP 阻断'}[x.rule]||x.rule||'未记录');
 return <><PageHead title="实时监控" desc="观察当前连接、进程、目标、规则、动作、出口、路径与流量。" actions={<><Button onClick={refresh}>刷新</Button><Button onClick={openNative}>独立窗口</Button><Button danger onClick={clear}>清理历史</Button></>}/>
 <div className="cards-4"><Metric label="活跃连接" value={active} hint={omitted?'另有 '+omitted+' 条未列出':'当前活动流'} series={history.active}/><Metric label="累计连接" value={total} hint="本进程计数窗口"/><Metric label="实时吞吐" value={fmtBytes(rate,true)} hint={fmtBytes(snapshot?.download_rate||0,true)+' ↓ · '+fmtBytes(snapshot?.upload_rate||0,true)+' ↑'} series={history.total}/><Metric label="累计流量" value={fmtBytes(totalBytes)} hint={fmtBytes(snapshot?.download||0)+' ↓ · '+fmtBytes(snapshot?.upload||0)+' ↑'}/></div>
 <Card title="连接列表" eyebrow={rows.length+' / '+allRows.length+' VISIBLE'}><div className="monitor-filters"><Input value={query} onChange={e=>setQuery(e.target.value)} placeholder="搜索进程 / 目标 / 规则 / 出口"/><Select value={stateFilter} onChange={e=>setStateFilter(e.target.value)}><option value="all">全部状态</option><option value="connecting">连接中</option><option value="active">活跃</option><option value="ended">已结束</option><option value="error">失败 / 阻断</option></Select><Select value={protocolFilter} onChange={e=>setProtocolFilter(e.target.value)}><option value="all">全部协议</option><option value="tcp">TCP</option><option value="udp">UDP</option></Select><Select value={actionFilter} onChange={e=>setActionFilter(e.target.value)}><option value="all">全部动作</option><option value="PROXY">代理</option><option value="DIRECT">直连</option><option value="REJECT">阻断</option></Select></div><div className="conn-table"><div className="conn-row conn-head"><span>应用 / 目标</span><span>连接路径</span><span>命中规则</span><span>状态</span><span>流量 / 速率</span><span>开始时间</span></div>{rows.map((x,i)=>{const proc=x.process_name||x.process||'未知进程',target=targetText(x),path=pathText(x),state=x.state||'—',up=Number(x.upload||0),down=Number(x.download||0),upRate=Number(x.upload_rate||0),downRate=Number(x.download_rate||0);return <div className="conn-row" key={x.id||i}><div><strong>{proc}</strong><div className="mono mini">{target} · {(x.protocol||'tcp').toUpperCase()}</div>{x.host&&<div className="mini">IP {x.ip||'—'} · 域名来源：{{'dns':'DNS','tls-sni':'TLS SNI','http-host':'HTTP Host','network-extension':'系统网络扩展','requested':'应用请求','fakeip':'FakeIP DNS 映射'}[x.domain_source]||x.domain_source||'未知'}</div>}{x.error&&<div className="conn-error">{x.error}</div>}</div><div><span>{path}</span><div className="mini">{x.entry||x.source||''}</div></div><div><span className={cx('rule-hit',!x.rule&&'muted')} title={'本连接路由决策：'+hitText(x)}>{hitText(x)}</span></div><Badge tone={tone(state)}>{state}</Badge><div className="mono"><div>{fmtBytes(down)} ↓ · {fmtBytes(up)} ↑</div><div className="mini">{fmtBytes(downRate,true)} ↓ · {fmtBytes(upRate,true)} ↑</div></div><span className="mini">{fmtTime(x.started_at)}</span></div>})}{!rows.length&&<div className="empty">{allRows.length?'没有符合当前筛选条件的连接。':'暂无连接记录。'}</div>}</div></Card></>
}

function DiagnosticsPage({diagnostics,logs,refreshDiagnostics,clearLogs,copyLogs}){
 const[filter,setFilter]=useState('all');
 const match=(line,type)=>{if(type==='all')return true;const s=String(line||'');if(type==='relay')return /relay|tunnel|control stream|server|handshake|tls/i.test(s);if(type==='p2p')return /p2p|punch|candidate|rendezvous|stun|hole.?punch/i.test(s);if(type==='proxy')return /proxy|socks5?|http proxy|divert|windivert|routing|fakeip/i.test(s);if(type==='exit')return /exit|egress|upstream|public direct|direct path|acl/i.test(s);return true};
 const visible=logs.filter(line=>match(line,filter));
 const st=diagnostics?.status||{},tunnel=st.tunnelDiagnostics||{},quic=tunnel.quic||{},diagConnections=arr(diagnostics?.connections),exitTCP=arr(diagnostics?.exit?.active_tcp);
 const quicLoss=Number(quic.sent_packet_loss_pct||0),quicRTT=Number(quic.smoothed_rtt_ms||quic.latest_rtt_ms||st.latency||0);
 return <><PageHead title="诊断与日志" desc="检查中继连接、P2P、代理转发、出口处理与网络接管状态。" actions={<><Button onClick={refreshDiagnostics}>刷新诊断</Button><Button onClick={copyLogs}>复制日志</Button><Button danger onClick={clearLogs}>清空日志</Button></>}/>
 <div className="diag-grid">
  <Card title="控制连接" eyebrow="RELAY"><div className={cx('diag-value',st.connected?'ok':'danger')}>{st.connected?'正常':'断开'}</div><div className="mini">{st.connected?((st.transport||tunnel.transport||'—')+' · RTT '+(st.latency||0)+' ms'):'当前无活动 Relay 会话'}</div>{tunnel.remote&&<div className="mono mini top-gap">{tunnel.local||'—'} → {tunnel.remote}</div>}</Card>
  <Card title="QUIC" eyebrow="TRANSPORT"><div className="diag-value">{quicRTT?quicRTT.toFixed(1)+' ms':'—'}</div><div className="mini">丢包 {quicLoss.toFixed(2)}% · GSO {quic.gso?'ON':'OFF'}</div><div className="mini">{fmtBytes(quic.receive_bps||0,true)} ↓ · {fmtBytes(quic.send_bps||0,true)} ↑</div></Card>
  <Card title="Public Direct" eyebrow="DIRECT PATH"><div className={cx('diag-value',tone(st.directState))}>{st.directState||'idle'}</div><div className="mini">{st.directPath||st.directEndpoint||'未建立'}{st.directRttMs?' · '+st.directRttMs+' ms':''}</div><div className="mini">回落 {Number(st.directFallbackCount||0)} 次</div></Card>
  <Card title="P2P" eyebrow="PEER PATH"><div className={cx('diag-value',tone(st.p2pState))}>{st.p2pState||'idle'}</div><div className="mini">{st.p2pPath||st.p2pCandidateSummary||'未建立'}{st.p2pRttMs?' · '+st.p2pRttMs+' ms':''}</div><div className="mini">回落 {Number(st.p2pFallbackCount||0)} 次</div><Setting title="UPnP 端口映射" desc={st.upnpError||st.upnpAddress||'默认关闭；需要本机和 Server 同时允许'}><Badge tone={st.upnpState==='MAPPED'?'ok':st.upnpState==='DEGRADED'||st.upnpState==='FAILED'?'danger':st.upnpState==='CGNAT'?'warn':'neutral'}>{st.upnpState||'DISABLED'}</Badge></Setting></Card>
  <Card title="透明代理" eyebrow="WINDIVERT"><div className={cx('diag-value',st.divertRunning?'ok':st.divertError?'danger':'')}>{st.divertRunning?'运行中':'未运行'}</div><div className="mini">{[st.divertDiagnostics?.captureBackend,st.divertDiagnostics?.captureState||st.divertStage||st.networkMode].filter(Boolean).join(' · ')||'off'}</div><div className="mini">抓包 {Number(st.divertDiagnostics?.captured||0)} · 分类 {Number(st.divertDiagnostics?.classified||0)} · PROXY {Number(st.divertDiagnostics?.proxy||0)} · DIRECT {Number(st.divertDiagnostics?.direct||0)} · REJECT {Number(st.divertDiagnostics?.reject||0)}</div><div className={cx('mini',Number(st.divertDiagnostics?.injectionError||0)>0&&'danger-text')}>注入 {Number(st.divertDiagnostics?.injected||0)} · 注入失败 {Number(st.divertDiagnostics?.injectionError||0)} · 重连 {Number(st.divertDiagnostics?.reconnects||0)}</div>{(st.divertError||st.divertDiagnostics?.lastError)&&<div className="conn-error top-gap">{st.divertError||st.divertDiagnostics?.lastError}</div>}</Card>
  <Card title="DNS 防泄漏状态" eyebrow="SECURITY"><div className={cx('diag-value',st.dnsProtection?.capture==='nfqueue-no-bypass'?'ok':st.dnsProtection?.fakeIpEnabled?'warn':'')}>{st.dnsProtection?.fakeIpEnabled?'FakeIP 已启用':'FakeIP 未启用'}</div><div className="mini">内核 DNS 接管：{({'nfqueue-no-bypass':'Linux NFQUEUE 严格接管','nfqueue-bypass':'NFQUEUE 普通模式','partial-update':'规则更新不完整','running-unverified':'透明拦截运行中 · 未独立验证','unavailable':'未运行'})[st.dnsProtection?.capture]||st.dnsProtection?.capture||'未检测'}</div><div className={cx('mini',st.dnsProtection?.independentGuard!=='rules-present'&&'danger-text')}>独立 Kill Switch：{({'rules-present':'发现持久防火墙规则 · 仍需实机验证','persistence-unverified':'发现规则，但未验证开机恢复','not-installed':'未安装','unknown':'无法验证','unverified':'未验证','incomplete':'规则不完整','unsupported':'不支持'})[st.dnsProtection?.independentGuard]||'未检测'}</div>{st.dnsProtection?.detail&&<div className="mini top-gap">{st.dnsProtection.detail}</div>}<div className="notice top-gap">显示的是内核/防火墙配置证据，不是零 DNS 泄漏认证。DoH/443、环回 DNS 与断线行为仍需真机测试。</div></Card>
  <Card title="Native UDP" eyebrow="DATAGRAM"><div className="diag-value">{Number(st.nativeUdp?.associations||0)}</div><div className="mini">活动 Association · Queue {fmtBytes(st.nativeUdp?.queueBytes||0)}</div><div className={cx('mini',(Number(st.nativeUdp?.associationRejects||0)+Number(st.nativeUdp?.queueDrops||0)+Number(st.nativeUdp?.reassemblyDrops||0))>0&&'danger-text')}>拒绝 {Number(st.nativeUdp?.associationRejects||0)} · 队列丢弃 {Number(st.nativeUdp?.queueDrops||0)} · 重组丢弃 {Number(st.nativeUdp?.reassemblyDrops||0)}</div></Card>
 </div>
 <Card title="活动连接" eyebrow="ACTIVE CONNECTIONS" action={<span className="mini">{diagConnections.length} 条活动 · Exit TCP {exitTCP.length} · 采样 {fmtTime(diagnostics?.sampledAt||diagnostics?.exit?.sampled_at)}</span>}>{diagConnections.length===0?<div className="muted">当前没有活动连接</div>:<div className="diag-connections">{diagConnections.slice(0,8).map((x,i)=><div className="diag-conn" key={x.id||i}><div className="grow"><strong>{x.process_name||x.process||'未知进程'}</strong><div className="mono mini">{(x.host||x.ip||'—')+(x.port?':'+x.port:'')}</div></div><div><b>{fmtBytes(x.download_rate||0,true)} ↓</b><div className="mini">{x.path||x.action||'—'}</div></div></div>)}</div>}</Card>
 <div className="cards-2">
  <Card title="运行建议" eyebrow="HEALTH"><div className="stack">{!st.connected&&<div className="notice danger">控制隧道当前未连接，请先检查 Server 地址、端口、TLS 与网络连通性。</div>}{quicLoss>=3&&<div className="notice warn">当前 QUIC 发送丢包为 {quicLoss.toFixed(2)}%，建议比较 Public Direct / P2P / Relay 路径并运行出口测速。</div>}{st.divertError&&<div className="notice danger">透明代理错误：{st.divertError}</div>}{!st.divertError&&<div className="notice">透明代理异常时，请检查 Network Service、WinDivert 驱动和管理员安装状态。</div>}<div className="notice">Direct/P2P 出现持续回落时，结合出口测速、候选地址和日志分类确认瓶颈。</div></div></Card>
  <Card title="详细诊断数据" eyebrow="RAW SNAPSHOT"><details className="details"><summary>展开原始诊断快照</summary><pre className="diagnostic-json">{JSON.stringify(diagnostics||{},null,2)}</pre></details></Card>
 </div>
 <Card title="运行日志" eyebrow={visible.length+' / '+logs.length+' LINES'}><div className="log-toolbar"><div className="segment">{[['all','全部'],['relay','中继'],['p2p','P2P'],['proxy','代理'],['exit','出口']].map(x=><button key={x[0]} className={filter===x[0]?'active':''} onClick={()=>setFilter(x[0])}>{x[1]}</button>)}</div><span className="mini">{filter==='all'?'显示全部日志':'仅显示匹配分类的日志'}</span></div><div className="log-console">{visible.length?visible.map((x,i)=><div key={i} className={cx(/error|failed|失败|fatal|timeout/i.test(x)&&'log-error',/warn|retry|警告/i.test(x)&&'log-warn')}>{x}</div>):<div className="muted">当前分类暂无日志</div>}</div></Card></>
}

function SettingsPage({config,setv,save,toast,onReload,onRestart,onQuit,onGoto,dirty,onConfigRefresh,onRefreshCapabilities}){
 const[service,setService]=useState(null);
 const windowsServiceAvailable=hasBridge("goGetNetworkServiceStatus");
 useEffect(()=>{if(hasBridge('goGetNetworkServiceStatus'))callJSON('goGetNetworkServiceStatus',{}).then(setService).catch(()=>{})},[]);
 const act=async(name,msg)=>{if(name==='goUninstallNetworkService'&&dirty){toast('当前有未保存修改，请先保存或放弃后再卸载 Network Service','danger');return}try{const r=parseMutation(await call(name));if(!r.ok&&r.message)throw new Error(r.message);if(name==='goUninstallNetworkService'&&onConfigRefresh)await onConfigRefresh();if(onRefreshCapabilities)await onRefreshCapabilities();toast(r.message||msg);if(hasBridge('goGetNetworkServiceStatus'))setService(await callJSON('goGetNetworkServiceStatus',{}))}catch(e){toast(e.message,'danger')}};
 const auto=async v=>{try{const r=parseMutation(await call('goSetAutostart',v));if(!r.ok&&r.message)throw new Error(r.message);setv('isAutostart',v,false);toast(v?'已启用开机自启':'已关闭开机自启')}catch(e){toast(e.message,'danger')}};
 return <><PageHead title="设置" desc="主题、窗口行为、开机自启与客户端维护。"/>
 {config.restartRequired&&<div className="notice warn">以下设置需要重启客户端后生效：{arr(config.restartFields).join('、')||'启动参数'}。</div>}
 {config.reloadPending&&<div className="notice warn">配置文件中的策略与当前运行状态不同，可重新读取并应用。</div>}
 <div className="cards-2"><Card title="外观与窗口" eyebrow="APPEARANCE"><Setting title="主题" desc="跟随当前操作系统的外观设置"><Select value={config.theme||'system'} onChange={e=>setv('theme',e.target.value)}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></Select></Setting><Setting title="关闭时最小化到托盘" desc="关闭主窗口后 Agent 保持运行"><Switch checked={!!config.minimizeToTray} onChange={v=>setv('minimizeToTray',v)}/></Setting><Setting title="消息弹窗" desc="自动关闭时长与弹窗预览在消息中心配置"><Button quiet onClick={()=>onGoto('messages')}>前往消息中心 ›</Button></Setting><div className="actions end top-gap"><Button primary onClick={()=>save({gui:{theme:config.theme,minimizeToTray:config.minimizeToTray}})}>保存界面设置</Button></div></Card>
 <Card title="启动与文件" eyebrow="APPLICATION"><Setting title="开机自启" desc="启动能力及所需权限由当前操作系统决定"><Switch checked={!!config.isAutostart} onChange={auto}/></Setting><Setting title="配置文件" desc={config.configPath||'未指定'}><Button onClick={()=>call('goOpenConfigDir')}>打开目录</Button></Setting><Setting title="版本" desc="RelayProxy Agent"><span className="mono">{config.version||'dev'}</span></Setting><div className="actions top-gap"><Button onClick={onReload}>重新读取并应用</Button><Button onClick={onRestart}>重启客户端</Button><Button danger onClick={onQuit}>退出</Button></div></Card></div>
 {windowsServiceAvailable&&<Card title="Windows 网络服务" eyebrow="NETWORK SERVICE"><div className="setting"><div className="grow"><b>服务状态</b><div className="mini">{service?.message||'透明代理由低权限 GUI 与 SYSTEM 网络服务协作完成。'}</div></div><Badge tone={service?.ready?'ok':service?.installed?'warn':'neutral'}>{service?.ready?'就绪':service?.running?'运行中':service?.installed?'已安装':'未安装'}</Badge></div>{service&&<div className="service-grid"><div><span>安装</span><b>{service.installed?'是':'否'}</b></div><div><span>运行</span><b>{service.running?'是':'否'}</b></div><div><span>自动启动</span><b>{service.autoStartKnown?(service.autoStart?'是':'否'):'未知'}</b></div><div><span>版本匹配</span><b>{service.versionMatch?'是':'否'}</b></div><div><span>恢复策略</span><b>{service.recoveryKnown?(service.recoveryEnabled?'已启用':'未启用'):'未知'}</b></div><div><span>PID</span><b className="mono">{service.pid||'—'}</b></div>{service.binaryPath&&<div className="service-path"><span>程序路径</span><b className="mono">{service.binaryPath}</b></div>}</div>}{dirty&&<div className="notice warn top-gap">存在未保存配置时暂不允许卸载 Network Service，避免原生侧关闭透明代理后导致草稿 revision 失效。</div>}<div className="actions top-gap"><Button onClick={()=>act('goRepairNetworkService','网络服务修复完成')}>修复服务</Button><Button danger disabled={dirty} onClick={()=>window.confirm('卸载 RelayProxy Windows 网络服务？此操作会同时关闭已保存的系统透明代理配置。')&&act('goUninstallNetworkService','网络服务已卸载')}>卸载服务</Button></div></Card>}</>
}

function PopupPreview({type}){
 const isCode=type==='verification',important=type==='important';
 return <div className={cx('popup-preview',important&&'important')}><div className="popup-preview-head"><div className={cx('message-icon',isCode?'code':important?'important':'')}>{isCode?'#':important?'!':'✦'}</div><div><div className="eyebrow">{important?'IMPORTANT':isCode?'VERIFICATION CODE':'MESSAGE'}</div><strong>{important?'重要提醒':isCode?'收到新的验证码':'收到新消息'}</strong></div></div><div className="mini top-gap">来自 RelayProxy · 刚刚</div>{isCode&&<div className="preview-code">482913</div>}<p>{important?'Tokyo 出口已从 Public Direct 降级至 Relay，请检查公网直连状态。':isCode?'用于登录验证，请勿向他人泄露。':'设备审批已完成，新的授权能力已同步到本机。'}</p><div className="actions end"><Button>{isCode?'稍后处理':'知道了'}</Button>{isCode&&<Button primary>复制验证码</Button>}</div></div>
}

function SpeedModal({open,exits,initialExit,onClose,toast}){
 const[exitID,setExitID]=useState(''),[duration,setDuration]=useState(3),[busy,setBusy]=useState(false),[result,setResult]=useState(null);
 const exitsRef=useRef(exits);exitsRef.current=exits;
 useEffect(()=>{if(!open)return;setResult(null);setExitID(exitUsable(exitsRef.current,initialExit)?initialExit:'')},[open,initialExit]);
 useEffect(()=>{if(open&&exitID&&!exitUsable(exits,exitID))setExitID('')},[open,exitID,exits]);
 const run=async()=>{setBusy(true);setResult(null);try{const p=parseMutation(await call('goRunSpeedTest',exitID,Number(duration)));if(!p.ok)throw new Error(p.message||'测速失败');setResult(p.result||p);toast('出口测速完成')}catch(e){setResult({error:e.message})}finally{setBusy(false)}};
 const leg=(label,v={})=><div><div className="eyebrow">{label}</div><div className="big small-big">{Number(v.megabitsPerSecond??v.mbps??0).toFixed(2)} Mbps</div><div className="mini">{(v.path||'—')+' · '+fmtBytes(v.bytesPerSecond||0,true)}</div>{v.quic&&<div className="mini">丢包 {Number(v.quic.sent_packet_loss_pct||0).toFixed(2)}% · RTT 抖动 {Number(v.quic.rtt_deviation_ms||0).toFixed(1)} ms · GSO {v.quic.gso?'ON':'OFF'}{v.quic.congestion_controller?' · '+v.quic.congestion_controller:''}{(v.quic.udp_read_buffer_bytes||v.quic.udp_write_buffer_bytes)?<div>UDP 缓冲：读取 {fmtBytes(v.quic.udp_read_buffer_bytes||0)} · 写入 {fmtBytes(v.quic.udp_write_buffer_bytes||0)}</div>:null}</div>}</div>;
 return <Modal open={open} title="出口双向测速" onClose={onClose} footer={<><Button onClick={onClose}>关闭</Button><Button primary disabled={busy} onClick={run}>{busy?'测速中…':'开始测速'}</Button></>}><div className="form-grid"><Field label="出口"><Select value={exitID} onChange={e=>setExitID(e.target.value)}><option value="">当前 / 自动出口</option>{exits.map(x=><option key={x.deviceId||x.id} value={x.deviceId||x.id} disabled={x.online===false}>{x.name||x.deviceId}{x.online===false?' · 离线':''}</option>)}</Select></Field><Field label="每方向时长（秒）"><Input type="number" min="1" max="30" value={duration} onChange={e=>setDuration(Number(e.target.value))}/></Field></div>{result&&<div className={cx('speed-result',result.error&&'error')}>{result.error?result.error:<>{leg('上行',result.upload||result.up)}{leg('下行',result.download||result.down)}</>}</div>}</Modal>
}

function BatchSpeedModal({open,exits,onClose,toast}){
 const[rows,setRows]=useState([]),[busy,setBusy]=useState(false);
 const cancelRef=useRef(false);
 const exitsRef=useRef(exits);exitsRef.current=exits;
 useEffect(()=>{if(open)setRows(arr(exitsRef.current).filter(x=>x.online!==false).map(x=>({id:x.deviceId||x.id,name:x.name||x.deviceId,state:'pending'})));else cancelRef.current=true},[open]);
 const run=async()=>{if(busy)return;const targets=arr(exits).filter(x=>x.online!==false);if(!targets.length){toast('没有可测速的在线出口','danger');return}setBusy(true);cancelRef.current=false;setRows(targets.map(x=>({id:x.deviceId||x.id,name:x.name||x.deviceId,state:'pending'})));for(const x of targets){if(cancelRef.current)break;const id=x.deviceId||x.id;setRows(p=>p.map(r=>r.id===id?{...r,state:'running'}:r));try{const payload=parseMutation(await call('goRunSpeedTest',id,2));if(!payload.ok)throw new Error(payload.message||'测速失败');const result=payload.result||payload,up=result.upload||{},down=result.download||{};if(cancelRef.current)break;setRows(p=>p.map(r=>r.id===id?{...r,state:'done',up:Number(up.megabitsPerSecond||0),down:Number(down.megabitsPerSecond||0),path:down.path||up.path||'—'}:r))}catch(e){if(cancelRef.current)break;setRows(p=>p.map(r=>r.id===id?{...r,state:'error',error:e.message}:r))}}setBusy(false);if(!cancelRef.current)toast('全部出口测速完成')};
 const close=()=>{cancelRef.current=true;onClose()};
 return <Modal open={open} title="全部出口测速" wide onClose={close} footer={<><Button onClick={close}>{busy?'停止并关闭':'关闭'}</Button><Button primary disabled={busy} onClick={run}>{busy?'测速中…':'开始全部测速'}</Button></>}><div className="notice">按出口逐个执行双向测速，每个方向 2 秒；关闭窗口后将在当前出口测试结束时停止后续测速。</div><div className="batch-speed-list">{rows.map(r=><div className="batch-speed-row" key={r.id}><div className="grow"><strong>{r.name}</strong><div className="mono mini">{r.id}</div></div>{r.state==='pending'&&<Badge>等待</Badge>}{r.state==='running'&&<Badge tone="warn">测速中</Badge>}{r.state==='done'&&<div className="batch-result"><b>{r.down.toFixed(1)} ↓ / {r.up.toFixed(1)} ↑ Mbps</b><span>{r.path}</span></div>}{r.state==='error'&&<div className="batch-error">{r.error}</div>}</div>)}</div></Modal>
}

export default function App(){
 const[page,setPage]=useState(initialPage),[status,setStatus]=useState({connected:false,proxyExits:[]}),[config,setConfig]=useState(EMPTY),[loaded,setLoaded]=useState(false),[configError,setConfigError]=useState(''),[confirmQuit,setConfirmQuit]=useState(false),[dirty,setDirty]=useState(false),[pendingAction,setPendingAction]=useState(null),[exits,setExits]=useState([]),[exitsReady,setExitsReady]=useState(false),[targets,setTargets]=useState([]),[messages,setMessages]=useState([]),[unreadMessages,setUnreadMessages]=useState(0),[connections,setConnections]=useState({connections:[],active:0,total:0,omitted:0,upload:0,download:0,upload_rate:0,download_rate:0}),[logs,setLogs]=useState([]),[diagnostics,setDiagnostics]=useState({}),[toastState,setToastState]=useState(null),[speedOpen,setSpeedOpen]=useState(false),[speedExit,setSpeedExit]=useState(''),[batchSpeedOpen,setBatchSpeedOpen]=useState(false),[history,setHistory]=useState({down:[],up:[],total:[],active:[],latency:[]});
 const pushHistory=useCallback(patch=>setHistory(h=>{const n={...h};for(const k in patch)n[k]=[...h[k],Number(patch[k])||0].slice(-SPARK_POINTS);return n}),[]);
 const applyStatus=useCallback(x=>{setStatus(x||{});pushHistory({latency:x?.connected?x.latency:0})},[pushHistory]);
 const toastTimer=useRef(null),exitInventoryKey=useRef(''),pageRef=useRef(page),messageIDs=useRef(new Set());
 const toast=useCallback((message,t='normal')=>{clearTimeout(toastTimer.current);setToastState({message,t});toastTimer.current=setTimeout(()=>setToastState(null),3200)},[]);
 const theme=useCallback(mode=>{const m=['light','dark','system'].includes(mode)?mode:'system',dark=m==='dark'||(m==='system'&&matchMedia('(prefers-color-scheme: dark)').matches);document.documentElement.dataset.themeMode=m;document.documentElement.dataset.theme=dark?'dark':'light';document.documentElement.classList.toggle('dark',dark)},[]);
 const syncExitInventory=useCallback(async x=>{const summary=arr(x?.proxyExits),key=makeExitInventoryKey(x);if(exitInventoryKey.current===key)return;exitInventoryKey.current=key;if(!hasBridge('goGetProxyExits')){setExits(summary);setExitsReady(true);return}try{setExits(arr(await callJSON('goGetProxyExits',[])));setExitsReady(true)}catch{setExits(summary);setExitsReady(true)}},[]);
 const refreshStatus=useCallback(async()=>{if(!hasBridge('goGetStatus'))return null;try{const x=await callJSON('goGetStatus',{});applyStatus(x);void syncExitInventory(x);return x}catch{return null}},[applyStatus,syncExitInventory]);
 const refreshConfig=useCallback(async()=>{try{const x=await callJSON('goGetConfig',null);if(!x||x.configError)throw new Error(x?.configError?('配置文件 '+(x.configPath||'路径未知')+' 读取失败：'+x.configError):'配置接口没有返回有效数据');setConfig({...EMPTY,...x,customExits:arr(x.customExits),socks5:{...EMPTY.socks5,...x.socks5},http:{...EMPTY.http,...x.http},rdp:{...EMPTY.rdp,...x.rdp},p2p:{...EMPTY.p2p,...x.p2p},exitUpstream:{...EMPTY.exitUpstream,...x.exitUpstream},network:{...EMPTY.network,...x.network},routing:{...EMPTY.routing,...x.routing,rules:arr(x.routing?.rules)}});theme(x.theme||'system');setLoaded(true);setConfigError('');setDirty(false);return true}catch(e){setConfigError(e?.message||'配置读取失败');return false}},[theme]);
 const refreshNetworkCapabilities=useCallback(async()=>{if(!hasBridge('goGetNetworkCapabilities'))return null;try{const x=await callJSON('goGetNetworkCapabilities',{});setConfig(p=>({...p,networkCapabilities:x||{}}));return x}catch{return null}},[]);
 const refreshExits=useCallback(async()=>{if(hasBridge('goGetProxyExits'))try{setExits(arr(await callJSON('goGetProxyExits',[])));setExitsReady(true)}catch(e){toast('读取授权出口失败：'+e.message,'danger')}},[toast]);
 const refreshTargets=useCallback(async()=>{if(hasBridge('goGetRDPTargets'))try{setTargets(arr(await callJSON('goGetRDPTargets',[])))}catch(e){toast(e.message,'danger')}},[toast]);
 const refreshMessages=useCallback(async()=>{if(hasBridge('goGetMessages'))try{const items=arr(await callJSON('goGetMessages',[]));messageIDs.current=new Set(items.map(x=>x?.id).filter(Boolean));setMessages(items)}catch{}},[]);
 const refreshConnections=useCallback(async()=>{if(hasBridge('goGetConnections'))try{const x=await callJSON('goGetConnections',{connections:[]});const snap=Array.isArray(x)?{connections:x,active:x.filter(v=>/active|connecting/i.test(v.state||'')).length,total:x.length,omitted:0,upload:0,download:0,upload_rate:0,download_rate:0}:{connections:arr(x?.connections),active:Number(x?.active||0),total:Number(x?.total||0),omitted:Number(x?.omitted||0),upload:Number(x?.upload||0),download:Number(x?.download||0),upload_rate:Number(x?.upload_rate||0),download_rate:Number(x?.download_rate||0),sampled_at:x?.sampled_at,rate_window:x?.rate_window};setConnections(snap);pushHistory({down:snap.download_rate,up:snap.upload_rate,total:snap.download_rate+snap.upload_rate,active:snap.active})}catch{}},[pushHistory]);
 const refreshLogs=useCallback(async()=>{if(hasBridge('goGetLogs'))try{setLogs(arr(await callJSON('goGetLogs',[])).map(logText).filter(Boolean))}catch{}},[]);
 const refreshDiagnostics=useCallback(async()=>{if(hasBridge('goGetDiagnostics'))try{setDiagnostics(await callJSON('goGetDiagnostics',{}))}catch(e){toast(e.message,'danger')}},[toast]);

 useEffect(()=>{let disposed=false;(async()=>{await refreshConfig();if(!disposed)await Promise.all([refreshStatus(),refreshExits(),refreshTargets(),refreshMessages(),refreshConnections(),refreshLogs(),refreshDiagnostics()])})();const a=setInterval(refreshStatus,1500),b=setInterval(()=>{refreshConnections();refreshMessages();refreshLogs();refreshTargets()},2500);const mq=matchMedia('(prefers-color-scheme: dark)'),mh=()=>document.documentElement.dataset.themeMode==='system'&&theme('system');mq.addEventListener?.('change',mh);const uninstall=installNativeHooks({onStatus:x=>{if(typeof x==='string')try{x=JSON.parse(x)}catch{return}applyStatus(x);void syncExitInventory(x)},onLog:x=>setLogs(p=>[...p.slice(-1999),logText(x)].filter(Boolean)),onMessage:m=>{const id=m?.id||'',isNew=!!id&&!messageIDs.current.has(id);if(id)messageIDs.current.add(id);setMessages(p=>{if(!id)return[...p,m].filter(Boolean).slice(-500);const i=p.findIndex(x=>x.id===id);if(i<0)return[...p,m].slice(-500);const n=[...p];n[i]=m;return n});if(isNew&&pageRef.current!=='messages')setUnreadMessages(n=>Math.min(99,n+1))},onTheme:m=>{theme(m);setConfig(p=>({...p,theme:m}))},onAutostart:v=>setConfig(p=>({...p,isAutostart:!!v}))});return()=>{disposed=true;clearInterval(a);clearInterval(b);clearTimeout(toastTimer.current);mq.removeEventListener?.('change',mh);uninstall()}},[applyStatus,refreshConfig,refreshStatus,refreshExits,refreshTargets,refreshMessages,refreshConnections,refreshLogs,refreshDiagnostics,syncExitInventory,theme]);

 // Wails or the HTTP bridge may start after React mounts. Keep retrying
 // until settings are actually available instead of freezing on empty defaults.
 useEffect(()=>{if(loaded)return;const timer=setInterval(()=>{void refreshConfig()},3000);return()=>clearInterval(timer)},[loaded,refreshConfig]);
 useEffect(()=>{pageRef.current=page;sessionStorage.setItem('relayproxy-react-page',page);if(page==='exits')refreshExits();if(page==='rdp')refreshTargets();if(page==='messages'){setUnreadMessages(0);refreshMessages()}if(page==='monitor')refreshConnections();if(page==='diagnostics'){refreshDiagnostics();refreshLogs()}},[page,refreshExits,refreshTargets,refreshMessages,refreshConnections,refreshDiagnostics,refreshLogs]);
 useEffect(()=>theme(config.theme||'system'),[config.theme,theme]);

 const setv=useCallback((path,value,markDirty=true)=>{setConfig(prev=>{const next=structuredClone(prev),keys=path.split('.');let o=next;for(let i=0;i<keys.length-1;i++){o[keys[i]]=o[keys[i]]&&typeof o[keys[i]]==='object'?o[keys[i]]:{};o=o[keys[i]]}o[keys[keys.length-1]]=value;return next});if(markDirty)setDirty(true)},[]);
 const save=useCallback(async payload=>{if(!loaded||!config.revision){toast('配置尚未就绪，暂不能保存','danger');return false}try{const r=await saveConfig(config.revision,payload);if(!r.ok)throw new Error(r.message||'保存失败');await refreshConfig();await refreshStatus();toast(r.restartRequired?(r.message||'已保存，部分设置需重启'):(r.message||'已保存'));return true}catch(e){toast(e.message,'danger');return false}},[loaded,config.revision,toast,refreshConfig,refreshStatus]);
 const select=useCallback(async id=>{try{const r=parseMutation(await call('goSelectExit',id));if(!r.ok&&r.message)throw new Error(r.message);await refreshConfig();await refreshStatus();toast(id?'已切换到 '+exitName(exits,id):'已切换为自动选择')}catch(e){toast(e.message,'danger')}},[exits,refreshConfig,refreshStatus,toast]);
 const clearMessages=useCallback(async()=>{if(messages.length&&!confirm('清空本设备的消息历史？'))return;await call('goClearMessages');messageIDs.current.clear();setMessages([]);setUnreadMessages(0);toast('消息历史已清空')},[messages.length,toast]);
 const clearConnections=useCallback(async()=>{await call('goClearConnections');await refreshConnections();toast('已清理结束连接历史')},[refreshConnections,toast]);
 const clearLogs=useCallback(async()=>{await call('goClearLogs');setLogs([]);toast('日志已清空')},[toast]);
 const copyLogs=useCallback(async()=>{const text=logs.join('\n');if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',text);else await navigator.clipboard.writeText(text);toast('日志已复制')},[logs,toast]);
 const requestPage=useCallback(next=>{if(next===page)return;if(dirty){setPendingAction({kind:'page',target:next});return}setPage(next)},[dirty,page]);
 useEffect(()=>{window.relayNavigate=requestPage;return()=>{delete window.relayNavigate}},[requestPage]);
 const reloadConfig=useCallback(async()=>{let r;try{r=parseMutation(await call('goReloadConfig'))}catch(e){r={ok:false,message:e.message}}await refreshConfig();await refreshStatus();if(!r.ok){toast(r.message||'重新读取配置失败','danger');return}toast(r.message||'已重新读取配置')},[refreshConfig,refreshStatus,toast]);
 const restartApp=useCallback(async()=>{try{const r=parseMutation(await call('goRestart'));if(!r.ok)throw new Error(r.message||'重启客户端失败')}catch(e){toast(e.message,'danger')}},[toast]);
 const quitApp=useCallback(async()=>{try{await call('goQuit')}catch(e){toast('退出失败：'+(e?.message||String(e)),'danger')}},[toast]);
 const requestLifecycle=useCallback(kind=>{if(dirty){setPendingAction({kind});return}if(kind==='reload')void reloadConfig();else if(kind==='restart')void restartApp();else if(kind==='quit')setConfirmQuit(true)},[dirty,reloadConfig,restartApp,quitApp]);
 const discardAndContinue=useCallback(async()=>{const action=pendingAction;if(!action)return;setPendingAction(null);setDirty(false);if(action.kind==='page'){await refreshConfig();setPage(action.target);return}if(action.kind==='reload'){await reloadConfig();return}if(action.kind==='restart'){await restartApp();return}if(action.kind==='quit'){await quitApp()}},[pendingAction,refreshConfig,reloadConfig,restartApp,quitApp]);
 useEffect(()=>{const h=e=>{if(!dirty)return;e.preventDefault();e.returnValue=''};window.addEventListener('beforeunload',h);return()=>window.removeEventListener('beforeunload',h)},[dirty]);
 const selectedExit=status.selectedExit||config.defaultExitId||'';
 const exitUnavailable=selectedExitUnavailable({exitsReady,connected:!!status.connected,exits,selected:selectedExit});
 const approval=String(status.approvalState||'').toLowerCase();
 const approvalPending=/pending|wait|approval|待审批/.test(approval);
 const approvalRejected=/reject|denied|revoked|撤销|拒绝/.test(approval);
 const title=useMemo(()=>{for(const g of NAV){const x=g.items.find(i=>i[0]===page);if(x)return x[2]}return page==='settings'?'设置':'RelayProxy'},[page]);
 const p={status,config,exits,setv,save,toast,dirty};
 let content;
 if(page==='overview')content=<Overview {...p} traffic={connections} history={history} onRefresh={refreshStatus} onGoto={requestPage}/>;
 else if(page==='devices')content=<Devices {...p} onRefresh={refreshStatus}/>;
 else if(page==='connection')content=<Connection {...p} onReload={()=>requestLifecycle('reload')}/>;
 else if(page==='exits')content=<Exits status={status} exits={exits} selected={status.selectedExit||config.defaultExitId||''} onSelect={select} onSpeed={id=>{setSpeedExit(id||'');setSpeedOpen(true)}} onSpeedAll={()=>setBatchSpeedOpen(true)} config={config} save={save} toast={toast}/>;
 else if(page==='proxy')content=<ProxyPage {...p} onService={()=>requestPage('settings')} onRefreshCapabilities={refreshNetworkCapabilities}/>;
 else if(page==='exitshare')content=<ExitShare {...p}/>;
 else if(page==='routing')content=<RoutingPage {...p} onGoto={requestPage} onDiscard={async()=>{await refreshConfig();toast('已放弃分流规则草稿')}}/>;
 else if(page==='dns')content=<DNSPage {...p} onGoto={requestPage} onDiscard={async()=>{await refreshConfig();toast('已放弃 DNS 设置草稿')}}/>;
 else if(page==='rdp')content=<RDPPage status={status} config={config} setv={setv} save={save} dirty={dirty} targets={targets} refreshTargets={refreshTargets} refreshStatus={refreshStatus} toast={toast}/>;
 else if(page==='messages')content=<MessagesPage messages={messages} refresh={refreshMessages} clear={clearMessages} toast={toast} config={config} setv={setv} save={save}/>;
 else if(page==='monitor')content=<MonitorPage connections={connections} history={history} refresh={refreshConnections} clear={clearConnections} openNative={()=>call('goOpenConnections')}/>;
 else if(page==='diagnostics')content=<DiagnosticsPage diagnostics={diagnostics} logs={logs} refreshDiagnostics={refreshDiagnostics} clearLogs={clearLogs} copyLogs={copyLogs}/>;
 else content=<SettingsPage config={config} setv={setv} save={save} toast={toast} dirty={dirty} onGoto={requestPage} onConfigRefresh={async()=>{await refreshConfig();await refreshStatus()}} onRefreshCapabilities={refreshNetworkCapabilities} onReload={()=>requestLifecycle('reload')} onRestart={()=>requestLifecycle('restart')} onQuit={()=>requestLifecycle('quit')}/>;
 return <div className="app-shell"><aside className="sidebar"><div className="nav-scroll">{NAV.map(g=><div className="nav-group" key={g.group}><div className="group-name">{g.group}</div>{g.items.map(x=><button key={x[0]} className={cx('nav-item',page===x[0]&&'active')} onClick={()=>requestPage(x[0])}><span className="nav-icon"><Icon name={x[1]}/></span><span>{x[2]}</span>{x[0]==='messages'&&unreadMessages>0&&<i className="nav-dot" title={unreadMessages+' 条新消息'}>{unreadMessages>9?'9+':unreadMessages}</i>}</button>)}</div>)}</div><div className="sidebar-footer"><button className={cx('nav-item',page==='settings'&&'active')} onClick={()=>requestPage('settings')}><span className="nav-icon"><Icon name="settings"/></span><span>设置</span></button><div className="connection-mini"><span className={cx('dot',status.connected?'online':'offline')}/><div><strong>{status.connected?'已连接':'未连接'}</strong><span>{status.connected?((status.latency||0)+' ms'):'—'}</span></div></div></div></aside><section className="workspace"><header className="topbar"><div className="crumb"><b>{title}</b><span>·</span><span>{status.deviceName||config.deviceName||'本设备'}</span></div><div className="top-actions">{dirty&&<Badge tone="blue">未保存</Badge>}{config.restartRequired&&<Badge tone="warn">需重启</Badge>}</div></header><main className="main">{approvalPending&&<div className="notice warn state-banner"><b>设备等待审批。</b> 请在 Relay Server 使用当前身份审批此设备所需能力。</div>}{approvalRejected&&<div className="notice danger state-banner"><b>设备授权不可用。</b> 当前设备可能已被拒绝或撤销，代理、出口与 RDP 能力会受限。</div>}{!loaded&&<div className="notice danger state-banner"><b>配置尚未加载。</b> {configError||'正在连接配置接口…'} <Button quiet onClick={()=>void refreshConfig()}>重试读取</Button></div>}{exitUnavailable&&<div className="notice warn state-banner"><b>已选出口不可用。</b> RelayProxy 不会静默改选其他出口；请在“出口选择”重新选择。 <Button quiet onClick={()=>requestPage('exits')}>重新选择</Button></div>}{content}</main></section><SpeedModal open={speedOpen} exits={exits} initialExit={speedExit} onClose={()=>setSpeedOpen(false)} toast={toast}/><BatchSpeedModal open={batchSpeedOpen} exits={exits} onClose={()=>setBatchSpeedOpen(false)} toast={toast}/><Modal open={!!pendingAction} title="有未保存的修改" onClose={()=>setPendingAction(null)} footer={<><Button onClick={()=>setPendingAction(null)}>继续编辑</Button><Button danger onClick={discardAndContinue}>放弃修改并继续</Button></>}><div className="notice warn">当前页面存在尚未保存的配置草稿。离开、重载、重启或退出会丢失这些修改。</div></Modal><Modal open={confirmQuit} title="退出 RelayProxy" onClose={()=>setConfirmQuit(false)} footer={<><Button onClick={()=>setConfirmQuit(false)}>取消</Button><Button danger onClick={()=>{setConfirmQuit(false);void quitApp()}}>确认退出</Button></>}><div className="notice">退出后将停止当前代理进程和连接。</div></Modal>{toastState&&<div className={cx('toast show',toastState.t==='danger'&&'danger',toastState.t==='warn'&&'warn')}>{toastState.message}</div>}</div>
}
