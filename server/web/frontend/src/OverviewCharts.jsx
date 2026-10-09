import React, {useEffect, useMemo, useRef, useState} from 'react';
import {api, fmtBytes, fmtDate} from './api.js';
import {Icon} from './icons.jsx';
import {SAMPLE_INTERVAL_MS, MAX_SAMPLES, appendSample, formatRate, nonnegative, recordTelemetry} from './telemetry.js';

const timeText = value => new Date(value).toLocaleTimeString('zh-CN', {hour12:false});
const CHART_SIZE = {width:740, height:236, left:65, right:18, top:15, bottom:29};
const TRAFFIC_SERIES=[
  {key:'uploadBps',name:'上行',color:'var(--rt-blue)'},
  {key:'downloadBps',name:'下行',color:'var(--rt-violet)'}
];
const SESSION_SERIES=[
  {key:'connections',name:'活跃连接',color:'var(--rt-blue)'},
  {key:'online',name:'在线设备',color:'var(--rt-teal)'},
  {key:'p2p',name:'P2P 会话',color:'var(--rt-violet)'}
];
const PATH_SERIES=[
  {key:'p2p',name:'P2P QUIC',color:'var(--rt-teal)'},
  {key:'publicDirect',name:'公网直连',color:'var(--rt-blue)'},
  {key:'relay',name:'Relay 路径',color:'var(--rt-violet)'},
  {key:'unknown',name:'未上报路径',color:'var(--rt-muted)'}
];

function useTelemetry() {
  const [current,setCurrent]=useState(null);
  const [history,setHistory]=useState([]);
  const [error,setError]=useState('');
  const [pending,setPending]=useState(true);
  const previous=useRef(null);
  const pollRef=useRef(()=>{});
  useEffect(()=>{
    let alive=true, fetching=false;
    const controller=new AbortController();
    const poll=async()=>{
      if(!alive || fetching || document.visibilityState==='hidden')return;
      fetching=true;
      try{
        const options={signal:controller.signal};
        const [dashboard,sessions,p2p]=await Promise.all([
          api('/dashboard',options),api('/sessions/active',options),api('/p2p/sessions',options)
        ]);
        if(!alive)return;
        const packet=recordTelemetry(dashboard,sessions,p2p,previous.current,Date.now());
        previous.current=packet;
        setCurrent(packet.sample);
        setHistory(old=>appendSample(old,packet.sample));
        setError('');
      }catch(err){
        if(alive && err.name!=='AbortError')setError(err.status===401?'登录已失效，请重新登录后查看实时数据':err.message||'实时数据读取失败');
      }finally{
        if(alive)setPending(false);
        fetching=false;
      }
    };
    pollRef.current=poll;
    void poll();
    const interval=setInterval(poll,SAMPLE_INTERVAL_MS);
    const onVisibility=()=>{if(document.visibilityState==='visible')void poll()};
    document.addEventListener('visibilitychange',onVisibility);
    return()=>{alive=false;pollRef.current=()=>{};clearInterval(interval);controller.abort();document.removeEventListener('visibilitychange',onVisibility)};
  },[]);
  return {current,history,error,pending,refresh:()=>pollRef.current()};
}

function ChartPanel({title,desc,extra,children,className=''}){
 return <section className={'card rt-panel '+className}><header className="rt-panel-header"><div><h2>{title}</h2><p>{desc}</p></div>{extra}</header>{children}</section>;
}

function TrendChart({samples,series,format=String,empty='正在收集第一个样本'}){
  const available=samples.filter(sample=>series.some(item=>sample[item.key]!=null));
  if(available.length<2)return <div className="rt-chart-empty"><Icon name="activity" size={23}/><span>{empty}</span><small>数据仅来自实际采样，不会填充模拟曲线</small></div>;
  const {width,height,left,right,top,bottom}=CHART_SIZE;
  const plotWidth=width-left-right,plotHeight=height-top-bottom;
  const max=Math.max(1,...available.flatMap(point=>series.map(item=>nonnegative(point[item.key]))));
  const niceMax=max<=1?1:Math.ceil(max/Math.pow(10,Math.floor(Math.log10(max)))/2)*2*Math.pow(10,Math.floor(Math.log10(max)));
  const x=i=>left+(i/Math.max(1,available.length-1))*plotWidth;
  const y=value=>top+(1-nonnegative(value)/niceMax)*plotHeight;
  const ticks=Array.from({length:5},(_,i)=>niceMax*(4-i)/4);
  const paths=series.map(item=>{
    const parts=[];let current=[];
    available.forEach((point,index)=>{
      if(point[item.key] == null){if(current.length)parts.push(current);current=[];return;}
      current.push([x(index),y(point[item.key])]);
    });
    if(current.length)parts.push(current);
    return {...item,segments:parts};
  });
  return <div className="rt-chart"><svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label={series.map(item=>item.name).join('、')+' 随时间变化的真实采样曲线'}>
   {ticks.map((v,i)=><g key={i}><line className="rt-gridline" x1={left} x2={width-right} y1={top+i*plotHeight/4} y2={top+i*plotHeight/4}/><text className="rt-axis rt-axis-y" x={left-10} y={top+i*plotHeight/4+4} textAnchor="end">{format(v)}</text></g>)}
   {paths.map(item=>item.segments.map((points,i)=>points.length>1?<polyline key={item.key+'-'+i} points={points.map(([a,b])=>a.toFixed(2)+','+b.toFixed(2)).join(' ')} fill="none" stroke={item.color} strokeWidth="2.7" strokeLinejoin="round" strokeLinecap="round"/>:null))}
   {paths.map(item=>available.map((point,index)=>point[item.key]!=null?<circle key={item.key+'-'+index} className="rt-data-point" cx={x(index)} cy={y(point[item.key])} r="3.6" fill={item.color} stroke="var(--surface2)" strokeWidth="1.8"><title>{timeText(point.timestamp)} · {item.name}：{format(point[item.key])}</title></circle>:null))}
   <text className="rt-axis" x={left} y={height-6}>{timeText(available[0].timestamp)}</text>
   <text className="rt-axis" x={width-right} y={height-6} textAnchor="end">{timeText(available[available.length-1].timestamp)}</text>
  </svg><div className="rt-series-legend">{series.map(item=><span key={item.key}><i style={{background:item.color}}/>{item.name}</span>)}</div></div>;
}

