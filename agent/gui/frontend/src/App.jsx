import DeviceGroups from './DeviceGroups.jsx';
import GatewaySection,{gatewayPatch} from './GatewayPage.jsx';
import {withExitGroups} from './exitInventory.js';
import React, {useCallback,useEffect,useLayoutEffect,useMemo,useRef,useState} from 'react';
import {createPortal} from 'react-dom';
import {call,callJSON,hasBridge,installNativeHooks,parseMutation,saveConfig} from './bridge.js';
import {showWindowsCapture,showWindowsService} from './platformProxy.js';
import {exitInventoryKey as makeExitInventoryKey,exitUsable,selectedExitUnavailable} from './exitInventory.js';
import {connectionPathLabel} from './connectionPathLabel.js';
import {buildDeviceCenter} from './deviceCenter.js';
import {onlyVerifiedFilePeers} from './fileShareVisibility.js';
import {PROTOCOL_CHOICES,dropTargetIndex,moveRule,normalizeRuleLists,protocolChoice,protocolsFromChoice,ruleListText,splitRuleList} from './ruleLines.js';
import {Icon} from './icons.jsx';
import FileSharingPage from './FileSharingPage.jsx';
import useDialogFocus from './useDialogFocus.js';
import {setDoHBlocking,setOtherDNSForwarding} from './dnsSettings.js';
import {currentDNSPreset,applyDNSPreset} from './dnsPresets.js';
import {formatDNSUpstreamLines,parseDNSUpstreamLines} from './dnsUpstreams.js';
import {routingPagePatch,dnsPagePatch} from './configPatches.js';
import {BRUTAL_CALIBRATION_ROUNDS,proposeBrutalFromSpeedTests,validateBrutalCalibrationSample} from './brutalCalibration.js';
import {rdpPathLabel} from './pathStatus.js';
import {DEVICE_SPEED_REVIEW_STEPS,deviceSpeedChecks} from './deviceSpeedChecks.js';

// One flat sidebar, grouped by what the user is trying to do:
// which network to use (出口) → which traffic goes through it (分流 / DNS) →
// how traffic enters RelayProxy (接入) → how this device reaches the Relay
// Server (服务器) → what this device offers others (出口共享) → collaboration
// tools → observation. Every destination is one click away; there is no
// second-level configuration or settings navigation.
const NAV=[
 {group:'工作台',items:[['overview','dashboard','概览','连接状态、流量与快捷操作'],['exits','devices','出口','设备出口、设备分组与自定义代理']]},
 {group:'网络',items:[['routing','split','分流','路由模式、规则与订阅'],['dns','shield','DNS','解析模式、DNS 出口与防泄漏'],['access','swap','接入','本机监听、系统接管与局域网网关'],['server','route','服务器','Relay Server、传输策略与设备身份'],['exitshare','share','出口共享','本机作为其他设备的出口']]},
 {group:'协作',items:[['files','folder','文件共享','共享目录与远程浏览'],['rdp','monitor','远程桌面','连接已授权设备'],['messages','bell','消息','验证码、通知与弹窗偏好']]},
 {group:'观察',items:[['monitor','activity','实时监控','连接、进程、目标与路径'],['diagnostics','terminal','诊断','链路、透明代理、DNS 防泄漏与日志']]}
];
const PAGE_NAMES=Object.fromEntries([...NAV.flatMap(g=>g.items),['settings','settings','设置']].map(x=>[x[0],x[2]]));
const PAGE_DESC=Object.fromEntries([...NAV.flatMap(g=>g.items),['settings','settings','设置','外观、启动与客户端维护']].map(x=>[x[0],x[3]||'']));
const PAGE_IDS=new Set(Object.keys(PAGE_NAMES));
// Pages that were merged keep their old IDs working for bookmarks, stored
// state and native deep links (window.relayNavigate).
const PAGE_ALIASES={devices:'server',connection:'server',proxy:'access',gateway:'access','rdp-settings':'rdp','message-settings':'messages'};
const resolvePage=id=>PAGE_IDS.has(id)?id:PAGE_ALIASES[id]||'';
function initialPage() {
 const requested=resolvePage(new URLSearchParams(window.location.search).get('page'));
 if (requested) return requested;
 return resolvePage(sessionStorage.getItem('relayproxy-react-page'))||'overview';
}