function RoutesChart({routes,go}){
  const counts=PATH_SERIES.map(item=>({...item,value:nonnegative(routes?.[item.key])}));
  const total=counts.reduce((a,b)=>a+b.value,0);
  const circumference=2*Math.PI*58;
  let position=0;
  return <div className="rt-route-layout"><svg className="rt-donut" viewBox="0 0 190 190" role="img" aria-label={total?`共 ${total} 条已上报会话，其中 P2P ${counts[0].value}、公网直连 ${counts[1].value}、Relay ${counts[2].value}、未知 ${counts[3].value}`:'没有活跃会话路径数据'}>
   <circle cx="95" cy="95" r="58" fill="none" stroke="var(--line2)" strokeWidth="19"/>
   {total>0&&counts.filter(x=>x.value>0).map(item=>{
    const len=item.value/total*circumference;
    const segment=<circle key={item.key} cx="95" cy="95" r="58" fill="none" stroke={item.color} strokeWidth="19" strokeDasharray={`${len} ${circumference-len}`} strokeDashoffset={-position} transform="rotate(-90 95 95)"><title>{item.name}：{item.value} 条会话</title></circle>;
    position+=len;
    return segment;
   })}
   <text x="95" y="91" className="rt-donut-count" textAnchor="middle">{total}</text><text x="95" y="113" className="rt-donut-label" textAnchor="middle">活跃会话</text>
  </svg><div className="rt-paths">{counts.map(item=><div className="rt-path-row" key={item.key}><span><i style={{background:item.color}}/>{item.name}</span><strong>{item.value} <small>{total?Math.round(item.value/total*100):0}%</small></strong></div>)}<button className="rt-text-action" type="button" onClick={()=>go('p2p')}>查看 P2P 路径详情 →</button></div></div>;
}

function ExitLoadChart({exits,go}){
  const sorted=[...exits].sort((a,b)=>b.streams-a.streams);
  const max=Math.max(1,...sorted.map(exit=>exit.streams));
  if(!sorted.length)return <div className="rt-chart-empty rt-compact-empty">当前没有可用出口</div>;
  return <div className="rt-exits">{sorted.slice(0,6).map(exit=><div className="rt-exit-row" key={exit.id}><div className="rt-exit-label"><strong title={exit.name}>{exit.name}</strong><span>{exit.streams} 条活跃流</span></div><div className="rt-exit-bar"><span style={{width:(exit.streams/max*100)+'%'}}/></div></div>)}{sorted.length>6&&<small className="rt-muted">还有 {sorted.length-6} 个出口未显示</small>}<button type="button" className="rt-text-action" onClick={()=>go('exits')}>查看全部出口 →</button></div>;
}