const EMPTY={
 revision:'',serverAddress:'',quicPort:35820,tcpPort:35821,tlsEnabled:true,insecureTls:false,deviceName:'',identityId:'',transport:'auto',
 socks5:{enabled:true,listen:'127.0.0.1',port:1080},http:{enabled:true,listen:'127.0.0.1',port:8080},defaultExitId:'',customExits:[],upstreamExitId:'',
 exitEnabled:true,browserNetworkShare:false,allowInternet:true,allowPrivateNetwork:false,allowLoopback:false,accessMode:'',accessDomains:[],accessCidrs:[],
 exitUpstream:{mode:'',address:'',username:'',password:''},
 rdp:{enabled:true,address:'127.0.0.1:3389'},
 p2p:{enabled:true,mode:'auto',punchTimeoutMs:1200,keepaliveSec:10,idleTimeoutSec:120,maxExitSessions:4,fallback:true,upnpAllowed:false},publicDirectAdvertise:'',
 networkMode:'',network:{mode:'',exclude_processes:[]},systemProxy:{mode:'',autoFollow:true,http:true,socks5:true,proxyIpLiterals:true},systemProxyStatus:{},platform:'',networkCapabilities:{},isAutostart:false,minimizeToTray:true,theme:'system',
 verificationPopupTimeoutSec:15,routing:{mode:'global_proxy',dns_mode:'proxy',auto_detect_dns:false,dns_association_enabled:true,proxy_dns_enabled:false,fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false,device_groups:[],dns_exit_id:'',dns_upstreams:[],doh_blocked_ips:[],default_action:'PROXY',rules:[],subscriptions:[]},configPath:'',version:''
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
// The compact top bar already names the active page. Do not repeat a large
// title and description before its content; actions get their own toolbar.
function PageHead({actions}){
 const [menuOpen,setMenuOpen]=useState(false);
 const [host,setHost]=useState(null);
 const menuRef=useRef(null);
 useLayoutEffect(()=>{
  // The page's first card heading provides an existing row for its actions.
  // No dedicated toolbar is added above the content.
  // Kept-mounted but hidden pages (File Sharing) also contain card headings;
  // only a visible heading of the active page may host the actions.
  const root=document.querySelector('.workspace .main');
  const heading=[...(root?.querySelectorAll('.card-head')||[])].find(el=>el.offsetParent!==null&&!el.closest('.quick-actions')&&!el.querySelector('button,.switch,.badge'))||null;
  const fallback=document.querySelector('.topbar .top-actions');
  setHost(heading||fallback||null);
 },[]);
 const entries=React.Children.toArray(actions?.type===React.Fragment?actions.props.children:actions).filter(Boolean);
 useEffect(()=>{
  if(!menuOpen)return;
  const close=e=>{if(e.key==='Escape'||(e.type==='pointerdown'&&!menuRef.current?.contains(e.target)))setMenuOpen(false)};
  document.addEventListener('pointerdown',close);
  document.addEventListener('keydown',close);
  return()=>{document.removeEventListener('pointerdown',close);document.removeEventListener('keydown',close)};
 },[menuOpen]);
 if(!entries.length)return null;
 // Keep the most important action directly beside the page content.
 // All remaining actions are reachable through a compact overflow menu.
 const primaryIndex=entries.findIndex(item=>item?.props?.primary);
 const chosen=primaryIndex>=0?primaryIndex:entries.length-1;
 const primary=entries[chosen];
 const secondary=entries.filter((_,i)=>i!==chosen);
 const controls=<div className="page-head page-actions-contextual" aria-label="页面操作">
  <div className="page-context-primary">{primary}</div>
  {secondary.length>0&&<div className="page-context-overflow" ref={menuRef}>
   <button type="button" className="btn page-more-trigger" aria-label="更多页面操作" aria-expanded={menuOpen} aria-haspopup="menu" onClick={()=>setMenuOpen(v=>!v)}><span aria-hidden="true">···</span></button>
   {menuOpen&&<div className="page-more-menu" role="group" aria-label="更多页面操作">{secondary.map((action,index)=><div key={action.key??index} onClick={()=>setMenuOpen(false)}>{action}</div>)}</div>}
  </div>}
 </div>;
 return host?createPortal(controls,host):null;
}
function Field({label,help,children,className}){return <label className={cx('field',className)}><span>{label}</span>{children}{help&&<small>{help}</small>}</label>}
const Input=p=><input className="control" {...p}/>;
const Select=p=><select className="control" {...p}/>;
const Textarea=p=><textarea className="control textarea" {...p}/>;
function Setting({title,desc,children}){return <div className="setting"><div className="grow"><b>{title}</b>{desc&&<div className="mini">{desc}</div>}</div>{children}</div>}
const SPARK_POINTS=24;
function Spark({series}){const pts=arr(series).slice(-SPARK_POINTS),max=Math.max(0,...pts);if(!pts.length)return <div className="spark idle" aria-hidden="true"/>;return <div className="spark" title={'最近 '+pts.length+' 次采样'}>{pts.map((v,i)=><i key={i} style={{height:(max>0?Math.max(8,Math.round(v/max*100)):8)+'%'}}/>)}</div>}
function Metric({label,value,hint,series}){return <div className="metric"><div className="eyebrow">{label}</div><div className="metric-value">{value}</div><div className="mini">{hint}</div>{series&&<Spark series={series}/>}</div>}
function SaveBar({dirty,label,onSave,hint,children,disabled}){return <div className={cx('save-bar',dirty&&'dirty')}><div className="grow"><b>{dirty?'存在未保存的修改':'所有修改已保存'}</b>{hint&&<div className="mini">{hint}</div>}</div>{children}<Button primary disabled={disabled} onClick={onSave}>{label}</Button></div>}
function Modal({open,title,children,footer,onClose,wide}){
 const ref=useRef(null);useDialogFocus(open,ref,onClose);
 if(!open)return null;
 return <div className="overlay show" onMouseDown={e=>e.target===e.currentTarget&&onClose?.()}><section ref={ref} tabIndex={-1} role="dialog" aria-modal="true" aria-label={title} className={cx('modal',wide&&'wide')}><div className="modal-head"><h2>{title}</h2><button type="button" className="close" aria-label="关闭对话框" onClick={onClose}>×</button></div><div className="modal-body">{children}</div>{footer&&<div className="modal-foot">{footer}</div>}</section></div>;
}
// Shared primitives for pages that live in their own files (GatewayPage, DeviceGroups),
// so every configuration page keeps one visual language.
export {Badge,Button,Card,Field,Input,Metric,Modal,SaveBar,Select,Setting,Switch,Textarea,exitName};

// exitChoices lists every selectable default exit: authorised devices, device
// groups and enabled custom proxies. "自动选择" is represented by the empty id.
function exitChoices(exits,customExits){
 const devices=[],groups=[];
 for(const x of arr(exits)){(x.kind==='group'?groups:devices).push({id:x.deviceId||x.id,name:x.name||x.deviceId||x.id,online:x.online!==false,kind:x.kind==='group'?'group':'device'})}
 const customs=arr(customExits).filter(x=>x.enabled!==false).map(x=>({id:x.id,name:x.name||x.id,online:true,kind:'custom'}));
 return {devices,groups,customs};
}

// ExitSwitcher is the top-bar pill that shows the current default exit and lets
// the user switch it from any page. Switching calls goSelectExit immediately, so
// the control is disabled while a page holds unsaved drafts.
function ExitSwitcher({exits,customExits,selectedExit,connected,dirty,onSelect}){
 const[open,setOpen]=useState(false),ref=useRef(null);
 useEffect(()=>{if(!open)return;const h=e=>{if(ref.current&&!ref.current.contains(e.target))setOpen(false)};const k=e=>{if(e.key==='Escape')setOpen(false)};document.addEventListener('mousedown',h);document.addEventListener('keydown',k);return()=>{document.removeEventListener('mousedown',h);document.removeEventListener('keydown',k)}},[open]);
 const current=exitName([...arr(exits),...arr(customExits)],selectedExit);
 const {devices,groups,customs}=exitChoices(exits,customExits);
 const pick=id=>{setOpen(false);if(id!==(selectedExit||''))onSelect(id)};
 const item=x=><button type="button" role="menuitemradio" aria-checked={x.id===selectedExit} key={x.kind+':'+x.id} className={cx('exit-switcher-item',x.id===selectedExit&&'active')} disabled={x.kind!=='custom'&&connected&&!x.online} title={x.kind!=='custom'&&connected&&!x.online?'离线':undefined} onClick={()=>pick(x.id)}><span className={cx('dot',x.online?'online':'offline')}/><span className="grow">{x.name}</span>{x.id===selectedExit&&<span className="mini">当前</span>}</button>;
 return <div className="exit-switcher" ref={ref}>
  <button type="button" className={cx('exit-switcher-trigger',open&&'open')} aria-haspopup="menu" aria-expanded={open} aria-label={'默认出口：'+current} disabled={dirty} title={dirty?'存在未保存的修改，请先保存或放弃':'切换默认出口，立即生效'} onClick={()=>setOpen(o=>!o)}><Icon name="globe" size={15}/><span className="exit-switcher-label">出口</span><strong>{current}</strong><span className="exit-switcher-caret" aria-hidden="true">⌄</span></button>
  {open&&<div className="exit-switcher-menu" role="menu" aria-label="选择默认出口">
   <button type="button" role="menuitemradio" aria-checked={!selectedExit} className={cx('exit-switcher-item',!selectedExit&&'active')} onClick={()=>pick('')}><span className="dot online"/><span className="grow">自动选择</span>{!selectedExit&&<span className="mini">当前</span>}</button>
   {devices.length>0&&<div className="exit-switcher-group">设备出口</div>}{devices.map(item)}
   {groups.length>0&&<div className="exit-switcher-group">设备分组</div>}{groups.map(item)}
   {customs.length>0&&<div className="exit-switcher-group">自定义代理</div>}{customs.map(item)}
   {!devices.length&&!groups.length&&!customs.length&&<div className="exit-switcher-empty">暂无可用出口，请先在「出口」页授权设备或添加自定义代理。</div>}
  </div>}
 </div>;
}

const ROUTING_MODES=[['global_proxy','全局代理'],['rule','规则模式'],['direct','全局直连']];

// QuickActions is the overview's one-click control strip: routing mode, default
// exit and the platform's system takeover switch. Every action is applied
// immediately through the bridge, so the strip is locked while drafts exist.
function QuickActions({status,config,exits,dirty,onSave,onSelectExit,onGoto}){
 const routing=config.routing||EMPTY.routing,mode=routing.mode||'rule',isMac=config.platform==='darwin',isWin=config.platform==='windows',isLinux=config.platform==='linux';
 const selectedExit=status.selectedExit||config.defaultExitId||'';
 const {devices,groups,customs}=exitChoices(exits,config.customExits);
 const sys=config.systemProxy||EMPTY.systemProxy,sysStatus=config.systemProxyStatus||{},gw=config.gateway||{},gwStatus=status.gateway||config.gatewayStatus||{};
 const lock=dirty?'存在未保存的修改，请先保存或放弃':undefined;
 return <Card className="quick-actions" title="快捷操作" eyebrow="QUICK ACTIONS" action={dirty?<Badge tone="blue">有未保存修改，已锁定</Badge>:<Badge tone="neutral">即时生效</Badge>}>
  <div className="quick-actions-row">
   <div className="quick-action"><div className="mini">路由模式</div><div className="segment" role="radiogroup" aria-label="路由模式">{ROUTING_MODES.map(([id,label])=><button key={id} type="button" role="radio" aria-checked={mode===id} className={cx('segment-item',mode===id&&'active')} disabled={dirty} title={lock} onClick={()=>mode!==id&&onSave({routing:{mode:id}})}>{label}</button>)}</div></div>
   <div className="quick-action"><div className="mini">默认出口</div><div className="row"><Select aria-label="默认出口" value={selectedExit} disabled={dirty} title={lock} onChange={e=>onSelectExit(e.target.value)}><option value="">自动选择</option>{devices.length>0&&<optgroup label="设备出口">{devices.map(x=><option key={x.id} value={x.id} disabled={status.connected&&!x.online}>{x.name}{status.connected&&!x.online?' · 离线':''}</option>)}</optgroup>}{groups.length>0&&<optgroup label="设备分组">{groups.map(x=><option key={x.id} value={x.id}>{x.name}</option>)}</optgroup>}{customs.length>0&&<optgroup label="自定义代理">{customs.map(x=><option key={x.id} value={x.id}>{x.name}</option>)}</optgroup>}</Select><Button quiet onClick={()=>onGoto('exits')}>管理 ›</Button></div></div>
   <div className="quick-action"><div className="mini">{isMac?'macOS 系统代理':isWin?'Windows 透明代理':isLinux?'局域网网关':'系统接管'}</div>
    {isMac&&<div className="row"><Switch label="macOS 系统代理" checked={sys.mode==='system'} disabled={dirty} onChange={v=>onSave({systemProxy:{mode:v?'system':''}})}/><span className="mini grow">{sysStatus.lastError?'需要处理':sysStatus.applied?'已接管系统代理':sys.mode==='system'?'已启用，等待应用':'未接管'}</span><Button quiet onClick={()=>onGoto('access')}>设置 ›</Button></div>}
    {isWin&&<div className="row"><Badge tone={config.runtime?.network?.mode==='divert'||config.networkMode==='divert'?'ok':'neutral'}>{config.runtime?.network?.mode==='divert'?'接管中':config.networkMode==='divert'?'已启用 · 待重启':'未启用'}</Badge><span className="mini grow">按进程接管本机 TCP / UDP，开关需重启生效</span><Button quiet onClick={()=>onGoto('access')}>设置 ›</Button></div>}
    {isLinux&&<div className="row"><Badge tone={gwStatus.running?'ok':gw.enabled?'warn':'neutral'}>{gwStatus.running?'运行中':gw.enabled?'已启用 · 未运行':'未启用'}</Badge><span className="mini grow">LAN 设备经本机透明接入</span><Button quiet onClick={()=>onGoto('access')}>设置 ›</Button></div>}
    {!isMac&&!isWin&&!isLinux&&<div className="row"><span className="mini grow">本机 SOCKS5 / HTTP 代理接入</span><Button quiet onClick={()=>onGoto('access')}>设置 ›</Button></div>}
   </div>
  </div>
 </Card>;
}

function Overview({status,config,exits,traffic,history,dirty,onRefresh,onGoto,onSave,onSelectExit}){
 const connected=!!status.connected;
 const approval=String(status.approvalState||''),approvalLower=approval.toLowerCase();
 const approvalLabel=connected?(approval||'已连接'):(/pending|wait|approval|待审批/.test(approvalLower)||/reject|denied|revoked|撤销|拒绝/.test(approvalLower)?approval:'未连接');
 return <><PageHead actions={<Button onClick={onRefresh}>刷新</Button>}/>
 <Card className="hero overview-hero">
  <div className="hero-glow"/>
  <div className="hero-status"><span className={cx('dot',connected?'online':'offline')}/><div className="grow"><div className="eyebrow">{connected?'CONNECTED':'DISCONNECTED'}</div><div className="big">{connected?'RelayProxy 已连接':'等待连接到 RelayProxy Server'}</div><div className="muted">{((connected?(config.runtime?.serverAddress||config.serverAddress):config.serverAddress)||'尚未配置服务器')+' · '+(status.transport||config.runtime?.transport||config.transport||'auto')}{connected&&config.runtime?.serverAddress&&config.runtime.serverAddress!==config.serverAddress?' · 已保存新 Server，待重启':''}</div></div><Badge tone={connected?'ok':/reject|denied|revoked|撤销|拒绝/i.test(approvalLabel)?'danger':'neutral'}>{approvalLabel}</Badge></div>
  <div className="overview-current-exit"><div className="grow"><div className="mini">{status.selectedExit?'当前运行出口':'默认代理出口配置'}</div><strong>{exitName([...exits,...arr(config.customExits)],status.selectedExit||config.defaultExitId)}</strong></div><Button quiet onClick={()=>onGoto('server')}>服务器设置 ›</Button></div>
 </Card>
 <QuickActions status={status} config={config} exits={exits} dirty={dirty} onSave={onSave} onSelectExit={onSelectExit} onGoto={onGoto}/>
 <div className="cards-4"><Metric label="下载速度" value={fmtBytes(traffic?.download_rate||0,true)} hint={fmtBytes(traffic?.download||0)+' 累计'} series={history.down}/><Metric label="上传速度" value={fmtBytes(traffic?.upload_rate||0,true)} hint={fmtBytes(traffic?.upload||0)+' 累计'} series={history.up}/><Metric label="往返延迟" value={connected?((status.latency||0)+' ms'):'—'} hint="Server RTT" series={history.latency}/><Metric label="活跃连接" value={traffic?.active??status.activeStreams??0} hint={(traffic?.total||0)+' 累计连接'} series={history.active}/></div>
 <Card title="最近连接" eyebrow="LIVE TRAFFIC" action={<Button quiet onClick={()=>onGoto('monitor')}>实时监控 ›</Button>}><div className="recent-list">{arr(traffic?.connections).slice(0,3).map((x,i)=><div className="recent-row" key={x.id||i}><div className="app-mark">{String(x.process_name||x.process||'?').slice(0,1).toUpperCase()}</div><div className="grow"><strong>{x.process_name||x.process||'未知进程'}</strong><div className="mono mini">{(x.host||x.ip||'—')+(x.port?':'+x.port:'')}</div></div><div className="recent-rate"><b>{fmtBytes(x.download_rate||0,true)} ↓</b><span>{fmtBytes(x.upload_rate||0,true)} ↑</span></div><Badge tone={tone(x.state)}>{connectionPathLabel(x,[...exits,...arr(config.customExits)])||x.state||'—'}</Badge></div>)}{!arr(traffic?.connections).length&&<div className="empty compact-empty">暂无连接记录。</div>}</div></Card></>
}

function ServerPage({status,config,setv,save,dirty,toast,onRefresh,onReload}){
 const p=config.p2p||EMPTY.p2p,[tlsRisk,setTlsRisk]=useState(false),rt=config.runtime||{};
 const copy=async(text,label)=>{if(!text)return;if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',String(text));else await navigator.clipboard.writeText(String(text));toast('已复制'+label)};
 const capabilities=arr(status.approvedCapabilities),approval=String(status.approvalState||'').toLowerCase(),grantsActive=!!status.connected&&approval==='approved';
 const capMeta={
  'proxy.client':['代理客户端','使用 Server 授权的出口代理本机流量'],
  'proxy.exit':['网络出口','承载其他已授权设备的代理流量'],
  'rdp.controller':['RDP 控制端','连接 Server 授权的远程桌面设备'],
  'rdp.host':['RDP 主机','允许已授权控制端连接本机 RDP'],
  'rdp.public':['RDP 公网入口','使用 Server 公网 RDP ingress 能力']
 };
 const runtimeServer=[rt.serverAddress||config.serverAddress,rt.quicPort||config.quicPort,rt.tcpPort||config.tcpPort].join(' · ');
 const savedServer=[config.serverAddress,config.quicPort,config.tcpPort].join(' · ');
 const serverPending=!!config.restartRequired&&(rt.serverAddress!==undefined&&(rt.serverAddress!==config.serverAddress||Number(rt.quicPort)!==Number(config.quicPort)||Number(rt.tcpPort)!==Number(config.tcpPort)||rt.transport!==config.transport||!!rt.tlsEnabled!==!!config.tlsEnabled||!!rt.insecureTls!==!!config.insecureTls));
 return <><PageHead title="服务器与身份" desc="本机如何连接 Relay Server：地址、传输、直连策略，以及 Server 审批的设备身份。" actions={<><Button onClick={onRefresh}>刷新状态</Button><Button onClick={onReload}>重载配置</Button></>}/>
 {serverPending&&<div className="notice warn"><b>连接启动参数已保存，当前进程仍使用旧值。</b><br/>运行中：<span className="mono">{runtimeServer}</span> · {rt.transport||'auto'}<br/>重启后：<span className="mono">{savedServer}</span> · {config.transport||'auto'}</div>}
 <Card title="Relay Server" eyebrow="SERVER"><div className="form-grid"><Field label="Server 地址" className="full"><Input value={config.serverAddress||''} onChange={e=>setv('serverAddress',e.target.value)} placeholder="relay.example.com"/></Field><Field label="QUIC / UDP 端口"><Input type="number" value={config.quicPort||35820} onChange={e=>setv('quicPort',Number(e.target.value))}/></Field><Field label="TCP / TLS 端口"><Input type="number" value={config.tcpPort||35821} onChange={e=>setv('tcpPort',Number(e.target.value))}/></Field><Field label="TLS"><Select value={config.tlsEnabled===false?'off':'on'} onChange={e=>{const enabled=e.target.value==='on';setv('tlsEnabled',enabled);if(!enabled){setv('insecureTls',false);setv('transport','tcp_only')}}}><option value="on">启用 TLS</option><option value="off">关闭 TLS</option></Select></Field><Field label="传输策略"><Select value={config.transport||'auto'} onChange={e=>setv('transport',e.target.value)}><option value="auto">自动 · 优先 QUIC，必要时回落 TCP</option><option value="quic_only" disabled={config.tlsEnabled===false}>仅 QUIC</option><option value="tcp_only">仅 TCP · 固定使用 TLS/TCP</option></Select></Field></div><Setting title="允许自签名 / 不受信任证书" desc="仅用于可信开发环境；生产环境应保持关闭。"><Switch checked={!!config.insecureTls} disabled={config.tlsEnabled===false} onChange={v=>{if(v)setTlsRisk(true);else setv('insecureTls',false)}}/></Setting></Card>
 <div className="cards-2">
  <Card title="设备身份" eyebrow="DEVICE"><div className="identity-tile"><div className="identity-icon">▱</div><div className="grow"><div className="big small-big">{status.deviceName||config.deviceName||'RelayProxy Device'}</div><div className="mono muted">{status.deviceId||'等待 Server 分配设备 ID'}</div></div><Badge tone={tone(status.approvalState)}>{status.approvalState||'待连接'}</Badge></div>{status.deviceId&&<div className="actions end top-gap"><Button quiet onClick={()=>copy(status.deviceId,'设备 ID')}>复制设备 ID</Button></div>}<div className="divider"/><div className="form-grid"><Field label="设备名称"><Input value={config.deviceName||''} onChange={e=>setv('deviceName',e.target.value)}/></Field><Field label="身份 ID" help="由 Server 创建的身份 ID。"><div className="row"><Input value={config.identityId||''} onChange={e=>setv('identityId',e.target.value)}/><Button onClick={()=>copy(config.identityId,' Identity ID')}>复制</Button></div></Field></div><div className="divider"/><Setting title={grantsActive?'身份名称':'上次批准身份'} desc={grantsActive?'当前身份隔离范围':'缓存自上一次成功审批'}>{status.identityName||'—'}</Setting><Setting title={grantsActive?'策略版本':'上次策略版本'} desc="Server 下发的权限策略">{status.policyRevision||'—'}</Setting><Setting title={grantsActive?'当前模式':'上次批准模式'} desc="proxy.client / proxy.exit 聚合结果"><Badge tone={grantsActive?'blue':'neutral'}>{status.mode||'—'}</Badge></Setting></Card>
  <Card title={grantsActive?'当前设备能力':'设备能力'} eyebrow="SERVER GRANTS">{!grantsActive&&capabilities.length>0&&<div className="notice warn">当前没有已认证的 Relay 会话。下面仅显示上一次成功审批的能力，用于排障；在重新连接并完成审批前不视为有效授权。</div>}<div className="capability-list">{capabilities.map(cap=>{const meta=capMeta[cap]||[cap,'Server 批准的设备能力'];return <div className="capability-row" key={cap}><div className="grow"><b>{meta[0]}</b><div className="mini">{meta[1]}</div><div className="mono mini">{cap}</div></div><Badge tone={grantsActive?'ok':'neutral'}>{grantsActive?'允许':'上次批准'}</Badge></div>})}{grantsActive&&<div className="capability-row"><div className="grow"><b>消息接收</b><div className="mini">所有已审批 Agent 都能接收发往本设备的消息；渠道仍按身份隔离。</div><div className="mono mini">messages · approved-device</div></div><Badge tone="ok">启用</Badge></div>}{!capabilities.length&&!grantsActive&&<div className="empty compact-empty">等待设备连接并审批后显示 Server 批准能力。</div>}</div><div className="notice top-gap">设备首次连接后需要在 Server 管理端审批。出口、RDP、消息渠道等能力均按身份与设备授权隔离，客户端不会展示未授权资源。</div></Card>
 </div>
 <Card title="Direct / P2P" eyebrow="PATH POLICY"><Setting title="启用 P2P 直连" desc="首个请求可先走 Relay，后台建立可复用直连。"><Switch checked={p.enabled!==false} onChange={v=>setv('p2p.enabled',v)}/></Setting><Setting title="本机允许 UPnP 映射" desc={'默认关闭。只有本机和 Server 同时允许才会向路由器申请 UDP 端口，保存后重启生效。当前：'+(status.upnpState||'DISABLED')+(status.upnpAddress?' · '+status.upnpAddress:'')+(status.upnpError?' · '+status.upnpError:'')}><Switch checked={p.upnpAllowed===true} onChange={v=>setv('p2p.upnpAllowed',v)}/></Setting><div className="form-grid top-gap"><Field label="路径模式"><Select value={p.mode||'auto'} onChange={e=>setv('p2p.mode',e.target.value)}><option value="auto">auto · 自动</option><option value="direct_only">direct_only · 仅直连（Public Direct / P2P）</option><option value="p2p_only">p2p_only · 仅 P2P</option><option value="relay_only">relay_only · 仅 Relay</option></Select></Field><Field label="失败回落" help={p.mode==='auto'?'仅自动模式使用；直连失败后回落 Relay。':'当前路径模式固定，不使用 Relay 回落开关。'}><Select disabled={p.mode!=='auto'} value={p.fallback===false?'off':'on'} onChange={e=>setv('p2p.fallback',e.target.value==='on')}><option value="on">允许回落 Relay</option><option value="off">不允许回落</option></Select></Field><Field label="打洞超时 (ms)"><Input type="number" value={p.punchTimeoutMs??1200} onChange={e=>setv('p2p.punchTimeoutMs',Number(e.target.value))}/></Field><Field label="Keepalive (s)"><Input type="number" value={p.keepaliveSec??10} onChange={e=>setv('p2p.keepaliveSec',Number(e.target.value))}/></Field><Field label="空闲超时 (s)"><Input type="number" value={p.idleTimeoutSec??120} onChange={e=>setv('p2p.idleTimeoutSec',Number(e.target.value))}/></Field><Field label="最大出口会话"><Input type="number" value={p.maxExitSessions??4} onChange={e=>setv('p2p.maxExitSessions',Number(e.target.value))}/></Field><Field label="手动公网地址（可选）" className="full" help="用于端口转发 / 云主机 DNS 等场景，Server 会主动验证。"><Input value={config.publicDirectAdvertise||''} onChange={e=>setv('publicDirectAdvertise',e.target.value)} placeholder="edge.example.com:35820"/></Field></div>{(status.directError||status.p2pError)&&<div className="notice danger top-gap">{status.directError||status.p2pError}</div>}<div className="mini top-gap">实时路径（Relay / Public Direct / P2P）与 QUIC 质量请在「诊断」页查看。</div></Card>
 <SaveBar dirty={dirty} label="保存服务器与身份" hint="Server 地址、端口、TLS、传输、Direct / P2P 与设备身份均为启动参数，保存后需重启客户端生效。" onSave={()=>save({server:{address:config.serverAddress,quicPort:Number(config.quicPort),tcpPort:Number(config.tcpPort),tlsEnabled:config.tlsEnabled,insecureTls:!!config.insecureTls},transport:config.transport,p2p:config.p2p,direct:{public:{advertise:config.publicDirectAdvertise||''}},device:{name:config.deviceName,identityId:config.identityId}})}/>
 <Modal open={tlsRisk} title="允许不受信任的 TLS 证书？" onClose={()=>setTlsRisk(false)} footer={<><Button onClick={()=>setTlsRisk(false)}>保持安全设置</Button><Button danger onClick={()=>{setv('insecureTls',true);setTlsRisk(false)}}>我了解风险，继续</Button></>}><div className="notice danger"><b>此设置会跳过服务端证书验证。</b><br/>仅在你完全信任的自建测试环境中使用。生产环境应使用受信任证书并保持关闭。</div></Modal></>
}

function CustomExitManager({config,save,toast,dirty,selectedExit,onSelectExit}){
 const items=arr(config.customExits);
 const [draft,setDraft]=useState(null),[busy,setBusy]=useState(false);
 const refs=id=>[
  config.defaultExitId===id&&'默认出口',
  config.upstreamExitId===id&&'本机出口共享',
  config.routing?.dns_exit_id===id&&'DNS 专用出口',
  ...arr(config.routing?.rules).filter(r=>r.exit_id===id).map(r=>'分流规则：'+(r.name||'未命名'))
 ].filter(Boolean);
 const update=(key,val)=>setDraft(x=>({...x,[key]:val}));
 const persist=async next=>{if(dirty){toast('请先保存代理配置或放弃未保存的修改，再调整自定义出口。','warn');return false}setBusy(true);try{return await save({proxy:{customExits:next}})}finally{setBusy(false)}};
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
  <Card title="自定义代理" eyebrow={items.length+' LOCAL PROXIES'} action={<Button primary onClick={()=>edit(null)}>＋ 添加代理</Button>}>
   <div className="mini">本机可达的 SOCKS5 / HTTP CONNECT 代理，不依赖 Server 授权。可设为默认出口、分流规则目标和本机出口共享上游。「测试」会通过代理建立 TCP CONNECT；SOCKS5 还会经该代理向 Quad9 (9.9.9.9) 发送一次 example.com 的 UDP DNS 查询。</div>
   {!items.length&&<div className="empty">尚未创建自定义代理。点击「添加代理」创建 SOCKS5 或 HTTP 代理。</div>}
   <div className="target-list top-gap">
    {items.map(item=><div className="target-row" key={item.id}>
     <div className="identity-icon small">↗</div>
     <div className="grow">
      <div className="row"><strong>{item.name}</strong><Badge tone={item.enabled?'ok':'neutral'}>{item.enabled?'已启用':'已停用'}</Badge>{selectedExit===item.id&&<Badge tone="blue">默认出口</Badge>}{config.upstreamExitId===item.id&&<Badge tone="blue">共享上游</Badge>}</div>
      <div className="mono mini">{item.protocol.toUpperCase()} · {item.address} · {item.protocol==='socks5'?'支持 UDP ASSOCIATE':'仅 TCP'}</div>
     </div>
     {onSelectExit&&selectedExit!==item.id&&<Button quiet disabled={!item.enabled||busy} title="将此代理设为默认出口，立即生效" onClick={()=>onSelectExit(item.id)}>设为默认</Button>}
     <Button quiet disabled={!item.enabled||busy} onClick={()=>test(item)}>测试</Button>
     <Button quiet onClick={()=>edit(item)}>编辑</Button>
     <Switch checked={item.enabled} disabled={busy} label={'启停 '+item.name} onChange={()=>toggle(item)}/>
     <Button quiet danger disabled={busy} onClick={()=>remove(item)}>删除</Button>
    </div>)}
   </div>
  </Card>
  <Modal open={!!draft} title={items.some(x=>x.id===draft?.id)?'编辑自定义代理':'新增自定义代理'} onClose={()=>setDraft(null)} footer={<><Button onClick={()=>setDraft(null)}>取消</Button><Button primary disabled={busy} onClick={saveDraft}>{busy?'保存中…':'保存代理'}</Button></>}>
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

// 出口 page: everything about "whose network do I use" in one place —
// device exits, device groups and local custom proxies, each able to become
// the default exit with one click.
const EXIT_TABS=[['devices','设备出口'],['groups','设备分组'],['custom','自定义代理']];
function ExitsPage({status,config,exits,targets,filePeers,setv,save,dirty,toast,selectedExit,onSelectExit,onSpeed,onSpeedAll,onConnectRDP,onFiles,onRefresh}){
 const [tab,setTab]=useState('devices');
 const groups=arr(config.routing?.device_groups),customs=arr(config.customExits);
 const counts={devices:exits.length,groups:groups.length,custom:customs.length};
 const current=exitName([...withExitGroups(exits,groups),...customs],selectedExit);
 return <><PageHead actions={<><Button onClick={onRefresh}>刷新</Button>{tab==='devices'&&<Button disabled={!status.connected||!exits.some(x=>x.online!==false)} onClick={onSpeedAll}>批量测速</Button>}</>}/>
 <div className="exits-toolbar">
  <div className="segment" role="tablist" aria-label="出口类型">{EXIT_TABS.map(([id,label])=><button key={id} type="button" role="tab" aria-selected={tab===id} className={tab===id?'active':''} onClick={()=>setTab(id)}>{label}<span className="segment-count">{counts[id]}</span></button>)}</div>
  <div className="exits-current"><span className="mini">默认出口</span><strong>{current}</strong>{selectedExit&&<Button quiet disabled={dirty} title={dirty?'请先保存或放弃当前修改':'改为自动选择'} onClick={()=>onSelectExit('')}>改为自动</Button>}</div>
 </div>
 {dirty&&<div className="notice warn">存在未保存的修改；切换默认出口会立即生效并重新读取配置，请先保存或放弃修改。</div>}
 {tab==='devices'&&<DeviceCenter status={status} exits={exits} targets={targets} filePeers={filePeers} config={config} selectedExit={selectedExit} onSelectExit={dirty?null:onSelectExit} onSpeed={onSpeed} onConnectRDP={onConnectRDP} onFiles={onFiles}/>}
 {tab==='groups'&&<DeviceGroups config={config} exits={exits} setv={setv} save={save} dirty={dirty} toast={toast} selectedExit={selectedExit} onSelectExit={dirty?null:onSelectExit}/>}
 {tab==='custom'&&<CustomExitManager config={config} save={save} toast={toast} dirty={dirty} selectedExit={selectedExit} onSelectExit={dirty?null:onSelectExit}/>}
 </>;
}

function DeviceCenter({status,exits,targets,filePeers,config,selectedExit,onSelectExit,onSpeed,onConnectRDP,onFiles}){
 const [query,setQuery]=useState(''),[detail,setDetail]=useState('');
 const devices=useMemo(()=>buildDeviceCenter({proxyExits:exits,rdpTargets:targets,filePeers}),[exits,targets,filePeers]);
 const filtered=devices.filter(d=>!query.trim()||[d.name,d.id].some(x=>String(x).toLowerCase().includes(query.trim().toLowerCase())));
 const selected=devices.find(d=>d.id===detail);
 const proxyMeta=d=>{
  const publicPath=d.proxy?.direct?.public||{};
  const verified=arr(publicPath.endpoints).filter(x=>x.verified);
  return {publicPath,verified};
 };
 const rdpEnabled=config.rdp?.enabled!==false&&config.runtime?.rdp?.enabled!==false&&arr(status.approvedCapabilities).includes('rdp.controller');
 const activeRDPTargets=new Set(arr(status.rdpSessions).map(s=>s.deviceId).filter(Boolean));
 if(!activeRDPTargets.size&&status.rdpTargetId)activeRDPTargets.add(status.rdpTargetId);
 return <>
 <div className="device-center-intro">
  <div><strong>已授权设备</strong><span className="mini">Server 授权给本机的远程设备；提供代理出口的设备可直接设为默认出口。</span></div>
  <div className="device-center-filter"><Input aria-label="搜索设备名称或 ID" placeholder="搜索设备名称 / ID" value={query} onChange={e=>setQuery(e.target.value)}/><span className="mini">{filtered.length} 台</span></div>
 </div>
 <div className="device-center-grid">
  {filtered.map(d=>{
   const meta=proxyMeta(d),name=d.name||d.id,isDefault=selectedExit===d.id;
   const online=status.connected&&d.online;
   return <article className={cx('device-center-card',isDefault&&'is-default')} key={d.id} onClick={e=>{if(!e.target.closest('button'))setDetail(d.id)}}>
    <div className="device-center-card-head">
     <div className="identity-icon"><Icon name="devices" size={21}/></div>
     <button className="device-center-card-name" type="button" onClick={()=>setDetail(d.id)} title={'查看 '+name+' 的设备详情'}>
      <strong>{name}</strong><span className="mini mono">{d.id}</span>
     </button>
     {isDefault&&<Badge tone="blue">默认出口</Badge>}
     <Badge tone={online?'ok':'neutral'}>{online?'在线':status.connected?'离线':'未连接'}</Badge>
    </div>
    <div className="device-center-caps">
     {d.proxy&&<Badge tone="blue"><Icon name="globe" size={12}/> 代理出口</Badge>}
     {d.shares.length>0&&<Badge tone="blue"><Icon name="folder" size={12}/> {d.shares.length} 个共享目录</Badge>}
     {d.rdp&&<Badge tone="blue"><Icon name="monitor" size={12}/> 远程桌面</Badge>}
     {meta.publicPath?.available&&<Badge tone="ok">公网直连候选</Badge>}
    </div>
    <div className="device-center-card-actions" aria-label={name+' 快捷操作'}>
     {d.proxy&&!isDefault&&<button type="button" className="device-quick-action" disabled={!onSelectExit||!online||d.proxy.online===false} title={!onSelectExit?'请先保存或放弃当前修改':online?'将此设备设为默认代理出口，立即生效':'设备离线或未连接'} aria-label={'设为默认出口 '+name} onClick={()=>onSelectExit(d.id)}><Icon name="globe" size={18}/><span>设为默认</span></button>}
     {d.rdp&&<button type="button" className="device-quick-action" disabled={!online||d.rdp.online===false||!rdpEnabled} title={!rdpEnabled?'请在「远程桌面」页底部的偏好中启用，并确认控制端授权':online?activeRDPTargets.has(d.id)?'复用现有 RDP 入口，再次打开本地远程桌面':'连接此设备':'设备离线'} aria-label={'远程连接 '+name} onClick={()=>onConnectRDP(d.id)}><Icon name="monitor" size={18}/><span>{activeRDPTargets.has(d.id)?'再次打开':'远程桌面'}</span></button>}
     {d.proxy&&<button type="button" className="device-quick-action" disabled={!online||d.proxy.online===false} title={online?'测试此设备代理出口':'设备离线或未连接'} aria-label={'测速 '+name} onClick={()=>onSpeed(d.id)}><Icon name="activity" size={18}/><span>测速</span></button>}
     {d.shares.length>0&&onFiles&&<button type="button" className="device-quick-action" disabled={!online} title={online?'浏览此设备的共享目录':'设备离线或未连接'} aria-label={'浏览文件 '+name} onClick={()=>onFiles(d.id)}><Icon name="folder" size={18}/><span>浏览文件</span></button>}
     
    </div>
   </article>;
  })}
 </div>
 {!filtered.length&&<Card><div className="empty">{devices.length?'没有符合搜索条件的设备。':'暂无已授权的远程设备。请检查 Server 授权、RDP 权限或文件目录 ACL。'}</div></Card>}
 <Modal open={!!selected} title={selected?.name||'设备详情'} onClose={()=>setDetail('')} footer={<Button onClick={()=>setDetail('')}>关闭</Button>}>
  {selected&&<div className="stack">
   <Setting title="设备 ID"><span className="mono mini">{selected.id}</span></Setting>
   <Setting title="连接状态"><Badge tone={status.connected&&selected.online?'ok':'neutral'}>{status.connected&&selected.online?'在线':'离线 / 未连接'}</Badge></Setting>
   <Setting title="代理出口">{selected.proxy?'已授权':'未提供'}</Setting>
   <Setting title="远程桌面">{selected.rdp?'目标已授权':'未提供'}</Setting>
   <Setting title="共享目录">{selected.shares.length?selected.shares.length+' 个已验证的授权目录':'未提供'}</Setting>
   {selected.proxy&&<div className="notice">{proxyMeta(selected).publicPath?.available?'此设备提供公网直连候选，但实际路径取决于连接时协商。':'当前没有可确认的公网直连端点。'}{proxyMeta(selected).verified.length?' · '+proxyMeta(selected).verified.length+' 个已验证端点':''}。仅显示本设备报告的候选能力，不套用其他出口的运行态路径。</div>}
   {selected.proxy&&<div className="actions top-gap"><Button disabled={!status.connected||selected.proxy.online===false} onClick={()=>{setDetail('');onSpeed(selected.id)}}>测试此设备出口 ›</Button></div>}
   {!!selected.shares.length&&<div className="device-center-share-list">{selected.shares.map(s=><div key={s.id} className="device-center-share"><Icon name="folder" size={15}/><span className="grow">{s.name||s.id}</span><Badge>{s.permission==='read_write'?'读写':'只读'}</Badge></div>)}</div>}
  </div>}
 </Modal>
 </>;
}

// 接入 page: every way traffic enters RelayProxy on this machine — local
// SOCKS5/HTTP listeners, OS-level takeover (Windows transparent proxy or
// macOS system proxy) and the Linux LAN gateway. Platform-specific cards only
// appear on the platform they apply to; everything saves through one SaveBar.
function AccessPage({status,config,exits,setv,save,dirty,onRefreshCapabilities,onConfigRefresh,toast}){
 const sys=config.systemProxy||EMPTY.systemProxy,sysStatus=config.systemProxyStatus||{},isMac=config.platform==='darwin',isLinux=config.platform==='linux';
 const s=config.socks5||EMPTY.socks5,h=config.http||EMPTY.http,rs=config.runtime?.socks5||s,rh=config.runtime?.http||h,caps=config.networkCapabilities||{},[service,setService]=useState(null),[busy,setBusy]=useState('');
 const networkServiceBridgeAvailable=hasBridge('goGetNetworkServiceStatus');
 const windowsCaptureAvailable=showWindowsCapture(config.platform,networkServiceBridgeAvailable);
 const refreshService=useCallback(async()=>{if(!networkServiceBridgeAvailable)return null;try{const x=await callJSON('goGetNetworkServiceStatus',{});setService(x||{});return x}catch{return null}},[networkServiceBridgeAvailable]);
 useEffect(()=>{onRefreshCapabilities?.();refreshService()},[onRefreshCapabilities,refreshService]);
 const interceptReady=!!(caps.tcp&&caps.udp&&caps.ipv6&&caps.process_identity&&caps.original_destination&&caps.reply_injection&&caps.loop_bypass&&!caps.unavailable_reason);
 const canToggleIntercept=interceptReady||config.networkMode==='divert';
 const socksAddr=(s.listen||'127.0.0.1')+':'+(s.port||1080),httpAddr=(h.listen||'127.0.0.1')+':'+(h.port||8080),runtimeSocksAddr=(rs.listen||'127.0.0.1')+':'+(rs.port||1080),runtimeHttpAddr=(rh.listen||'127.0.0.1')+':'+(rh.port||8080);
 const socksURI='socks5h://'+socksAddr,httpURI='http://'+httpAddr,runtimeSocksURI='socks5h://'+runtimeSocksAddr,runtimeHttpURI='http://'+runtimeHttpAddr,socksPending=socksAddr!==runtimeSocksAddr||!!s.enabled!==!!rs.enabled,httpPending=httpAddr!==runtimeHttpAddr||!!h.enabled!==!!rh.enabled;
 const exposed=v=>{const x=String(v||'').trim().toLowerCase();return x==='0.0.0.0'||x==='::'||x==='[::]'||x==='*'};
 const serviceAct=async(name,msg)=>{
  if(name==='goUninstallNetworkService'){if(dirty){toast('当前有未保存修改，请先保存或放弃后再卸载 Network Service','danger');return}if(!window.confirm('卸载 RelayProxy Windows 网络服务？此操作会同时关闭已保存的系统透明代理配置。'))return}
  setBusy(name);
  try{const r=parseMutation(await call(name));if(!r.ok&&r.message)throw new Error(r.message);if(name==='goUninstallNetworkService')await onConfigRefresh?.();await Promise.all([refreshService(),onRefreshCapabilities?.()]);toast(r.message||msg)}
  catch(e){toast(e.message,'danger')}finally{setBusy('')}
 };
 const copy=async(value,label)=>{if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',value);else await navigator.clipboard.writeText(value);toast('已复制 '+label)};
 const saveAll=()=>save({
  systemProxy:isMac?{mode:sys.mode||'',autoFollow:sys.autoFollow!==false,http:sys.http!==false,socks5:sys.socks5!==false,proxyIpLiterals:sys.proxyIpLiterals!==false}:undefined,
  proxy:{socks5Enabled:s.enabled,socks5Listen:s.listen,socks5Port:Number(s.port),httpEnabled:h.enabled,httpListen:h.listen,httpPort:Number(h.port)},
  network:{mode:config.networkMode||'',excludeProcesses:splitRuleList(config.network?.exclude_processes)},
  gateway:isLinux?gatewayPatch(config.gateway||{}):undefined
 });
 return <><PageHead title="接入" desc={'本机流量如何进入 RelayProxy：'+(isMac?'应用通过本地代理或 macOS 系统代理接入。':windowsCaptureAvailable?'应用通过本地代理接入，或由 Windows 透明代理按进程接管。':isLinux?'应用通过本地代理接入；LAN 设备可经局域网网关透明接入。':'应用通过本地 SOCKS5 / HTTP 代理接入。')}/>
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
   {exposed(h.listen)&&<div className="notice warn top-gap">HTTP Proxy 正绑定所有网卡，局域网设备可能访问此代理。请确认系统防火墙与网络环境可信。</div>}
   <div className="setting top-gap"><div className="grow"><b>{status.httpRunning?'当前运行地址':'保存后地址'}</b><div className="mono mini">{status.httpRunning?runtimeHttpURI:httpURI}</div></div><Button quiet onClick={()=>copy(status.httpRunning?runtimeHttpURI:httpURI,'HTTP Proxy 地址')}>复制</Button></div>{httpPending&&<div className="notice warn top-gap">保存配置为 <span className="mono">{httpURI}</span>，当前进程仍使用 <span className="mono">{runtimeHttpURI}</span>；重启后切换。</div>}
  </Card>
 </div>
 {isMac&&<Card title="macOS 系统代理" eyebrow="SYSTEM TAKEOVER" action={<Badge tone={sysStatus.lastError?'warn':sysStatus.applied?'ok':'neutral'}>{sysStatus.lastError?'需要处理':sysStatus.applied?'已接管':'未接管'}</Badge>}>
   <Setting title="接管模式" desc="通过系统 HTTP / HTTPS / SOCKS 代理将兼容应用流量交给 RelayProxy；无需 Apple Developer 签名。">
    <Select value={sys.mode||''} onChange={e=>{setv('systemProxy.mode',e.target.value);if(e.target.value==='system'){setv('networkMode','');setv('network.mode','')}}}><option value="">关闭系统代理</option><option value="system">系统代理（推荐）</option></Select>
   </Setting>
   <Setting title="自动跟随活动网络" desc="Wi-Fi / 有线网络切换时恢复旧服务，并配置新的活动服务。"><Switch checked={sys.autoFollow!==false} onChange={v=>setv('systemProxy.autoFollow',v)}/></Setting>
   <Setting title="HTTP / HTTPS" desc={httpAddr}><Switch checked={sys.http!==false} onChange={v=>setv('systemProxy.http',v)}/></Setting>
   <Setting title="SOCKS5" desc={socksAddr}><Switch checked={sys.socks5!==false} onChange={v=>setv('systemProxy.socks5',v)}/></Setting>
   <Setting title="代理 IP 地址目标" desc="临时移除系统绕过列表中的 IP / CIDR 例外，保护 localhost 和链路本地地址；关闭或退出时恢复原列表。"><Switch checked={sys.proxyIpLiterals!==false} onChange={v=>setv('systemProxy.proxyIpLiterals',v)}/></Setting>
   <div className="mini top-gap">启用前持久保存原系统代理；正常退出、关闭或网络切换时仅恢复 RelayProxy 设置的值。意外退出后下一次启动尝试恢复。可能提示 macOS 管理员授权。系统代理不会捕获所有应用的 UDP / QUIC 流量；已有认证代理、PAC 或自动代理发现配置不会被静默覆盖。</div>
   {sysStatus.service&&<div className="setting top-gap"><div className="grow"><b>当前网络服务</b><span className="mono mini">{sysStatus.service}</span></div></div>}
   {sysStatus.pendingRecovery>0&&<div className="notice warn top-gap">尚有 {sysStatus.pendingRecovery} 项代理设置等待安全恢复。</div>}
   {sysStatus.lastError&&<div className="notice warn top-gap">{sysStatus.lastError}</div>}
  </Card>}
 {windowsCaptureAvailable&&<Card title="Windows 透明代理" eyebrow="SYSTEM TAKEOVER" action={<Badge tone={service?.ready?'ok':service?.installed?'warn':'neutral'}>{service?.ready?'运行条件已满足':service?.installed?'服务需修复':service?'服务未安装':'服务状态待检查'}</Badge>}>
  <Setting title="接管系统流量" desc="使用 WinDivert 按进程与分流规则处理本机 TCP / UDP；开关需重启生效。"><Switch checked={config.networkMode==='divert'} disabled={!canToggleIntercept} onChange={v=>{setv('networkMode',v?'divert':'');setv('network.mode',v?'divert':'')}}/></Setting>
  {!interceptReady&&<div className="notice warn top-gap"><b>当前系统透明代理尚不可用。</b> {caps.unavailable_reason||'此构建缺少完整的 TCP / UDP / IPv6 / 进程识别或回注能力。'}{config.networkMode==='divert'?' 你仍可关闭已保存的透明代理模式。':' 请修复 Network Service，或继续使用 SOCKS5 / HTTP Proxy。'}</div>}
  <div className="cards-3 compact top-gap">
   <div><div className="mini">Network Service</div><strong>{service?.ready?'就绪':service?.running?'运行中':service?.installed?'已安装':'尚未取得状态'}</strong><div className="mini">{service?.autoStartKnown?(service?.autoStart?'Automatic':'非自动启动'):'启动类型未知'}</div></div>
   <div><div className="mini">抓包后端</div><strong>{status.divertDiagnostics?.captureBackend||'WinDivert'}</strong><div className="mini">{status.divertDiagnostics?.captureState||status.divertStage||'idle'}</div></div>
   <div><div className="mini">数据面</div><strong>{Number(status.divertDiagnostics?.captured||0)} / {Number(status.divertDiagnostics?.classified||0)}</strong><div className="mini">抓包 / 分类 · PROXY {Number(status.divertDiagnostics?.proxy||0)}</div></div>
  </div>
  {status.divertError&&<div className="notice danger top-gap">{status.divertError}</div>}
  <Field label="排除进程" help="这些进程的流量不经透明代理；可用换行、逗号或分号分隔，如 steam.exe, Teams.exe"><Textarea rows="4" value={ruleListText(config.network?.exclude_processes)} onChange={e=>setv('network.exclude_processes',e.target.value)}/></Field>
  {showWindowsService(config.platform,networkServiceBridgeAvailable)&&<details className="details top-gap"><summary>Network Service 维护</summary>
   <div className="mini top-gap">透明代理由低权限 GUI 与 SYSTEM 网络服务协作完成。{service?.message||''}</div>
   {service&&<div className="service-grid top-gap"><div><span>安装</span><b>{service.installed?'是':'否'}</b></div><div><span>运行</span><b>{service.running?'是':'否'}</b></div><div><span>自动启动</span><b>{service.autoStartKnown?(service.autoStart?'是':'否'):'未知'}</b></div><div><span>版本匹配</span><b>{service.versionMatch?'是':'否'}</b></div><div><span>恢复策略</span><b>{service.recoveryKnown?(service.recoveryEnabled?'已启用':'未启用'):'未知'}</b></div><div><span>PID</span><b className="mono">{service.pid||'—'}</b></div>{service.binaryPath&&<div className="service-path"><span>程序路径</span><b className="mono">{service.binaryPath}</b></div>}</div>}
   {dirty&&<div className="notice warn top-gap">存在未保存配置时暂不允许卸载 Network Service，避免原生侧关闭透明代理后导致草稿 revision 失效。</div>}
   <div className="actions top-gap"><Button disabled={!!busy} onClick={()=>serviceAct('goRepairNetworkService','Network Service 修复完成')}>{busy==='goRepairNetworkService'?'修复中…':'安装 / 修复服务'}</Button><Button danger disabled={!!busy||dirty} onClick={()=>serviceAct('goUninstallNetworkService','网络服务已卸载')}>卸载服务</Button></div>
  </details>}
 </Card>}
 {isLinux&&<GatewaySection config={config} status={status} exits={exits} setv={setv}/>}
 <SaveBar dirty={dirty} label="保存接入设置" hint={'本地监听参数'+(windowsCaptureAvailable?'与 Windows 透明代理开关需重启生效':'需重启生效')+(isMac?'；macOS 系统代理即时应用':'')+(isLinux?'；局域网网关保存后立即应用':'')+'。'} onSave={saveAll}/></>
}

function ExitShare({status,config,setv,save,dirty}){
 const u=config.exitUpstream||EMPTY.exitUpstream,[loopbackRisk,setLoopbackRisk]=useState(false);
 const customs=[...arr(config.customExits).map(x=>({...x,kindLabel:(x.protocol||'socks5').toUpperCase()})),...arr(config.routing?.device_groups).map(g=>({...g,enabled:true,protocol:'group',kindLabel:'设备分组',address:g.members.length+' 台设备 · 按优先级故障切换'}))],chosen=customs.find(x=>x.id===config.upstreamExitId);
 const legacy=!config.upstreamExitId&&u.mode&&u.mode!=='direct';
 const selected=config.upstreamExitId||(legacy?'__legacy__':'__direct__');
 return <><PageHead title="本机出口共享" desc="允许 Server 将授权流量通过此设备访问 Internet 或指定网络。"/>
 <Card title="出口能力" eyebrow="EXIT NODE" action={<Switch checked={config.exitEnabled!==false} onChange={v=>setv('exitEnabled',v)}/>}><Setting title="当前运行状态" desc="Agent 出口处理器"><Badge tone={status.exitRunning?'ok':'neutral'}>{status.exitRunning?'正在提供出口':'未运行'}</Badge></Setting><Setting title="允许访问 Internet" desc="允许作为公网出口"><Switch checked={!!config.allowInternet} onChange={v=>setv('allowInternet',v)}/></Setting><Setting title="允许浏览器网络共享" desc="允许同身份已授权浏览器通过 Server 的独立网络代理使用此 Agent 出口。默认关闭；Server 还需单独批准设备 ID。"><Switch checked={!!config.browserNetworkShare} onChange={v=>setv('browserNetworkShare',v)}/></Setting><Setting title="允许访问私有网络" desc="访问 RFC1918 / 内网地址"><Switch checked={!!config.allowPrivateNetwork} onChange={v=>setv('allowPrivateNetwork',v)}/></Setting><Setting title="允许访问 Loopback" desc="允许访问 127.0.0.0/8 / ::1 上的本机服务；高风险，默认关闭。"><Switch checked={!!config.allowLoopback} onChange={v=>v?setLoopbackRisk(true):setv('allowLoopback',false)}/></Setting></Card>
 <div className="cards-2"><Card title="共享上游出口" eyebrow="UPSTREAM">
  <Field label="上游连接方式">
   <Select value={selected} onChange={e=>{
    const id=e.target.value;
    setv('upstreamExitId',(id.startsWith('local:')||id.startsWith('group:'))?id:'');
    if(id==='__direct__')setv('exitUpstream.mode','direct');
   }}>
    <option value="__direct__">本机直连</option>
    {legacy&&<option value="__legacy__">旧配置：{u.mode.toUpperCase()} · {u.address||'地址未知'}（保留原行为）</option>}
    {customs.map(x=><option key={x.id} value={x.id} disabled={!x.enabled&&x.id!==config.upstreamExitId}>{x.name} · {x.kindLabel} · {x.address}{!x.enabled?'（已停用）':''}</option>)}
   </Select>
  </Field>
  {chosen&&<div className="setting top-gap"><div className="grow"><b>{chosen.name}</b><div className="mini">{chosen.address} · {chosen.kindLabel}</div></div><Badge tone={chosen.enabled?'ok':'danger'}>{chosen.enabled?'可用':'不可用'}</Badge></div>}
  {chosen&&chosen.protocol==='group'&&<div className="notice top-gap">共享流量将经由分组内首个可用的已授权 Agent 出口转发，成员全部离线时共享请求失败，不会回退本机直连。</div>}
  {chosen&&chosen.protocol!=='socks5'&&chosen.protocol!=='group'&&<div className="notice warn top-gap">HTTP(S) CONNECT 仅支持 TCP。共享出口的 UDP 请求会失败，不会回退本机直连。</div>}
  {config.upstreamExitId&&!chosen&&<div className="notice danger top-gap">所选上游出口已丢失，保存将被阻止。请重新选择或切换到本机直连。</div>}
  {legacy&&<div className="notice warn top-gap">当前仍使用旧版上游 {u.mode}。请创建自定义出口并在此引用；切换上游前保留原有工作方式。</div>}
  {!customs.length&&<div className="notice top-gap">暂无自定义出口。请前往「出口」页的「自定义代理」创建。</div>}
 </Card><Card title="出口访问控制" eyebrow="ACCESS POLICY"><Field label="策略模式"><Select value={config.accessMode||''} onChange={e=>setv('accessMode',e.target.value)}><option value="">关闭</option><option value="allow">仅允许列表</option><option value="deny">拒绝列表</option></Select></Field><Field label="域名规则" help="可用换行、逗号或分号分隔，支持通配符，如 *.example.com"><Textarea rows="4" value={ruleListText(config.accessDomains)} onChange={e=>setv('accessDomains',e.target.value)}/></Field><Field label="CIDR / IP" help="可用换行、逗号或分号分隔，如 10.0.0.0/8, 192.168.1.1"><Textarea rows="4" value={ruleListText(config.accessCidrs)} onChange={e=>setv('accessCidrs',e.target.value)}/></Field></Card></div>
 <SaveBar dirty={dirty} label="保存出口共享设置" hint="同时保存出口能力、上游代理与访问控制；出口相关设置均为启动参数，保存后需重启客户端生效。" onSave={()=>save({exit:{enabled:config.exitEnabled,browserNetworkShare:!!config.browserNetworkShare,allowInternet:config.allowInternet,allowPrivateNetwork:config.allowPrivateNetwork,allowLoopback:config.allowLoopback,upstreamExitId:config.upstreamExitId||'',upstream:config.upstreamExitId?undefined:u,access:{mode:config.accessMode||'',domains:splitRuleList(config.accessDomains),cidrs:splitRuleList(config.accessCidrs)}}})}/>
 <Modal open={loopbackRisk} title="允许出口流量访问本机 Loopback？" onClose={()=>setLoopbackRisk(false)} footer={<><Button onClick={()=>setLoopbackRisk(false)}>保持关闭</Button><Button danger onClick={()=>{setv('allowLoopback',true);setLoopbackRisk(false)}}>我了解风险，允许访问</Button></>}><div className="notice danger"><b>启用后，经 Server 授权到达此出口的流量可以访问本机 Loopback 服务。</b><br/>这可能包含只监听 127.0.0.1 / ::1 的管理接口、开发服务或其他原本不面向网络开放的进程。仅在你明确需要且访问策略足够严格时开启。</div></Modal></>
}

function RuleEditor({value,exits,onChange}){
 const set=(k,v)=>onChange({...value,[k]:v});
 const setAction=action=>onChange({...value,action,exit_id:action==='PROXY'?(value.exit_id||''):'',handle_direct:action==='DIRECT'?!!value.handle_direct:false});
 const sep='可用换行、逗号或分号分隔多个条目';
 return <div className="form-grid"><Field label="规则名称"><Input value={value.name||''} onChange={e=>set('name',e.target.value)}/></Field><Field label="动作"><Select value={value.action||'PROXY'} onChange={e=>setAction(e.target.value)}><option value="PROXY">PROXY</option><option value="DIRECT">DIRECT</option><option value="REJECT">REJECT</option></Select></Field><Field label="指定出口"><Select value={value.exit_id||''} disabled={value.action!=='PROXY'} onChange={e=>set('exit_id',e.target.value)}><option value="">跟随默认出口</option>{exits.map(x=><option key={x.deviceId||x.id} value={x.deviceId||x.id} disabled={x.online===false&&x.kind!=='group'}>{x.name||x.deviceId}{x.online===false?' · 离线':''}</option>)}</Select></Field><Field label="协议"><Select value={protocolChoice(value.protocols)} onChange={e=>set('protocols',protocolsFromChoice(e.target.value))}>{PROTOCOL_CHOICES.map(([v,l])=><option key={v} value={v}>{l}</option>)}</Select></Field><Field label="进程" help={sep+'，如 chrome.exe, steam.exe'} className="full"><Textarea rows="3" value={ruleListText(value.processes)} onChange={e=>set('processes',e.target.value)}/></Field><Field label="域名 / IP / CIDR" help={sep+'，支持通配符，如 *.example.com; 10.0.0.0/8'} className="full"><Textarea rows="3" value={ruleListText(value.targets)} onChange={e=>set('targets',e.target.value)}/></Field><Field label="入口" help="限定规则只对某类入口生效；来自局域网网关的流量没有本机进程信息"><Select value={arr(value.entries).includes('gateway')?'gateway':arr(value.entries).length?'__custom__':''} onChange={e=>{if(e.target.value!=='__custom__')set('entries',e.target.value?[e.target.value]:[])}}><option value="">所有入口</option><option value="gateway">局域网网关</option>{arr(value.entries).length>0&&!arr(value.entries).includes('gateway')&&<option value="__custom__">自定义：{arr(value.entries).join(', ')}</option>}</Select></Field><Field label="来源网段" help={sep+'；留空匹配所有来源，如 192.168.1.0/24'}><Textarea rows="2" value={ruleListText(value.source_cidrs)} onChange={e=>set('source_cidrs',e.target.value)}/></Field><Field label="端口" help={sep+'，留空表示任意端口'} className="full"><Textarea rows="2" value={ruleListText(value.ports)} onChange={e=>set('ports',e.target.value)}/></Field><label className="check-line"><input type="checkbox" checked={!!value.datagram_required} disabled={!!value.handle_direct} onChange={e=>set('datagram_required',e.target.checked)}/><span><strong>要求原生 UDP</strong><small>{value.handle_direct?'托管 DIRECT 与 Relay Datagram 互斥。':'仅在支持 datagram 的代理路径上使用。'}</small></span></label><label className="check-line"><input type="checkbox" checked={!!value.handle_direct} disabled={value.action!=='DIRECT'||!!value.datagram_required} onChange={e=>set('handle_direct',e.target.checked)}/><span><strong>RelayProxy 托管 DIRECT</strong><small>{value.datagram_required?'要求原生 UDP 时不能同时托管 DIRECT。':'由 RelayProxy 建立本地直连并纳入连接监控。'}</small></span></label></div>
}

function RoutingPage({config,exits,setv,save,dirty,onDiscard,onGoto}){
 exits=[...exits,...arr(config.customExits).map(x=>({id:x.id,name:x.name,online:x.enabled}))];
 const r=config.routing||EMPTY.routing,[editing,setEditing]=useState(null),rules=arr(r.rules);
 const subs=arr(r.subscriptions),[editingSub,setEditingSub]=useState(null),[subscriptionStatus,setSubscriptionStatus]=useState([]);
 useEffect(()=>{
  let mounted=true;
  const refresh=async()=>{if(!hasBridge('goGetRoutingSubscriptions'))return;try{const statuses=arr(await callJSON('goGetRoutingSubscriptions',[]));if(mounted)setSubscriptionStatus(statuses)}catch{}};
  void refresh();const timer=setInterval(refresh,5000);
  return()=>{mounted=false;clearInterval(timer)};
 },[]);
 const commitSub=x=>setv('routing.subscriptions',x);
 const editSub=i=>setEditingSub({index:i,sub:JSON.parse(JSON.stringify(i>=0?subs[i]:{name:'GFWList',url:'https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt',enabled:true,action:'PROXY',exit_id:'',fetch_via_proxy:false}))});
 const saveSub=()=>{
  const sub={...editingSub.sub,name:(editingSub.sub.name||'').trim(),url:(editingSub.sub.url||'').trim()};
  try{const u=new URL(sub.url);if(u.protocol!=='https:'||!u.hostname||u.username||u.password)throw Error('需要公开的 HTTPS 订阅地址');if(!sub.name)throw Error('请输入订阅名称');if(subs.some((x,i)=>i!==editingSub.index&&x.url===sub.url))throw Error('不能添加重复的订阅地址');}
  catch(e){window.alert(e.message||'订阅地址无效');return}
  const next=[...subs];if(editingSub.index<0)next.push(sub);else next[editingSub.index]=sub;
  commitSub(next);setEditingSub(null);
 };
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
 const mode=r.mode||'global_proxy';
 return <><PageHead title="分流" actions={<Button primary onClick={()=>edit(-1)}>＋ 新建规则</Button>}/>
 <Card className="routing-policy">
  <div className="routing-policy-row">
   <div className="routing-policy-item"><span className="mini">路由模式</span><div className="segment" role="radiogroup" aria-label="路由模式">{ROUTING_MODES.map(([v,l])=><button key={v} type="button" role="radio" aria-checked={mode===v} className={mode===v?'active':''} onClick={()=>setv('routing.mode',v)}>{l}</button>)}</div></div>
   <div className="routing-policy-item"><span className="mini">规则未命中时</span><Select aria-label="规则未命中时" disabled={mode!=='rule'} value={r.default_action||'PROXY'} onChange={e=>setv('routing.default_action',e.target.value)}><option value="PROXY">PROXY · 走默认出口</option><option value="DIRECT">DIRECT · 本机直连</option><option value="REJECT">REJECT · 阻断</option></Select></div>
   <div className="routing-policy-item grow"><span className="mini">默认出口</span><div className="row"><strong>{exitName(exits,config.defaultExitId||'')}</strong><Button quiet onClick={()=>onGoto('exits')}>前往出口页 ›</Button></div></div>
  </div>
  <div className="mini top-gap">{mode==='rule'?'按下方规则从上到下匹配：手动规则 → 订阅 → 未命中动作。':mode==='direct'?'全局直连：所有流量不经代理，下方规则不参与匹配。':'全局代理：所有流量经默认出口，下方规则不参与匹配。'}「默认出口」决定 PROXY 连接用哪台设备，在「出口」页一键切换；已授权出口撤销或离线时不会静默直连。</div>
 </Card>
 <Card title="规则列表" eyebrow={rules.length+' RULES'}><div className="rule-table"><div className="rule-row rule-head"><span>#</span><span>规则</span><span>匹配条件</span><span>动作</span><span>启用</span><span>操作</span></div>{rules.map((x,i)=><div className={cx('rule-row',!x.enabled&&'disabled',dragging===i&&'dragging',dropAt&&dropAt.i===i&&dragging!==i&&(dropAt.after?'drop-after':'drop-before'))} key={(x.name||'rule')+'-'+i} {...dragProps(i)}><span className="drag" role="button" tabIndex={-1} aria-label={'拖动调整顺序：'+(x.name||'规则 '+(i+1))} title="拖动调整顺序" onMouseDown={()=>{armed.current=true}}>⋮⋮</span><div><div className="row"><strong>{x.name||'规则 '+(i+1)}</strong></div><div className="mini">优先级 {i+1}</div></div><div className="chips">{arr(x.entries).includes('gateway')&&<span className="chip" key="e">局域网网关</span>}{arr(x.source_cidrs).slice(0,1).map(v=><span className="chip" key={'s'+v}>来源 {v}</span>)}{arr(x.processes).slice(0,2).map(v=><span className="chip" key={'p'+v}>{v}</span>)}{arr(x.targets).slice(0,2).map(v=><span className="chip" key={'t'+v}>{v}</span>)}{arr(x.ports).slice(0,1).map(v=><span className="chip" key={'o'+v}>{v}</span>)}{!arr(x.entries).length&&!arr(x.source_cidrs).length&&!arr(x.processes).length&&!arr(x.targets).length&&!arr(x.ports).length&&<span className="mini">任意流量</span>}</div><div><Badge tone={x.action==='REJECT'?'danger':x.action==='DIRECT'?'blue':'ok'}>{x.action||'PROXY'}</Badge>{x.exit_id&&<div className="mini">{exitName(exits,x.exit_id)}</div>}</div><div><Switch checked={!!x.enabled} label={(x.enabled?'停用':'启用')+' '+(x.name||'规则 '+(i+1))} onChange={v=>toggle(i,v)}/></div><div className="rule-ops"><Button quiet disabled={i===0} onClick={()=>move(i,-1)}>↑</Button><Button quiet disabled={i===rules.length-1} onClick={()=>move(i,1)}>↓</Button><Button quiet onClick={()=>edit(i)}>编辑</Button><Button quiet danger onClick={()=>window.confirm('删除这条分流规则？')&&commit(rules.filter((_,n)=>n!==i))}>删除</Button></div></div>)}{!rules.length&&<div className="empty">尚无规则。切换到规则模式前请先添加规则。</div>}</div></Card>
 <Card title="规则订阅" eyebrow={subs.length+' SUBSCRIPTIONS'} action={<Button onClick={()=>editSub(-1)}>＋ 添加订阅</Button>}>
  <div className="mini">支持 GFWList（Base64 / Adblock 主机规则）、域名和 IP/CIDR 文本列表。按上到下顺序匹配：手动规则优先 → 订阅 → 默认动作。仅在规则模式生效，每 6 小时自动更新；下载失败继续使用本地已缓存的最后成功版本。URL 路径、正则和 Adblock 修饰符不会被扩大为整个域名匹配。</div>
  {subs.map((sub,i)=><div className="setting" key={sub.url+'-'+i}>
   <div className="grow" style={{minWidth:0}}><b>{sub.name||'未命名订阅'}</b><div className="mini" style={{overflowWrap:'anywhere'}}>{sub.url}</div><div className="mini">{sub.action||'PROXY'}{sub.exit_id?' · '+exitName(exits,sub.exit_id):' · 跟随默认出口'} · {sub.fetch_via_proxy?'代理更新':'直连更新'}</div>
   {(()=>{const current=subscriptionStatus.find(x=>x.url===sub.url);if(!current||!sub.enabled)return null;return <div className="mini">{current.rules?('已加载 '+current.rules+' 条规则'):'等待首次下载'}{current.updated_at&&new Date(current.updated_at).getFullYear()>2000?' · 更新于 '+fmtTime(current.updated_at):''}{current.skipped?' · 跳过 '+current.skipped+' 条不支持的规则':''}{current.error&&<span style={{color:'var(--danger, #b91c1c)'}}> · 更新失败：{current.error}</span>}</div>})()}</div>
   <Switch checked={!!sub.enabled} label={(sub.enabled?'停用':'启用')+sub.name} onChange={on=>commitSub(subs.map((x,j)=>i===j?{...x,enabled:on}:x))}/>
   <div className="rule-ops"><Button quiet disabled={i===0} onClick={()=>{const next=[...subs];[next[i-1],next[i]]=[next[i],next[i-1]];commitSub(next)}}>↑</Button><Button quiet disabled={i===subs.length-1} onClick={()=>{const next=[...subs];[next[i],next[i+1]]=[next[i+1],next[i]];commitSub(next)}}>↓</Button><Button quiet onClick={()=>editSub(i)}>编辑</Button><Button quiet danger onClick={()=>window.confirm('删除此规则订阅？')&&commitSub(subs.filter((_,j)=>j!==i))}>删除</Button></div>
  </div>)}
  {!subs.length&&<div className="empty">尚未添加订阅，可以添加 GFWList 或自建规则列表。</div>}
 </Card>
 <SaveBar dirty={dirty} label="保存分流" hint="保存路由模式、未命中动作、规则与订阅；默认出口在「出口」页切换，DNS 选项在「DNS」页修改。" onSave={()=>save({routing:routingPagePatch(r)})}><Button disabled={!dirty} onClick={onDiscard}>放弃修改</Button></SaveBar>
 <Modal open={!!editingSub} title={editingSub&&editingSub.index>=0?'编辑规则订阅':'添加规则订阅'} wide onClose={()=>setEditingSub(null)} footer={<><Button onClick={()=>setEditingSub(null)}>取消</Button><Button primary onClick={saveSub}>保存订阅</Button></>}>
  {editingSub&&<div className="form-grid">
   <Field label="订阅名称"><Input value={editingSub.sub.name||''} onChange={e=>setEditingSub({...editingSub,sub:{...editingSub.sub,name:e.target.value}})}/></Field>
   <Field label="动作"><Select value={editingSub.sub.action||'PROXY'} onChange={e=>setEditingSub({...editingSub,sub:{...editingSub.sub,action:e.target.value,exit_id:e.target.value==='PROXY'?editingSub.sub.exit_id:''}})}><option value="PROXY">PROXY</option><option value="DIRECT">DIRECT</option><option value="REJECT">REJECT</option></Select></Field>
   <Field label="订阅 URL" className="full" help="仅允许公开 HTTPS 地址；GFWList 会自动识别 Base64 编码。"><Input value={editingSub.sub.url||''} onChange={e=>setEditingSub({...editingSub,sub:{...editingSub.sub,url:e.target.value}})} placeholder="https://raw.githubusercontent.com/.../gfwlist.txt"/></Field>
   <Field label="指定出口" className="full"><Select disabled={(editingSub.sub.action||'PROXY')!=='PROXY'} value={editingSub.sub.exit_id||''} onChange={e=>setEditingSub({...editingSub,sub:{...editingSub.sub,exit_id:e.target.value}})}><option value="">跟随默认出口</option>{exits.map(x=><option value={x.deviceId||x.id} key={x.deviceId||x.id}>{x.name||x.deviceId||x.id}</option>)}</Select></Field>
   <label className="check-line"><input type="checkbox" checked={!!editingSub.sub.fetch_via_proxy} onChange={e=>setEditingSub({...editingSub,sub:{...editingSub.sub,fetch_via_proxy:e.target.checked}})}/><span><strong>通过代理获取订阅内容</strong><small>默认关闭。启用后通过本订阅指定的代理出口下载；未指定时跟随默认代理出口。与订阅的 PROXY / DIRECT / REJECT 匹配动作无关；连接失败绝不回退直连。</small></span></label>
   <label className="check-line"><input type="checkbox" checked={!!editingSub.sub.enabled} onChange={e=>setEditingSub({...editingSub,sub:{...editingSub.sub,enabled:e.target.checked}})}/><span><strong>启用订阅</strong><small>启用后按间隔自动刷新，保存会触发立即下载。</small></span></label>
  </div>}
 </Modal>
 <Modal open={!!editing} title={editing&&editing.index>=0?'编辑分流规则':'新建分流规则'} wide onClose={()=>setEditing(null)} footer={<><Button onClick={()=>setEditing(null)}>取消</Button><Button primary onClick={saveEditor}>保存规则</Button></>}>{editing&&<RuleEditor value={editing.rule} exits={exits} onChange={rule=>setEditing({...editing,rule})}/>}</Modal></>
}

// The three presets intentionally project to legacy backend fields. Advanced
// settings are retained for existing deployments and specialised debugging.
function DNSUpstreamsEditor({upstreams,onChange}){
 const[draft,setDraft]=useState(()=>formatDNSUpstreamLines(upstreams));
 useEffect(()=>setDraft(formatDNSUpstreamLines(upstreams)),[upstreams]);
 return <Textarea rows="4" placeholder={'https://resolver.example/dns-query | 10.0.0.53'}
  value={draft} onChange={e=>setDraft(e.target.value)}
  onBlur={()=>onChange(parseDNSUpstreamLines(draft))}/>;
}

function DNSPage({status,config,exits,setv,save,dirty,onDiscard}){
 const[advancedOpen,setAdvancedOpen]=useState(false);
 const[probing,setProbing]=useState(false);
 const[probe,setProbe]=useState(null);
 const runDNSProbe=async()=>{
  if(probing||dirty)return;
  setProbing(true);setProbe(null);
  try {
   const response=parseMutation(await call('goProbeDNS'));
   if(!response.ok)throw new Error(response.message||'DNS 出口诊断失败');
   setProbe({result:response.result||response});
  }catch(e){setProbe({error:e.message||'DNS 出口诊断失败'})}
  finally{setProbing(false)}
 };
 const r=config.routing||EMPTY.routing;
 const inventory=[...arr(exits).map(x=>({id:x.deviceId||x.id,name:x.name||x.deviceName||x.deviceId||x.id})),...arr(config.customExits).filter(x=>x.enabled).map(x=>({id:x.id,name:x.name||x.id}))];
 const change=next=>setv('routing',next);
 const preset=currentDNSPreset(r);
 return <><PageHead title="DNS" desc="系统 DNS 的解析方式、加密 DNS 出口与防泄漏选项；实际接管状态请在「诊断」页查看。"/>
 <Card title="DNS 处理模式" eyebrow="DNS POLICY">
  <Field label="解析模式" help="切换模式会统一更新内部 DNS 选项，旧版配置仍可使用。">
   <Select value={preset} onChange={e=>change(applyDNSPreset(r,e.target.value))}>
    <option value="auto">自动（推荐）· 经代理接管系统 DNS，返回真实 IP</option>
    <option value="real_ip">真实 IP · 旁路关联域名，不接管系统 DNS</option>
    <option value="fake_ip">FakeIP · 虚拟地址与域名映射</option>
   </Select>
  </Field>
  <div className="notice top-gap">{preset==='auto'?'自动模式：接管可捕获的系统 DNS/53 查询，经代理出口解析真实 IP；连接中的已知域名交给出口解析。':preset==='real_ip'?'真实 IP 模式：保留系统 DNS，观察传统 DNS 响应以关联域名；DNS 查询可能从本机发出。':'FakeIP 模式：为接管的 DNS 查询分配虚拟地址，并在连接时恢复域名。'}</div>
  <div className="mini top-gap">透明代理需处于运行状态才能接管系统 DNS。Chrome 私有 DoH、环回 DNS 与 Agent 退出后的防泄漏不保证由此模式覆盖；可在诊断页检查。</div>
  <div className="actions end top-gap"><Button onClick={()=>setAdvancedOpen(!advancedOpen)}>{advancedOpen?'收起高级设置':'高级设置 ›'}</Button></div>
 </Card>
 {advancedOpen&&<>
  <Card title="域名关联与 DNS 出口" eyebrow="ADVANCED DNS">
   <Setting title="真实 IP → 域名关联" desc="观察验证过的 DNS A/AAAA 应答，为透明代理提供域名线索。共享 IP、系统缓存和应用私有 DoH 仍可能导致无法识别。">
    <Switch checked={r.dns_association_enabled!==false} label="DNS 域名关联" onChange={v=>setv('routing.dns_association_enabled',v)}/>
   </Setting>
   <div className="form-grid top-gap">
    <Field label="加密 DNS 查询出口" help="代理真实 IP DNS 与 FakeIP 补充查询使用；留空跟随默认出口。">
     <Select value={r.dns_exit_id||''} onChange={e=>setv('routing.dns_exit_id',e.target.value)}>
      <option value="">跟随当前默认出口</option>
      {inventory.filter(x=>x.id).map(x=><option value={x.id} key={x.id}>{x.name}</option>)}
      {r.dns_exit_id&&!inventory.some(x=>x.id===r.dns_exit_id)&&<option value={r.dns_exit_id}>{r.dns_exit_id}（不可用）</option>}
     </Select>
    </Field>
   </div>
   <div className="form-grid top-gap">
    <Field label="自定义 DoH 上游（每行 URL | 连接 IP）" className="full" help="留空使用内置 DNS。填写后只使用这些上游，不会回退到公共 DNS。连接经上方出口发送，证书按 URL 域名验证。支持指定出口节点内网 HTTPS DNS。">
     <DNSUpstreamsEditor upstreams={r.dns_upstreams} onChange={v=>setv('routing.dns_upstreams',v)}/>
    </Field>
    <Field label="本地 DNS 域名例外" className="full" help="仅这些域名及其子域名使用系统原定的 DNS 服务器，适合企业内网域名；一行一个，例如 corp.example。留空继续全部经代理解析。对应连接仍按分流规则处理；独立 DNS 阻断规则可能阻止本地解析。">
     <Textarea rows="3" placeholder="corp.example" value={ruleListText(r.dns_direct_domains)} onChange={e=>setv('routing.dns_direct_domains',e.target.value)}/>
    </Field>
   </div>
  </Card>
  <Card title="FakeIP 专用防护" eyebrow="ADVANCED PROTECTION">
   <Setting title="阻断已知 DoH 解析器" desc="仅针对可识别的已知 DoH 端点，不能覆盖全部私有 DoH、ECH 或直连 IP。只有 FakeIP 模式可修改。">
    <Switch checked={!!r.block_doh_endpoints} label="已知 DoH 解析器阻断" disabled={!r.fake_ip_enabled} onChange={v=>change(setDoHBlocking(r,v))}/>
   </Setting>
   <Setting title="通过代理补充 TXT / SRV 查询" desc="经指定出口的加密解析器查询额外 DNS 记录；只有 FakeIP 模式可修改。">
    <Switch checked={!!r.forward_other_dns} label="FakeIP 补充 TXT 与 SRV" disabled={!r.fake_ip_enabled} onChange={v=>change(setOtherDNSForwarding(r,v))}/>
   </Setting>
   <div className="form-grid top-gap">
    <Field label="额外阻断的 DoH IP / CIDR" className="full" help="按地址阻断 TCP/UDP 443，可能同时影响共享此 IP 的正常 HTTPS 服务；一行一个 IP 或 CIDR。">
     <Textarea rows="3" placeholder={'9.9.9.9\\n1.1.1.1'} value={arr(r.doh_blocked_ips).join('\\n')} onChange={e=>setv('routing.doh_blocked_ips',e.target.value.split(/[\\n,;]+/).map(x=>x.trim()).filter(Boolean))}/>
    </Field>
   </div>
   <div className="notice warn top-gap">这些开关不等于完整的 DNS 防泄漏保证。环回 DNS、应用私有 DoH 与 Agent/驱动退出后的网络保护，需要独立验证。</div>
   
  </Card>
 </>}
 <Card title="加密 DNS 出口连通性" eyebrow="DNS PROBE">
  <div className="mini">使用当前已保存的 DNS 出口逐个测试加密上游的真实 DNS 查询，并显示延迟及错误原因。不会访问本机 DNS，也不代表已经验证应用私有 DoH 或独立 Kill Switch。</div>
  <div className="actions end top-gap"><Button disabled={probing||dirty} onClick={runDNSProbe}>{probing?'检测中…':'检测 DNS 上游'}</Button></div>
  {dirty&&<div className="notice warn top-gap">DNS 配置尚未保存，请保存后再检测，避免诊断结果与页面设置不一致。</div>}
  {probe?.error&&<div className="notice danger top-gap">{probe.error}</div>}
  {probe?.result&&<>
   <div className="mini top-gap">DNS 出口：{probe.result.exitId||'跟随默认出口'} · {probe.result.custom?'自定义上游':'内置上游'}</div>
   {arr(probe.result.results).map((item,i)=><div className="target-row" key={item.url+'-'+i}>
    <div className="grow">
     <div className="row"><strong>{item.name||'DNS 上游'}</strong><Badge tone={item.ok?'ok':'warn'}>{item.ok?'可用':'失败'}</Badge></div>
     <div className="mono mini">{item.protocol} · {item.address} · {item.url}</div>
     <div className="mini">{item.ok?'DNS 查询成功'+(item.rcode?' · '+item.rcode:''):(item.error||'无法获取应答')}</div>
    </div>
    <div className="mono">{item.latencyMs??'—'} ms</div>
   </div>)}
  </>}
 </Card>
 <SaveBar dirty={dirty} label="保存 DNS 设置" hint="切换模式自动配置兼容字段；保存后按现有热更新流程生效。" onSave={()=>save({routing:dnsPagePatch(r)})}><Button disabled={!dirty} onClick={onDiscard}>放弃修改</Button></SaveBar>
 </>;
}

function RDPPage({status,config,targets,refreshTargets,refreshStatus,toast,setv,save,dirty,onRestart}){
 const[busy,setBusy]=useState('');
 const desiredEnabled=config.rdp?.enabled!==false,runtimeEnabled=config.runtime?.rdp?.enabled!==false;
 const rdpRestartPending=!dirty&&config.restartRequired&&arr(config.restartFields).some(x=>String(x).includes('RDP'));
 const controllerApproved=arr(status.approvedCapabilities).includes('rdp.controller');
 const sessions=arr(status.rdpSessions).length?arr(status.rdpSessions):(status.rdpTargetId?[{deviceId:status.rdpTargetId,listenAddr:status.rdpListenAddr,udpEnabled:status.rdpUdpEnabled,udpActive:status.rdpUdpActive,udpReason:status.rdpUdpReason,pathTcp:status.rdpPathTcp,pathUdp:status.rdpPathUdp}]:[]);
 const activeByID=new Map(sessions.map(s=>[s.deviceId,s]));
 const connect=async id=>{const reopening=activeByID.has(id);setBusy(id);try{const r=parseMutation(await call('goConnectRDP',id,true));if(!r.ok)throw new Error(r.message||'RDP 连接失败');toast(r.target?.hostsError?'RDP 已建立，hosts 别名未写入：'+r.target.hostsError:reopening?'已请求再次打开本地远程桌面':'RDP 本地入口已建立',r.target?.hostsError?'warn':undefined);await refreshStatus()}catch(e){toast(e.message,'danger')}finally{setBusy('')}};
 const disconnect=async id=>{setBusy(id);try{const r=parseMutation(await call('goDisconnectRDPTarget',id));if(!r.ok)throw new Error(r.message||'RDP 断开失败');await refreshStatus();toast('该设备的 RDP 已断开')}catch(e){toast(e.message,'danger')}finally{setBusy('')}};
 const disconnectAll=async()=>{setBusy('all');try{const r=parseMutation(await call('goDisconnectRDP'));if(!r.ok)throw new Error(r.message||'RDP 断开失败');await refreshStatus();toast('全部 RDP 会话已断开')}catch(e){toast(e.message,'danger')}finally{setBusy('')}};
 const copy=async value=>{if(!value)return;try{if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',String(value));else await navigator.clipboard.writeText(String(value));toast('已复制 RDP 本地入口')}catch(e){toast(e.message,'danger')}};
 return <><PageHead title="远程桌面" desc="已授权设备可同时保持独立的远程桌面连接，各自使用独立本地 TCP / UDP 端口。" actions={<Button onClick={refreshTargets}>刷新设备</Button>}/>
 {!desiredEnabled&&<div className="notice warn"><b>RDP 已在本机配置中关闭。</b> 在下方「远程桌面偏好」中开启、保存并重启客户端，之后才会重新申请 rdp.controller。</div>}
 {desiredEnabled&&runtimeEnabled&&status.connected&&!controllerApproved&&<div className="notice warn"><b>Server 未授予当前设备 rdp.controller。</b> 请在 Server 设备授权中启用“RDP 控制端”；授权变更后 Agent 会重新连接并刷新列表。</div>}
 {sessions.length>0&&<Card title="当前 RDP 会话" eyebrow={sessions.length+' ACTIVE SESSIONS'} action={sessions.length>1?<Button danger disabled={!!busy} onClick={disconnectAll}>断开全部</Button>:null}>
  <div className="target-list">{sessions.map(s=>{const id=s.deviceId,name=s.name||targets.find(t=>(t.deviceId||t.id)===id)?.name||id;return <div className="target-row rdp-session-row" key={id}><div className="identity-icon small">▣</div><div className="grow"><strong>{name}</strong><div className="mono mini">{s.listenAddr||'准备中'}</div><div className="mini">TCP {rdpPathLabel(s.pathTcp)} · UDP {rdpPathLabel(s.pathUdp)} · {s.udpActive?'UDP Active':s.udpEnabled?'UDP Ready':'TCP Only'}</div>{s.udpReason&&<div className="mini">{s.udpReason}</div>}{s.hostsError&&<div className="mini">hosts：{s.hostsError}（已回退本地 IP）</div>}</div><Badge tone="ok">已连接</Badge><Button primary disabled={!!busy||!status.connected} title="复用现有本地端口，再次唤起远程桌面客户端" onClick={()=>connect(id)}>{busy===id?'打开中…':'再次打开'}</Button><Button quiet disabled={!s.listenAddr} onClick={()=>copy(s.listenAddr)}>复制地址</Button><Button danger disabled={!!busy} onClick={()=>disconnect(id)}>{busy===id?'断开中…':'断开'}</Button></div>})}</div>
 </Card>}
 <Card title="已授权设备" eyebrow={targets.length+' TARGETS'}><div className="target-list">{targets.map(x=>{const id=x.deviceId||x.id,active=activeByID.get(id);return <div className="target-row rdp-authorized-row" key={id}><div className="identity-icon small">▣</div><div className="grow"><strong>{x.name||id}</strong><div className="mono mini">{id}</div></div>{active&&<Badge tone="blue">已建立入口</Badge>}<Badge tone={x.online?'ok':'neutral'}>{x.online?'在线':'离线'}</Badge><Button primary disabled={!x.online||!status.connected||!!busy} title={active?'复用现有本地端口，再次唤起远程桌面客户端':'连接此设备'} onClick={()=>connect(id)}>{busy===id?'打开中…':active?'再次打开':'连接'}</Button></div>})}{!targets.length&&<div className="empty">{!desiredEnabled?'本机 RDP 功能已关闭。':!runtimeEnabled?'RDP 设置等待重启生效。':status.connected&&!controllerApproved?'当前设备未获得 Server 的 rdp.controller 授权。':'当前没有 Server 授权的 RDP 目标设备。'}</div>}</div></Card>
 <Card title="远程桌面偏好" eyebrow="PREFERENCES" action={<Badge tone={controllerApproved?'ok':status.connected?'warn':'neutral'}>{controllerApproved?'控制端已授权':status.connected?'控制端未授权':'未连接'}</Badge>}>
  <Setting title="启用远程桌面" desc={runtimeEnabled?'Agent 正在向 Server 申请 rdp.controller 能力。关闭后保存并重启客户端生效。':'当前进程未启用 RDP；开启后保存并重启客户端，Agent 才会申请 rdp.controller。'}><Switch checked={desiredEnabled} onChange={v=>setv('rdp.enabled',v)} label="启用远程桌面"/></Setting>
  {dirty&&<div className="actions end top-gap"><Button primary onClick={()=>save({rdp:{enabled:desiredEnabled}})}>保存 RDP 偏好</Button></div>}
  {rdpRestartPending&&<div className="actions end top-gap"><Button primary onClick={onRestart}>重启客户端应用 RDP 设置</Button></div>}
 </Card></>
}

function MessagesPage({messages,refresh,clear,toast,config,setv,save,dirty}){
 const[filter,setFilter]=useState('all'),[query,setQuery]=useState(''),[preview,setPreview]=useState('');
 const timeout=Number(config?.verificationPopupTimeoutSec??15);
 const classified=m=>({code:!!m.verificationCode||m.messageType==='verification_code'||m.popupType==='verification_code',important:m.messageType==='important'||m.popupType==='important'});
 const q=query.trim().toLowerCase();
 const filtered=messages.filter(m=>{const type=classified(m),typeOK=filter==='all'||(filter==='code'&&type.code)||(filter==='important'&&type.important)||(filter==='normal'&&!type.code&&!type.important);if(!typeOK)return false;if(!q)return true;return [m.title,m.content,m.source,m.verificationCode,m.messageType,m.popupType,m.messageRule,m.verificationRule].some(v=>String(v||'').toLowerCase().includes(q))});
 const copy=async t=>{if(!t)return;if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',String(t));else await navigator.clipboard.writeText(String(t));toast('已复制')};
 return <><PageHead title="消息中心" desc="验证码、普通消息与重要提醒；匹配规则由 Server 管理，Agent 负责展示、弹窗和历史。"/>
 <div className="filter-bar message-filter-compact"><label className="message-type-filter"><span className="mini">类型</span><Select aria-label="消息类型" value={filter} onChange={e=>setFilter(e.target.value)}><option value="all">全部消息</option><option value="code">验证码</option><option value="normal">普通消息</option><option value="important">重要提醒</option></Select></label><div className="message-search"><Input value={query} onChange={e=>setQuery(e.target.value)} placeholder="搜索标题、内容、来源、规则或验证码"/><span className="mini">{filtered.length} 条</span></div><div className="message-filter-actions" aria-label="消息操作"><Button onClick={refresh}>刷新</Button><Button danger disabled={!messages.length} onClick={clear}>清空历史</Button></div></div>
 <div className="message-list">{filtered.slice().reverse().map((m,i)=>{const type=classified(m),code=type.code,important=type.important,msgType=m.messageType||m.popupType||(code?'verification_code':important?'important':'message'),rule=m.messageRule||m.verificationRule||'未标注规则';return <article className={cx('message-card',important&&'important')} key={m.id||i}><div className={cx('message-icon',code?'code':important?'important':'')}>{code?'#':important?'!':'✦'}</div><div className="grow"><div className="row between"><strong>{m.title||(code?'验证码':important?'重要消息':'消息')}</strong><div className="actions"><Badge tone={code?'ok':important?'danger':'blue'}>{code?'验证码':important?'重要':'通知'}</Badge><Badge tone={m.popup?'ok':'neutral'}>{m.popup?'弹窗':'仅历史'}</Badge></div></div><div className="mini">{[m.source||'RelayProxy',rule,fmtTime(m.createdAt)].filter(Boolean).join(' · ')}</div><div className="chips top-gap"><span className="chip mono">{msgType}</span>{m.popupType&&m.popupType!==msgType&&<span className="chip mono">compat · {m.popupType}</span>}</div><p>{m.content||''}</p>{m.verificationCode&&<button className="code-box" onClick={()=>copy(m.verificationCode)}>{m.verificationCode}<small>点击复制</small></button>}</div></article>})}{!filtered.length&&<Card><div className="empty">{messages.length?'没有符合当前筛选条件的消息。':'暂无消息。'}</div></Card>}</div>
 <Card title="弹窗偏好" eyebrow="PREFERENCES">
  <Setting title="消息弹窗自动关闭" desc="0 表示不自动关闭；支持的桌面端使用系统原生弹窗。匹配规则、消息类型与验证码提取由 Relay Server 下发。"><div className="row"><Input type="number" min="0" max="3600" aria-label="弹窗自动关闭秒数" value={timeout} onChange={e=>setv('verificationPopupTimeoutSec',Number(e.target.value))}/><span className="mini">秒</span></div></Setting>
  <Setting title="预览弹窗样式" desc="按消息类型查看本平台的弹窗呈现。"><div className="actions"><Button quiet onClick={()=>setPreview('verification')}>验证码</Button><Button quiet onClick={()=>setPreview('message')}>普通消息</Button><Button quiet onClick={()=>setPreview('important')}>重要提醒</Button></div></Setting>
  {dirty&&<div className="actions end top-gap"><Button primary onClick={()=>save({gui:{verificationPopupTimeoutSec:Number(config.verificationPopupTimeoutSec)}})}>保存弹窗偏好</Button></div>}
 </Card>
 <Modal open={!!preview} title={preview==='verification'?'验证码弹窗':preview==='important'?'重要提醒弹窗':'普通消息弹窗'} onClose={()=>setPreview('')} footer={<Button primary onClick={()=>setPreview('')}>关闭预览</Button>}><PopupPreview type={preview}/></Modal>
 </>
}

function MonitorPage({connections: snapshot,history,exits,customExits,refresh,clear,openNative}){
 const[query,setQuery]=useState(''),[stateFilter,setStateFilter]=useState('all'),[protocolFilter,setProtocolFilter]=useState('all'),[actionFilter,setActionFilter]=useState('all'),[visibleLimit,setVisibleLimit]=useState(100);
 useEffect(()=>setVisibleLimit(100),[query,stateFilter,protocolFilter,actionFilter]);
 const allRows=arr(snapshot?.connections);
 const inventory=[...arr(exits),...arr(customExits)];
 const q=query.trim().toLowerCase();
 const rows=allRows.filter(x=>{const state=String(x.state||'').toLowerCase(),protocol=String(x.protocol||'').toLowerCase(),action=String(x.action||'').toUpperCase();const stateOK=stateFilter==='all'||(stateFilter==='active'&&state==='active')||(stateFilter==='connecting'&&state==='connecting')||(stateFilter==='ended'&&/closed|ended|done/.test(state))||(stateFilter==='error'&&(/error|failed|reject|blocked/.test(state)||!!x.error));const protocolOK=protocolFilter==='all'||protocol===protocolFilter;const actionOK=actionFilter==='all'||action===actionFilter;if(!stateOK||!protocolOK||!actionOK)return false;if(!q)return true;return [x.process_name,x.process,x.host,x.ip,x.rule,x.exit_id,x.path,x.source,x.entry,connectionPathLabel(x,inventory)].some(v=>String(v||'').toLowerCase().includes(q))});
 const visibleRows=rows.slice(0,visibleLimit);
 const active=Number(snapshot?.active??allRows.filter(x=>/active|connecting/i.test(x.state||'')).length);
 const total=Number(snapshot?.total??allRows.length),omitted=Number(snapshot?.omitted||0);
 const totalBytes=Number(snapshot?.upload||0)+Number(snapshot?.download||0),rate=Number(snapshot?.upload_rate||0)+Number(snapshot?.download_rate||0);
 const targetText=x=>{const host=x.host||x.ip||'—',port=Number(x.port||0);return port?host+':'+port:host};
 const pathText=x=>connectionPathLabel(x,inventory);
 const hitText=x=>({'global_proxy':'全局代理','direct':'全局直连','default':'默认动作','loop-guard':'回环保护','relay-unavailable':'Relay 不可用','fakeip-dns':'本机 FakeIP DNS','fakeip-expired':'FakeIP 映射失效','fakeip-dns-exit-changed':'DNS 出口变更，FakeIP 缓存已失效','fakeip-disabled':'FakeIP 已关闭','fakeip-direct-unsupported':'FakeIP 不支持直连','fakeip-proxy-unavailable':'FakeIP 上游不可用','doh-endpoint-blocked':'已知 DoH 解析器阻断','doh-ip-endpoint-blocked':'DoH 目标 IP 阻断'}[x.rule]||x.rule||'未记录');
 return <><PageHead title="实时监控" desc="观察当前连接、进程、目标、规则、动作、出口、路径与流量。" actions={<><Button onClick={refresh}>刷新</Button><Button onClick={openNative}>独立窗口</Button><Button danger onClick={clear}>清理历史</Button></>}/>
 <Card title="连接列表" eyebrow={visibleRows.length+' / '+rows.length+' 条显示 · '+allRows.length+' 条记录'}><div className="monitor-filters" role="search" aria-label="连接筛选"><Input aria-label="搜索进程、目标、规则或出口" value={query} onChange={e=>setQuery(e.target.value)} placeholder="搜索进程 / 目标 / 规则 / 出口"/><Select aria-label="连接状态" value={stateFilter} onChange={e=>setStateFilter(e.target.value)}><option value="all">全部状态</option><option value="connecting">连接中</option><option value="active">活跃</option><option value="ended">已结束</option><option value="error">失败 / 阻断</option></Select><Select aria-label="连接协议" value={protocolFilter} onChange={e=>setProtocolFilter(e.target.value)}><option value="all">全部协议</option><option value="tcp">TCP</option><option value="udp">UDP</option></Select><Select aria-label="连接动作" value={actionFilter} onChange={e=>setActionFilter(e.target.value)}><option value="all">全部动作</option><option value="PROXY">代理</option><option value="DIRECT">直连</option><option value="REJECT">阻断</option></Select></div><div className="conn-table" role="region" aria-label="实时连接列表" tabIndex={0}><div className="conn-row conn-head"><span>应用 / 目标</span><span>连接路径</span><span>命中规则</span><span>状态</span><span>流量 / 速率</span><span>开始时间</span></div>{visibleRows.map((x,i)=>{const proc=x.process_name||x.process||'未知进程',target=targetText(x),path=pathText(x),state=x.state||'—',up=Number(x.upload||0),down=Number(x.download||0),upRate=Number(x.upload_rate||0),downRate=Number(x.download_rate||0);return <div className="conn-row" key={x.id||i}><div><strong>{proc}</strong><div className="mono mini">{target} · {(x.protocol||'tcp').toUpperCase()}</div>{x.host&&<div className="mini">IP {x.ip||'—'} · 域名来源：{{'dns':'DNS','tls-sni':'TLS SNI','http-host':'HTTP Host','network-extension':'系统网络扩展','requested':'应用请求','fakeip':'FakeIP DNS 映射'}[x.domain_source]||x.domain_source||'未知'}</div>}{x.error&&<div className="conn-error">{x.error}</div>}</div><div><span>{path}</span><div className="mini">{x.entry||x.source||''}</div></div><div><span className={cx('rule-hit',!x.rule&&'muted')} title={'本连接路由决策：'+hitText(x)}>{hitText(x)}</span></div><Badge tone={tone(state)}>{state}</Badge><div className="mono"><div>{fmtBytes(down)} ↓ · {fmtBytes(up)} ↑</div><div className="mini">{fmtBytes(downRate,true)} ↓ · {fmtBytes(upRate,true)} ↑</div></div><span className="mini">{fmtTime(x.started_at)}</span></div>})}{!rows.length&&<div className="empty">{allRows.length?'没有符合当前筛选条件的连接。':'暂无连接记录。'}</div>}</div>{rows.length>visibleLimit&&<div className="monitor-load-more"><span className="mini">已显示 {visibleRows.length} / {rows.length} 条匹配连接</span><Button onClick={()=>setVisibleLimit(n=>n+100)}>显示更多（剩余 {rows.length-visibleRows.length} 条）</Button></div>}</Card>
 <details className="monitor-stats-disclosure"><summary><strong>流量统计与趋势</strong><span className="mini">{active} 条活跃 · {fmtBytes(rate,true)} 当前吞吐 · {fmtBytes(totalBytes)} 累计</span></summary>
<div className="cards-4"><Metric label="活跃连接" value={active} hint={omitted?'另有 '+omitted+' 条未列出':'当前活动流'} series={history.active}/><Metric label="累计连接" value={total} hint="本进程计数窗口"/><Metric label="实时吞吐" value={fmtBytes(rate,true)} hint={fmtBytes(snapshot?.download_rate||0,true)+' ↓ · '+fmtBytes(snapshot?.upload_rate||0,true)+' ↑'} series={history.total}/><Metric label="累计流量" value={fmtBytes(totalBytes)} hint={fmtBytes(snapshot?.download||0)+' ↓ · '+fmtBytes(snapshot?.upload||0)+' ↑'}/></div>
 </details></>
}

function DiagnosticsPage({diagnostics,logs,exits,customExits,refreshDiagnostics,clearLogs,copyLogs}){
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
 <Card title="活动连接" eyebrow="ACTIVE CONNECTIONS" action={<span className="mini">{diagConnections.length} 条活动 · Exit TCP {exitTCP.length} · 采样 {fmtTime(diagnostics?.sampledAt||diagnostics?.exit?.sampled_at)}</span>}>{diagConnections.length===0?<div className="muted">当前没有活动连接</div>:<div className="diag-connections">{diagConnections.slice(0,8).map((x,i)=><div className="diag-conn" key={x.id||i}><div className="grow"><strong>{x.process_name||x.process||'未知进程'}</strong><div className="mono mini">{(x.host||x.ip||'—')+(x.port?':'+x.port:'')}</div></div><div><b>{fmtBytes(x.download_rate||0,true)} ↓</b><div className="mini">{connectionPathLabel(x,[...arr(exits),...arr(customExits)])}</div></div></div>)}</div>}</Card>
 <div className="cards-2">
  <Card title="运行建议" eyebrow="HEALTH"><div className="stack">{!st.connected&&<div className="notice danger">控制隧道当前未连接，请先检查 Server 地址、端口、TLS 与网络连通性。</div>}{quicLoss>=3&&<div className="notice warn">当前 QUIC 发送丢包为 {quicLoss.toFixed(2)}%，建议比较 Public Direct / P2P / Relay 路径并运行出口测速。</div>}{st.divertError&&<div className="notice danger">透明代理错误：{st.divertError}</div>}{!st.divertError&&<div className="notice">透明代理异常时，请检查 Network Service、WinDivert 驱动和管理员安装状态。</div>}<div className="notice">Direct/P2P 出现持续回落时，结合出口测速、候选地址和日志分类确认瓶颈。</div></div></Card>
  <Card title="详细诊断数据" eyebrow="RAW SNAPSHOT"><details className="details"><summary>展开原始诊断快照</summary><pre className="diagnostic-json">{JSON.stringify(diagnostics||{},null,2)}</pre></details></Card>
 </div>
 <Card title="运行日志" eyebrow={visible.length+' / '+logs.length+' LINES'}><div className="log-toolbar"><div className="segment">{[['all','全部'],['relay','中继'],['p2p','P2P'],['proxy','代理'],['exit','出口']].map(x=><button key={x[0]} className={filter===x[0]?'active':''} onClick={()=>setFilter(x[0])}>{x[1]}</button>)}</div><span className="mini">{filter==='all'?'显示全部日志':'仅显示匹配分类的日志'}</span></div><div className="log-console">{visible.length?visible.map((x,i)=><div key={i} className={cx(/error|failed|失败|fatal|timeout/i.test(x)&&'log-error',/warn|retry|警告/i.test(x)&&'log-warn')}>{x}</div>):<div className="muted">当前分类暂无日志</div>}</div></Card></>
}

function SettingsPage({config,setv,save,toast,onReload,onRestart,onQuit}){
 const auto=async v=>{try{const r=parseMutation(await call('goSetAutostart',v));if(!r.ok&&r.message)throw new Error(r.message);setv('isAutostart',v,false);toast(v?'已启用开机自启':'已关闭开机自启')}catch(e){toast(e.message,'danger')}};
 return <><PageHead title="设置" desc="外观、窗口行为、开机自启与客户端维护。网络相关设置都在左侧「网络」分组。"/>
 {config.restartRequired&&<div className="notice warn">以下设置需要重启客户端后生效：{arr(config.restartFields).join('、')||'启动参数'}。</div>}
 {config.reloadPending&&<div className="notice warn">配置文件中的策略与当前运行状态不同，可重新读取并应用。</div>}
 <div className="cards-2"><Card title="外观与窗口" eyebrow="APPEARANCE"><Setting title="主题" desc="跟随当前操作系统的外观设置"><Select value={config.theme||'system'} onChange={e=>setv('theme',e.target.value)}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></Select></Setting><Setting title="关闭时最小化到托盘" desc="关闭主窗口后 Agent 保持运行"><Switch checked={!!config.minimizeToTray} onChange={v=>setv('minimizeToTray',v)}/></Setting><div className="actions end top-gap"><Button primary onClick={()=>save({gui:{theme:config.theme,minimizeToTray:config.minimizeToTray}})}>保存界面设置</Button></div></Card>
 <Card title="启动与维护" eyebrow="APPLICATION"><Setting title="开机自启" desc="启动能力及所需权限由当前操作系统决定"><Switch checked={!!config.isAutostart} onChange={auto}/></Setting><Setting title="配置文件" desc={config.configPath||'未指定'}><Button onClick={()=>call('goOpenConfigDir')}>打开目录</Button></Setting><Setting title="版本" desc="RelayProxy Agent"><span className="mono">{config.version||'dev'}</span></Setting><div className="actions top-gap"><Button onClick={onReload}>重新读取并应用</Button><Button onClick={onRestart}>重启客户端</Button><Button danger onClick={onQuit}>退出</Button></div></Card></div></>
}

function PopupPreview({type}){
 const isCode=type==='verification',important=type==='important';
 return <div className={cx('popup-preview',important&&'important')}><div className="popup-preview-head"><div className={cx('message-icon',isCode?'code':important?'important':'')}>{isCode?'#':important?'!':'✦'}</div><div><div className="eyebrow">{important?'IMPORTANT':isCode?'VERIFICATION CODE':'MESSAGE'}</div><strong>{important?'重要提醒':isCode?'收到新的验证码':'收到新消息'}</strong></div></div><div className="mini top-gap">来自 RelayProxy · 刚刚</div>{isCode&&<div className="preview-code">482913</div>}<p>{important?'Tokyo 出口已从 Public Direct 降级至 Relay，请检查公网直连状态。':isCode?'用于登录验证，请勿向他人泄露。':'设备审批已完成，新的授权能力已同步到本机。'}</p><div className="actions end"><Button>{isCode?'稍后处理':'知道了'}</Button>{isCode&&<Button primary>复制验证码</Button>}</div></div>
}

function DeviceSpeedLinkDetails({exitID,exits,result,status,phase,revealCount}){
 const matched=arr(exits).find(x=>(x.deviceId||x.id)===exitID);
 const online=status?.connected===true;
 const rows=deviceSpeedChecks({exitID,exit:matched,connected:online,phase,result,revealCount});
 const udp=Number(status?.nativeUdp?.associations||0);
 const stateLabel={checked:'通过',progress:'检测中',waiting:'待检测',candidate:'仅候选',unknown:'未确认',failed:'失败'};
 const [checkExpanded,setCheckExpanded]=useState(true);
 const passedCount=rows.filter(row=>row.state==='checked').length;
 const summary=phase==='running'?'测速中':phase==='reviewing'?'结果核验中':phase==='failed'?'检测失败':phase==='done'?passedCount+'/'+rows.length+' 项通过':passedCount+'/'+rows.length+' 项已确认';
 return <section className="device-speed-evidence" aria-label="该设备链路检测进度">
  <button type="button" className="device-speed-check-toggle" aria-expanded={checkExpanded} aria-controls="device-speed-check-content" onClick={()=>setCheckExpanded(open=>!open)}>
   <strong>链路逐项检查</strong><span className="device-speed-check-summary" role="status" aria-live="polite">{summary}</span><span className="device-speed-check-chevron" aria-hidden="true">{checkExpanded?'⌄':'›'}</span>
  </button>
  {checkExpanded&&<div id="device-speed-check-content" className="device-speed-check-content">
  <div className="mini">目标：{matched?.name||matched?.deviceName||exitID||'未指定设备'} · 本次测速未使用的路径仅代表未确认，不代表不可用。</div>
  <div className="device-speed-check-list" role="list">
   {rows.map(row=><div key={row.id} role="listitem" className={'device-speed-check is-'+row.state} title={row.label+' · '+row.detail} aria-label={row.label+'，'+row.detail+'，'+stateLabel[row.state]}>
    <span className="device-speed-check-icon" aria-hidden="true">{row.state==='checked'?'✓':row.state==='progress'?'◌':row.state==='failed'?'×':row.state==='waiting'?'·':row.state==='candidate'?'i':'–'}</span>
    <strong className="device-speed-check-label">{row.label}</strong>
    <span className="device-speed-check-detail">{row.detail}</span>
    <span className="device-speed-check-state">{stateLabel[row.state]}</span>
   </div>)}
  </div>
  <div className="mini device-speed-check-note" role="status" aria-live="polite">{phase==='running'?'正在对该设备执行双向测速，链路结果尚未返回。':phase==='reviewing'?'测速已结束，正在逐项核对结果。':phase==='done'?'检测结果已逐项核对完成。':phase==='failed'?'测速失败，未确认的链路不会标记为通过。':'准备就绪，可开始检测所选设备。'}</div>
  <details className="device-speed-local">
   <summary>本机网络环境（不是远程设备测速结果）</summary>
   <div className="device-speed-path-list">
    <Setting title="本机 UPnP 映射" desc={status?.upnpError||status?.upnpAddress||'仅反映本机网关；映射成功不代表远端设备可达'}><Badge tone={status?.upnpState==='MAPPED'?'ok':status?.upnpState==='FAILED'||status?.upnpState==='DEGRADED'?'danger':'neutral'}>{status?.upnpState||'DISABLED'}</Badge></Setting>
    <Setting title="本机 Native UDP" desc="当前活动的数据报 Association">{udp+' 个'}</Setting>
    <Setting title="Server Relay 控制会话" desc="仅代表 Agent 控制连接，并非这台设备测速路径"><Badge tone={online?'blue':'neutral'}>{online?'在线':'离线'}</Badge></Setting>
    {arr(status?.rdpSessions).filter(s=>s.deviceId===exitID).map(s=><React.Fragment key={s.deviceId}><Setting title="RDP TCP 路径" desc="这台设备的独立 RDP 会话">{rdpPathLabel(s.pathTcp)}</Setting><Setting title="RDP UDP 路径" desc={s.udpReason||'这台设备的独立 RDP 会话'}>{rdpPathLabel(s.pathUdp)}</Setting></React.Fragment>)}
   </div>
  </details>
  </div>}
 </section>;
}

function SpeedModal({open,exits,status,initialExit,onClose,toast}){
 // Device Center is the source of truth. Never silently use the automatic
 // default exit when the requested device is offline or has lost authorization.
 const exitID=initialExit||'';
 const matched=arr(exits).find(x=>(x.deviceId||x.id)===exitID);
 const exitReady=!!status?.connected&&exitUsable(exits,exitID);
 const[duration,setDuration]=useState(3),[busy,setBusy]=useState(false),[result,setResult]=useState(null),[calibration,setCalibration]=useState(null),[calibrateStep,setCalibrateStep]=useState(0);
 const[phase,setPhase]=useState('idle'),[revealCount,setRevealCount]=useState(0);
 useEffect(()=>{if(!open)return;setResult(null);setCalibration(null);setCalibrateStep(0);setPhase('idle');setRevealCount(0)},[open,exitID]);
 // The speed RPC has no streamed sub-step results. After the actual response
 // returns, check and reveal each evidence row without fake probe timings.
 useEffect(()=>{
  if(phase!=='reviewing')return;
  if(revealCount>=DEVICE_SPEED_REVIEW_STEPS){setPhase('done');return}
  const id=setTimeout(()=>setRevealCount(n=>n+1),190);
  return()=>clearTimeout(id);
 },[phase,revealCount]);
 const close=()=>{if(!busy&&phase!=='reviewing')onClose()};
 const run=async()=>{if(busy||!exitReady)return;const requested=exitID;setBusy(true);setPhase('running');setRevealCount(0);setResult(null);setCalibration(null);try{
  const p=parseMutation(await call('goRunSpeedTest',requested,Number(duration)));
  if(!p.ok)throw new Error(p.message||'测速失败');
  const measurement=p.result||p;
  if(measurement.exitId!==requested)throw new Error('测速返回的出口与所选设备不一致，不能归属链路结果');
  if(!(measurement.upload||measurement.up)||!(measurement.download||measurement.down))throw new Error('测速结果缺少完整的上下行数据，链路未确认');
  setResult(measurement);setPhase('reviewing');toast('出口测速完成');
 }catch(e){setResult({error:e.message,exitId:requested});setPhase('failed')}finally{setBusy(false)}};
 const calibrate=async()=>{if(busy||!exitReady)return;setBusy(true);setPhase('running');setRevealCount(0);setCalibration(null);setResult(null);setCalibrateStep(0);try{
  const records=[];const requested=exitID;
  for(let i=0;i<BRUTAL_CALIBRATION_ROUNDS;i++){
   const payload=parseMutation(await call('goRunSpeedTest',requested,3));
   if(!payload.ok)throw new Error(payload.message||'BBR 测速失败');
   const measurement=payload.result||payload;
   if(measurement.exitId!==requested)throw new Error('出口在测速期间发生切换');
   validateBrutalCalibrationSample(measurement);
   records.push(measurement);
   if(i>0&&records[0].exitId!==measurement.exitId)throw new Error('出口在测速期间发生切换');
   setCalibrateStep(i+1);
  }
  setCalibration(proposeBrutalFromSpeedTests(records));setResult(records.at(-1));setPhase('reviewing');toast('BBR 保守带宽候选值已计算');
 }catch(e){setCalibration({error:e.message});setResult({error:e.message,exitId});setPhase('failed');toast('未能生成安全候选值','danger')}finally{setBusy(false)}};
 const leg=(label,v={})=><div><div className="eyebrow">{label}</div><div className="big small-big">{Number(v.megabitsPerSecond??v.mbps??0).toFixed(2)} Mbps</div><div className="mini">{(v.path||'—')+' · '+fmtBytes(v.bytesPerSecond||0,true)}</div>{v.quic&&<div className="mini">丢包 {Number(v.quic.sent_packet_loss_pct||0).toFixed(2)}% · RTT {Number(v.quic.smoothed_rtt_ms||v.quic.latest_rtt_ms||0).toFixed(1)} ms · RTT 抖动 {Number(v.quic.rtt_deviation_ms||0).toFixed(1)} ms · GSO {v.quic.gso?'ON':'OFF'}{v.quic.congestion_controller?' · '+v.quic.congestion_controller:''}{v.quic.brutal_fallback_reason&&<div className="notice warn">Brutal 已自动退回 BBR：{v.quic.brutal_fallback_reason}</div>}{(v.quic.udp_read_buffer_bytes||v.quic.udp_write_buffer_bytes)?<div>UDP 缓冲：读取 {fmtBytes(v.quic.udp_read_buffer_bytes||0)} · 写入 {fmtBytes(v.quic.udp_write_buffer_bytes||0)}</div>:null}</div>}</div>;
 const reviewing=phase==='reviewing';
 return <Modal open={open} title="出口双向测速 · 设备链路信息" wide onClose={close} footer={<><Button disabled={busy||reviewing} onClick={close}>关闭</Button><Button disabled={busy||reviewing||!exitReady} onClick={calibrate}>{busy&&calibrateStep?'校准中 '+calibrateStep+'/'+BRUTAL_CALIBRATION_ROUNDS:'5 轮 BBR 保守校准'}</Button><Button primary disabled={busy||reviewing||!exitReady} onClick={run}>{busy?'测速中…':reviewing?'核验中…':'开始测速'}</Button></>}>
  <div className="device-speed-target"><div className="device-speed-target-identity"><Icon name="devices" size={18}/><div><span className="mini">当前测速设备（由设备卡片指定）</span><strong>{matched?.name||matched?.deviceName||exitID||'未选择设备'}</strong><span className="mono mini">{exitID||'无设备 ID'}</span></div></div><Field label="每方向时长（秒）"><Input type="number" min="1" max="30" disabled={busy||reviewing} value={duration} onChange={e=>setDuration(Number(e.target.value))}/></Field></div>
  {!exitReady&&<div className="notice warn" role="alert">该设备当前不在线或未获得代理出口授权，已阻止自动回退到其他出口。请返回「出口」页刷新授权。</div>}
  <DeviceSpeedLinkDetails exitID={exitID} exits={exits} result={result} status={status} phase={phase} revealCount={revealCount}/>
  {calibration&&<div className={cx('speed-result',calibration.error&&'error')}>{calibration.error?calibration.error:<><div className="notice warn">仅验证本机 BBR 发送侧，无法确认服务端发送侧的控制器或真实链路容量；共享公平性、负载 RTT 与 Hysteria2 实网对照仍须单独验收。本功能不会应用配置。</div><div>当前 Relay BBR 五轮候选：上行 <b>{calibration.upload.candidateMbps} Mbps</b> · 下行 <b>{calibration.download.candidateMbps} Mbps</b></div><div className="mini">最低分位吞吐：上行 {calibration.upload.bbrP10Mbps} Mbps / 下行 {calibration.download.bbrP10Mbps} Mbps；预留 40% 带宽余量。</div></>}</div>}
  {result&&<div className={cx('speed-result',result.error&&'error')}>{result.error?result.error:<>{leg('上行',result.upload||result.up)}{leg('下行',result.download||result.down)}</>}</div>}
 </Modal>;
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
 const[page,setPage]=useState(initialPage),[transferPanelOpen,setTransferPanelOpen]=useState(false),[transferSummary,setTransferSummary]=useState({running:0,total:0}),[status,setStatus]=useState({connected:false,proxyExits:[]}),[config,setConfig]=useState(EMPTY),[loaded,setLoaded]=useState(false),[configError,setConfigError]=useState(''),[confirmQuit,setConfirmQuit]=useState(false),[dirty,setDirty]=useState(false),[pendingAction,setPendingAction]=useState(null),[exits,setExits]=useState([]),[exitsReady,setExitsReady]=useState(false),[targets,setTargets]=useState([]),[filePeers,setFilePeers]=useState([]),[fileTarget,setFileTarget]=useState(null),[messages,setMessages]=useState([]),[unreadMessages,setUnreadMessages]=useState(0),[connections,setConnections]=useState({connections:[],active:0,total:0,omitted:0,upload:0,download:0,upload_rate:0,download_rate:0}),[logs,setLogs]=useState([]),[diagnostics,setDiagnostics]=useState({}),[toastState,setToastState]=useState(null),[speedOpen,setSpeedOpen]=useState(false),[speedExit,setSpeedExit]=useState(''),[batchSpeedOpen,setBatchSpeedOpen]=useState(false),[history,setHistory]=useState({down:[],up:[],total:[],active:[],latency:[]});
 const syncTransferSummary=useCallback((running,total)=>setTransferSummary(old=>old.running===running&&old.total===total?old:{running,total}),[]);
 const pushHistory=useCallback(patch=>setHistory(h=>{const n={...h};for(const k in patch)n[k]=[...h[k],Number(patch[k])||0].slice(-SPARK_POINTS);return n}),[]);
 const applyStatus=useCallback(x=>{setStatus(x||{});pushHistory({latency:x?.connected?x.latency:0})},[pushHistory]);
 const toastTimer=useRef(null),exitInventoryKey=useRef(''),pageRef=useRef(page),messageIDs=useRef(new Set());
 const toast=useCallback((message,t='normal')=>{clearTimeout(toastTimer.current);setToastState({message,t});toastTimer.current=setTimeout(()=>setToastState(null),3200)},[]);
 const theme=useCallback(mode=>{const m=['light','dark','system'].includes(mode)?mode:'system',dark=m==='dark'||(m==='system'&&matchMedia('(prefers-color-scheme: dark)').matches);document.documentElement.dataset.themeMode=m;document.documentElement.dataset.theme=dark?'dark':'light';document.documentElement.classList.toggle('dark',dark)},[]);
 const syncExitInventory=useCallback(async x=>{const summary=arr(x?.proxyExits),key=makeExitInventoryKey(x);if(exitInventoryKey.current===key)return;exitInventoryKey.current=key;if(!hasBridge('goGetProxyExits')){setExits(summary);setExitsReady(true);return}try{setExits(arr(await callJSON('goGetProxyExits',[])));setExitsReady(true)}catch{setExits(summary);setExitsReady(true)}},[]);
 const refreshStatus=useCallback(async()=>{if(!hasBridge('goGetStatus'))return null;try{const x=await callJSON('goGetStatus',{});applyStatus(x);void syncExitInventory(x);return x}catch{return null}},[applyStatus,syncExitInventory]);
 const refreshConfig=useCallback(async()=>{try{const x=await callJSON('goGetConfig',null);if(!x||x.configError)throw new Error(x?.configError?('配置文件 '+(x.configPath||'路径未知')+' 读取失败：'+x.configError):'配置接口没有返回有效数据');setConfig({...EMPTY,...x,customExits:arr(x.customExits),socks5:{...EMPTY.socks5,...x.socks5},http:{...EMPTY.http,...x.http},rdp:{...EMPTY.rdp,...x.rdp},p2p:{...EMPTY.p2p,...x.p2p},exitUpstream:{...EMPTY.exitUpstream,...x.exitUpstream},network:{...EMPTY.network,...x.network},systemProxy:{...EMPTY.systemProxy,...x.systemProxy},routing:{...EMPTY.routing,...x.routing,rules:arr(x.routing?.rules),subscriptions:arr(x.routing?.subscriptions)}});theme(x.theme||'system');setLoaded(true);setConfigError('');setDirty(false);return true}catch(e){setConfigError(e?.message||'配置读取失败');return false}},[theme]);
 const refreshNetworkCapabilities=useCallback(async()=>{if(!hasBridge('goGetNetworkCapabilities'))return null;try{const x=await callJSON('goGetNetworkCapabilities',{});setConfig(p=>({...p,networkCapabilities:x||{}}));return x}catch{return null}},[]);
 const refreshExits=useCallback(async()=>{if(hasBridge('goGetProxyExits'))try{setExits(arr(await callJSON('goGetProxyExits',[])));setExitsReady(true)}catch(e){toast('读取授权出口失败：'+e.message,'danger')}},[toast]);
 const refreshTargets=useCallback(async()=>{if(hasBridge('goGetRDPTargets'))try{setTargets(arr(await callJSON('goGetRDPTargets',[])))}catch(e){setTargets([]);toast(e.message,'danger')}},[toast]);
 const refreshFilePeers=useCallback(async()=>{
  if(!hasBridge('goGetFileSharePeers')){setFilePeers([]);return}
  try{setFilePeers(onlyVerifiedFilePeers(await callJSON('goGetFileSharePeers',[])))}
  catch{setFilePeers([])}
 },[]);
 const refreshMessages=useCallback(async()=>{if(hasBridge('goGetMessages'))try{const items=arr(await callJSON('goGetMessages',[]));messageIDs.current=new Set(items.map(x=>x?.id).filter(Boolean));setMessages(items)}catch{}},[]);
 const refreshConnections=useCallback(async()=>{if(hasBridge('goGetConnections'))try{const x=await callJSON('goGetConnections',{connections:[]});const snap=Array.isArray(x)?{connections:x,active:x.filter(v=>/active|connecting/i.test(v.state||'')).length,total:x.length,omitted:0,upload:0,download:0,upload_rate:0,download_rate:0}:{connections:arr(x?.connections),active:Number(x?.active||0),total:Number(x?.total||0),omitted:Number(x?.omitted||0),upload:Number(x?.upload||0),download:Number(x?.download||0),upload_rate:Number(x?.upload_rate||0),download_rate:Number(x?.download_rate||0),sampled_at:x?.sampled_at,rate_window:x?.rate_window};setConnections(snap);pushHistory({down:snap.download_rate,up:snap.upload_rate,total:snap.download_rate+snap.upload_rate,active:snap.active})}catch{}},[pushHistory]);
 const refreshLogs=useCallback(async()=>{if(hasBridge('goGetLogs'))try{setLogs(arr(await callJSON('goGetLogs',[])).map(logText).filter(Boolean))}catch{}},[]);
 const refreshDiagnostics=useCallback(async()=>{if(hasBridge('goGetDiagnostics'))try{setDiagnostics(await callJSON('goGetDiagnostics',{}))}catch(e){toast(e.message,'danger')}},[toast]);

 useEffect(()=>{let disposed=false;(async()=>{await refreshConfig();if(!disposed)await Promise.all([refreshStatus(),refreshExits(),refreshTargets(),refreshMessages(),refreshConnections(),refreshLogs(),refreshDiagnostics()])})();const a=setInterval(refreshStatus,1500),b=setInterval(()=>{refreshConnections();refreshMessages();refreshLogs();refreshTargets()},2500);const mq=matchMedia('(prefers-color-scheme: dark)'),mh=()=>document.documentElement.dataset.themeMode==='system'&&theme('system');mq.addEventListener?.('change',mh);const uninstall=installNativeHooks({onStatus:x=>{if(typeof x==='string')try{x=JSON.parse(x)}catch{return}applyStatus(x);void syncExitInventory(x)},onLog:x=>setLogs(p=>[...p.slice(-1999),logText(x)].filter(Boolean)),onMessage:m=>{const id=m?.id||'',isNew=!!id&&!messageIDs.current.has(id);if(id)messageIDs.current.add(id);setMessages(p=>{if(!id)return[...p,m].filter(Boolean).slice(-500);const i=p.findIndex(x=>x.id===id);if(i<0)return[...p,m].slice(-500);const n=[...p];n[i]=m;return n});if(isNew&&pageRef.current!=='messages')setUnreadMessages(n=>Math.min(99,n+1))},onTheme:m=>{theme(m);setConfig(p=>({...p,theme:m}))},onAutostart:v=>setConfig(p=>({...p,isAutostart:!!v}))});return()=>{disposed=true;clearInterval(a);clearInterval(b);clearTimeout(toastTimer.current);mq.removeEventListener?.('change',mh);uninstall()}},[applyStatus,refreshConfig,refreshStatus,refreshExits,refreshTargets,refreshMessages,refreshConnections,refreshLogs,refreshDiagnostics,syncExitInventory,theme]);

 // Wails or the HTTP bridge may start after React mounts. Keep retrying
 // until settings are actually available instead of freezing on empty defaults.
 useEffect(()=>{if(loaded)return;const timer=setInterval(()=>{void refreshConfig()},3000);return()=>clearInterval(timer)},[loaded,refreshConfig]);
 useEffect(()=>{pageRef.current=page;sessionStorage.setItem('relayproxy-react-page',page);if(page==='exits'){refreshExits();refreshTargets();refreshFilePeers()}if(page==='rdp')refreshTargets();if(page==='messages'){setUnreadMessages(0);refreshMessages()}if(page==='monitor')refreshConnections();if(page==='diagnostics'){refreshDiagnostics();refreshLogs()}},[page,refreshExits,refreshTargets,refreshFilePeers,refreshMessages,refreshConnections,refreshDiagnostics,refreshLogs]);
 useEffect(()=>{
  if(page!=='exits')return;
  const timer=setInterval(()=>void refreshFilePeers(),5000);
  return()=>{clearInterval(timer);setFilePeers([])};
 },[page,refreshFilePeers]);
 useEffect(()=>theme(config.theme||'system'),[config.theme,theme]);

 const setv=useCallback((path,value,markDirty=true)=>{setConfig(prev=>{const next=structuredClone(prev),keys=path.split('.');let o=next;for(let i=0;i<keys.length-1;i++){o[keys[i]]=o[keys[i]]&&typeof o[keys[i]]==='object'?o[keys[i]]:{};o=o[keys[i]]}o[keys[keys.length-1]]=value;return next});if(markDirty)setDirty(true)},[]);
 const save=useCallback(async payload=>{if(!loaded||!config.revision){toast('配置尚未就绪，暂不能保存','danger');return false}try{const r=await saveConfig(config.revision,payload);if(!r.ok)throw new Error(r.message||'保存失败');await refreshConfig();await refreshStatus();toast(r.restartRequired?(r.message||'已保存，部分设置需重启'):(r.message||'已保存'));return true}catch(e){toast(e.message,'danger');return false}},[loaded,config.revision,toast,refreshConfig,refreshStatus]);
 const select=useCallback(async id=>{if(dirty){toast('存在未保存的修改，请先保存或放弃后再切换出口','warn');return}try{const r=parseMutation(await call('goSelectExit',id));if(!r.ok&&r.message)throw new Error(r.message);await refreshConfig();await refreshStatus();toast(id?'已切换到 '+exitName([...withExitGroups(exits,arr(config.routing?.device_groups)),...arr(config.customExits)],id):'已切换为自动选择')}catch(e){toast(e.message,'danger')}},[dirty,exits,config.customExits,config.routing?.device_groups,refreshConfig,refreshStatus,toast]);
 const clearMessages=useCallback(async()=>{if(!messages.length||!confirm('清空本设备的消息历史？'))return;try{await call('goClearMessages');messageIDs.current.clear();setMessages([]);setUnreadMessages(0);toast('消息历史已清空')}catch(e){toast('清空消息失败：'+(e?.message||String(e)),'danger')}},[messages.length,toast]);
 const clearConnections=useCallback(async()=>{await call('goClearConnections');await refreshConnections();toast('已清理结束连接历史')},[refreshConnections,toast]);
 const clearLogs=useCallback(async()=>{await call('goClearLogs');setLogs([]);toast('日志已清空')},[toast]);
 const copyLogs=useCallback(async()=>{const text=logs.join('\n');if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',text);else await navigator.clipboard.writeText(text);toast('日志已复制')},[logs,toast]);
 // Native menus and older bookmarks may still ask for pre-refactor page ids;
 // resolve them to the page that now owns that content.
 const requestPage=useCallback(raw=>{const next=resolvePage(raw)||'overview';if(next===page)return;if(dirty){setPendingAction({kind:'page',target:next});return}setFileTarget(null);setPage(next)},[dirty,page]);
 const openDeviceFiles=useCallback(deviceID=>{
  if(dirty){setPendingAction({kind:'page',target:'files',fileDevice:deviceID});return}
  setFileTarget({device:deviceID,serial:Date.now()});
  setPage('files');
 },[dirty]);
 const connectDeviceRDP=useCallback(async deviceID=>{
  const target=targets.find(t=>(t.deviceId||t.id)===deviceID);
  if(!status.connected||!target||target.online===false||config.rdp?.enabled===false||config.runtime?.rdp?.enabled===false||!arr(status.approvedCapabilities).includes('rdp.controller')){toast('远程桌面目标当前不可连接，请检查设备状态与控制端授权','warn');return}
  try{const result=parseMutation(await call('goConnectRDP',deviceID,true));
   if(!result.ok)throw new Error(result.message||'RDP 连接失败');
   await refreshStatus();toast(result.target?.hostsError?'远程桌面已连接，hosts 别名未写入：'+result.target.hostsError:arr(status.rdpSessions).some(s=>s.deviceId===deviceID)||status.rdpTargetId===deviceID?'已请求再次打开本地远程桌面':'已连接远程桌面',result.target?.hostsError?'warn':undefined);
   requestPage('rdp');
  }catch(e){toast(e.message||String(e),'danger')}
 },[targets,status,config.rdp,config.runtime,refreshStatus,requestPage,toast]);
 useEffect(()=>{window.relayNavigate=requestPage;return()=>{delete window.relayNavigate}},[requestPage]);
 const reloadConfig=useCallback(async()=>{let r;try{r=parseMutation(await call('goReloadConfig'))}catch(e){r={ok:false,message:e.message}}await refreshConfig();await refreshStatus();if(!r.ok){toast(r.message||'重新读取配置失败','danger');return}toast(r.message||'已重新读取配置')},[refreshConfig,refreshStatus,toast]);
 const restartApp=useCallback(async()=>{try{const r=parseMutation(await call('goRestart'));if(!r.ok)throw new Error(r.message||'重启客户端失败')}catch(e){toast(e.message,'danger')}},[toast]);
 const quitApp=useCallback(async()=>{try{await call('goQuit')}catch(e){toast('退出失败：'+(e?.message||String(e)),'danger')}},[toast]);
 const requestLifecycle=useCallback(kind=>{if(dirty){setPendingAction({kind});return}if(kind==='reload')void reloadConfig();else if(kind==='restart')void restartApp();else if(kind==='quit')setConfirmQuit(true)},[dirty,reloadConfig,restartApp,quitApp]);
 const discardAndContinue=useCallback(async()=>{const action=pendingAction;if(!action)return;setPendingAction(null);setDirty(false);if(action.kind==='page'){await refreshConfig();setFileTarget(action.fileDevice?{device:action.fileDevice,serial:Date.now()}:null);setPage(action.target);return}if(action.kind==='reload'){await reloadConfig();return}if(action.kind==='restart'){await restartApp();return}if(action.kind==='quit'){await quitApp()}},[pendingAction,refreshConfig,reloadConfig,restartApp,quitApp]);
 useEffect(()=>{const h=e=>{if(!dirty)return;e.preventDefault();e.returnValue=''};window.addEventListener('beforeunload',h);return()=>window.removeEventListener('beforeunload',h)},[dirty]);
 const selectedExit=status.selectedExit||config.defaultExitId||'';
 const exitUnavailable=selectedExitUnavailable({exitsReady,connected:!!status.connected,exits:[...withExitGroups(exits,arr(config.routing?.device_groups)),...arr(config.customExits)],selected:selectedExit});
 const approval=String(status.approvalState||'').toLowerCase();
 const approvalPending=/pending|wait|approval|待审批/.test(approval);
 const approvalRejected=/reject|denied|revoked|撤销|拒绝/.test(approval);
 const title=PAGE_NAMES[page]||'RelayProxy';
 const pageDesc=PAGE_DESC[page]||'';
 const groupExits=withExitGroups(exits,arr(config.routing?.device_groups));
 const refreshExitInventory=()=>{void Promise.all([refreshExits(),refreshTargets(),refreshFilePeers()])};
 const p={status,config,exits:groupExits,setv,save,toast,dirty};
 let content;
 if(page==='overview')content=<Overview {...p} traffic={connections} history={history} onRefresh={refreshStatus} onGoto={requestPage} onSave={save} onSelectExit={select}/>;
 else if(page==='exits')content=<ExitsPage {...p} exits={exits} targets={targets} filePeers={filePeers} selectedExit={selectedExit} onSelectExit={select} onSpeed={id=>{setSpeedExit(id);setSpeedOpen(true)}} onSpeedAll={()=>setBatchSpeedOpen(true)} onConnectRDP={connectDeviceRDP} onFiles={openDeviceFiles} onRefresh={refreshExitInventory}/>;
 else if(page==='routing')content=<RoutingPage {...p} onGoto={requestPage} onDiscard={async()=>{await refreshConfig();toast('已放弃分流规则草稿')}}/>;
 else if(page==='dns')content=<DNSPage {...p} onDiscard={async()=>{await refreshConfig();toast('已放弃 DNS 设置草稿')}}/>;
 else if(page==='access')content=<AccessPage {...p} onRefreshCapabilities={refreshNetworkCapabilities} onConfigRefresh={async()=>{await refreshConfig();await refreshStatus()}}/>;
 else if(page==='server')content=<ServerPage {...p} onRefresh={refreshStatus} onReload={()=>requestLifecycle('reload')}/>;
 else if(page==='exitshare')content=<ExitShare {...p}/>;
 else if(page==='files')content=null;
 else if(page==='rdp')content=<RDPPage status={status} config={config} targets={targets} refreshTargets={refreshTargets} refreshStatus={refreshStatus} toast={toast} setv={setv} save={save} dirty={dirty} onRestart={()=>requestLifecycle('restart')}/>;
 else if(page==='messages')content=<MessagesPage messages={messages} refresh={refreshMessages} clear={clearMessages} toast={toast} config={config} setv={setv} save={save} dirty={dirty}/>;
 else if(page==='monitor')content=<MonitorPage connections={connections} history={history} exits={exits} customExits={config.customExits} refresh={refreshConnections} clear={clearConnections} openNative={()=>call('goOpenConnections')}/>;
 else if(page==='diagnostics')content=<DiagnosticsPage diagnostics={diagnostics} logs={logs} exits={exits} customExits={config.customExits} refreshDiagnostics={refreshDiagnostics} clearLogs={clearLogs} copyLogs={copyLogs}/>;
 else content=<SettingsPage config={config} setv={setv} save={save} toast={toast} onReload={()=>requestLifecycle('reload')} onRestart={()=>requestLifecycle('restart')} onQuit={()=>requestLifecycle('quit')}/>;
 return <div className="app-shell">
 <aside className="sidebar">
  <div className="nav-scroll">{NAV.map(g=><div className="nav-group" key={g.group}><div className="group-name">{g.group}</div>{g.items.map(x=><button key={x[0]} type="button" title={x[3]||x[2]} aria-label={x[2]} aria-current={page===x[0]?'page':undefined} className={cx('nav-item',page===x[0]&&'active')} onClick={()=>requestPage(x[0])}><span className="nav-icon"><Icon name={x[1]}/></span><span>{x[2]}</span>{x[0]==='messages'&&unreadMessages>0&&<i className="nav-dot" title={unreadMessages+' 条新消息'}>{unreadMessages>9?'9+':unreadMessages}</i>}</button>)}</div>)}</div>
  <div className="sidebar-footer"><button type="button" title="外观、启动与客户端维护" aria-label="设置" aria-current={page==='settings'?'page':undefined} className={cx('nav-item',page==='settings'&&'active')} onClick={()=>requestPage('settings')}><span className="nav-icon"><Icon name="settings"/></span><span>设置</span></button><div className="connection-mini"><span className={cx('dot',status.connected?'online':'offline')}/><div><strong>{status.connected?'已连接':'未连接'}</strong><span>{status.connected?((status.latency||0)+' ms'):'—'}</span></div></div></div>
 </aside>
 <section className="workspace">
  <header className="topbar">
   <div className="crumb"><b>{title}</b>{pageDesc&&<span className="crumb-desc">{pageDesc}</span>}</div>
   <div className="top-actions">
    <ExitSwitcher exits={groupExits} customExits={config.customExits} selectedExit={selectedExit} connected={!!status.connected} dirty={dirty} onSelect={select}/>
    <button type="button" className="global-transfer-trigger" aria-label={'传输任务，'+transferSummary.running+' 项进行中'} aria-expanded={transferPanelOpen} onClick={()=>setTransferPanelOpen(open=>!open)}><Icon name="activity" size={16}/><span>传输任务</span>{transferSummary.running>0&&<span className="global-transfer-count">{transferSummary.running}</span>}</button>
    {dirty&&<Badge tone="blue">未保存</Badge>}{config.restartRequired&&<Badge tone="warn">需重启</Badge>}
   </div>
  </header>
  <main className="main">
   {approvalPending&&<div className="notice warn state-banner"><b>设备等待审批。</b> 请在 Relay Server 使用当前身份审批此设备所需能力。</div>}
   {approvalRejected&&<div className="notice danger state-banner"><b>设备授权不可用。</b> 当前设备可能已被拒绝或撤销，代理、出口与 RDP 能力会受限。</div>}
   {!loaded&&<div className="notice danger state-banner"><b>配置尚未加载。</b> {configError||'正在连接配置接口…'} <Button quiet onClick={()=>void refreshConfig()}>重试读取</Button></div>}
   {exitUnavailable&&<div className="notice warn state-banner"><b>已选出口不可用。</b> RelayProxy 不会静默改选其他出口；请在「出口」页重新选择默认出口，或切回自动选择。 <Button quiet onClick={()=>requestPage('exits')}>前往出口页</Button>{!dirty&&<Button quiet onClick={()=>select('')}>改为自动</Button>}</div>}
   {content}
   <FileSharingPage toast={toast} active={page==='files'} navigationTarget={fileTarget} transferOpen={transferPanelOpen} onTransferOpenChange={setTransferPanelOpen} onTransferSummary={syncTransferSummary}/>
  </main>
 </section>
 <div id="agent-file-transfer-portal"/>
 <SpeedModal open={speedOpen} exits={exits} status={status} initialExit={speedExit} onClose={()=>setSpeedOpen(false)} toast={toast}/>
 <BatchSpeedModal open={batchSpeedOpen} exits={exits} onClose={()=>setBatchSpeedOpen(false)} toast={toast}/>
 <Modal open={!!pendingAction} title="有未保存的修改" onClose={()=>setPendingAction(null)} footer={<><Button onClick={()=>setPendingAction(null)}>继续编辑</Button><Button danger onClick={discardAndContinue}>放弃修改并继续</Button></>}><div className="notice warn">当前页面存在尚未保存的配置草稿。离开、重载、重启或退出会丢失这些修改。</div></Modal>
 <Modal open={confirmQuit} title="退出 RelayProxy" onClose={()=>setConfirmQuit(false)} footer={<><Button onClick={()=>setConfirmQuit(false)}>取消</Button><Button danger onClick={()=>{setConfirmQuit(false);void quitApp()}}>确认退出</Button></>}><div className="notice">退出后将停止当前代理进程和连接。</div></Modal>
 {toastState&&<div className={cx('toast show',toastState.t==='danger'&&'danger',toastState.t==='warn'&&'warn')}>{toastState.message}</div>}
 </div>
}