export function OverviewRealtime({ctx}){
 const {current,history,error,pending,refresh}=useTelemetry();
 const {data,go,admin}=ctx;
 const [period,setPeriod]=useState(5);
 const visible=useMemo(()=>history.filter(s=>s.timestamp>=Date.now()-period*60_000),[history,period]);
 const latest=current;
 const totalDevices=data.devices.length;
 const online=latest?.online;
 const offline=online==null?null:Math.max(0,totalDevices-online);
 const waiting=data.enrollments.length;
 const rateIsReady=latest?.uploadBps!=null;
 const sampledAt=latest?.timestamp;
 const metrics=[
  {title:'在线设备',value:online,hint:totalDevices+' 台已登记 · '+(offline??'—')+' 台离线',icon:'devices',tone:'blue',page:'devices'},
  {title:'可用出口',value:latest?.availableExits,hint:'实时出口状态',icon:'globe',tone:'teal',page:'exits'},
  {title:'活跃连接',value:latest?.connections,hint:'当前传输流数量',icon:'activity',tone:'violet',page:'sessions'},
  {title:'P2P 会话',value:latest?.p2p,hint:'当前协商及路径租约',icon:'route',tone:'amber',page:'p2p'}
 ];
 return <>
  <div className="rt-heading"><div><h1>实时运行概览</h1><p>每 10 秒从 Server 读取一次真实指标，折线历史仅保留本次打开页面后的采样。</p></div><div className="rt-refresh"><span className={'rt-live-status '+(error?'rt-error':'')}><i/>{error?'数据更新失败':sampledAt?'实时监控中':pending?'正在读取…':'等待数据'}</span>{sampledAt&&<small>更新于 {timeText(sampledAt)}</small>}<button type="button" className="btn" onClick={refresh}>刷新数据</button></div></div>
  {error&&<div className="rt-warning" role="alert">{error}。图表暂时保留上次成功采样的数值。</div>}
  <div className="grid g4 rt-metrics">{metrics.map(m=><button type="button" className={'card overview-metric metric-'+m.tone} onClick={()=>go(m.page)} key={m.title}><span className="metric-icon"><Icon name={m.icon} size={21}/></span><span className="overview-metric-copy"><span className="metric-label">{m.title}</span><strong className="metric-value">{m.value??'—'}</strong><span className="metric-hint">{m.hint}</span></span><span className="metric-arrow">↗</span></button>)}</div>
  <div className="rt-chart-topline"><div><span className="rt-kicker">LIVE TELEMETRY</span><h2>网络实时趋势</h2></div><div className="rt-period" aria-label="选择趋势图时间范围">{[1,5,10].map(n=><button type="button" aria-pressed={period===n} className={period===n?'active':''} key={n} onClick={()=>setPeriod(n)}>{n} 分钟</button>)}</div></div>
  <ChartPanel title="实时上下行速率" desc="当前已上报会话的字节计数差值 ÷ 实际采样间隔；非全服历史流量" extra={<div className="rt-live-values"><span>↑ {formatRate(latest?.uploadBps)}</span><span>↓ {formatRate(latest?.downloadBps)}</span></div>} className="rt-wide">
   <TrendChart samples={visible} series={TRAFFIC_SERIES} format={formatRate} empty={rateIsReady?'正在累积曲线采样':'需要至少两次有效采样，约 10 秒'}/>
  </ChartPanel>
  <div className="rt-two-col"><ChartPanel title="连接与设备趋势" desc="活跃连接数、在线设备及 P2P 会话随时间变化"><TrendChart samples={visible} series={SESSION_SERIES} format={n=>String(Math.round(n))} empty="等待第二次真实采样"/></ChartPanel><ChartPanel title="当前路径分布" desc="基于活跃会话实际报告；未上报的路径单独列出"><RoutesChart routes={latest?.routes} go={go}/></ChartPanel></div>
  <div className="rt-two-col"><ChartPanel title="出口实时负载" desc="各在线出口的活跃流数量"><ExitLoadChart exits={latest?.exits||[]} go={go}/></ChartPanel><ChartPanel title="当前设备与今日流量" desc="在线率为实时值；今日累计量来自 Server 审计统计"><div className="rt-summary-grid"><div className="rt-summary-stat"><span>在线设备</span><strong>{online==null?'—':online+' / '+totalDevices}</strong><div className="rt-meter"><i style={{width:totalDevices?Math.min(100,online/totalDevices*100)+'%':'0%'}}/></div></div><div className="rt-summary-stat"><span>待审批设备</span><strong>{waiting}</strong><button type="button" className="rt-text-action" onClick={()=>go('devices')}>前往审批 →</button></div><div className="rt-summary-stat"><span>今日累计上传</span><strong>{latest?fmtBytes(latest.todayUpload):'—'}</strong></div><div className="rt-summary-stat"><span>今日累计下载</span><strong>{latest?fmtBytes(latest.todayDownload):'—'}</strong></div></div></ChartPanel></div>
  <ChartPanel title="最近设备" desc="最近活跃的已登记设备" extra={<button type="button" className="btn" onClick={()=>go('devices')}>全部设备 →</button>}><div className="rt-device-list">{data.devices.slice().sort((a,b)=>(Date.parse(b.lastSeenAt)||0)-(Date.parse(a.lastSeenAt)||0)).slice(0,5).map(device=><div className="rt-device-row" key={device.id}><span className={'rt-device-dot '+(device.status==='online'?'on':'')}/><span className="rt-device-name"><strong>{device.name||device.id}</strong>{device.name&&<small>{device.id}</small>}</span><span className="rt-device-platform">{device.platform||'—'} / {device.deviceMode||'—'}</span><span className="rt-device-last">{fmtDate(device.lastSeenAt)}</span></div>)}{data.devices.length===0&&<div className="rt-compact-empty">暂无已登记设备</div>}</div></ChartPanel>
  <p className="rt-source-note">数据来源：/api/v1/dashboard、/api/v1/sessions/active、/api/v1/p2p/sessions。刷新页面后折线重新积累；后台标签页暂停采样，返回时恢复。</p>
 </>;
}
