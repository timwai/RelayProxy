// Offline and on-demand Agent calibration only. A 5-round BBR speed test
// observes *application goodput*, NOT independently measured physical WAN
// capacity or real multi-tenant fairness. Never auto-enable Brutal from it.
export const BRUTAL_CALIBRATION_ROUNDS=5;
export const BRUTAL_CALIBRATION_FRACTION=0.60;

function percentile(values,fraction){
 const ordered=[...values].sort((a,b)=>a-b);
 const position=(ordered.length-1)*fraction;
 const lower=Math.floor(position),upper=Math.ceil(position);
 return ordered[lower]+(ordered[upper]-ordered[lower])*(position-lower);
}
// Validate every individual reading before permitting another throughput
// probe, so a non-Relay route or active Brutal cannot waste five full rounds.
export function validateBrutalCalibrationSample(sample){
 if(!sample?.exitId)throw new Error('缺少出口标识，无法确认测速来源');
 const rates={};
 for(const direction of ['upload','download']){
  const entry=sample?.[direction];
  if(!entry||!entry.quic)throw new Error('无法确认 Relay QUIC 路径：保留 BBR');
  const path=String(entry.path||'').toLowerCase();
  if(path!=='relay_quic')throw new Error('测速路径不是固定 Relay QUIC，无法估算 Brutal');
  if(entry.quic.congestion_controller!=='bbr-aggressive'||entry.quic.brutal_fallback_reason){
   throw new Error('需要纯客户端 BBR Aggressive 测速，当前存在 Brutal 或保护回退');
  }
  const mbps=Number(entry.megabitsPerSecond);
  const milliseconds=Number(entry.durationMs);
  if(!Number.isFinite(mbps)||mbps<=0||!Number.isFinite(milliseconds)||milliseconds<1000){
   throw new Error('吞吐或时长无效，保留 BBR');
  }
  rates[direction]=mbps;
 }
 return {exitId:sample.exitId,relayPath:'relay_quic',...rates};
}
export function proposeBrutalFromSpeedTests(samples){
 if(!Array.isArray(samples)||samples.length<BRUTAL_CALIBRATION_ROUNDS)throw new Error('至少需要 5 轮有效的双向 BBR 测速');
 const ids=new Set();
 const paths=new Set();
 const measurements={upload:[],download:[]};
 for(const sample of samples){
  const checked=validateBrutalCalibrationSample(sample);
  ids.add(checked.exitId);
  paths.add(checked.relayPath);
  measurements.upload.push(checked.upload);
  measurements.download.push(checked.download);
 }
 if(ids.size!==1||paths.size!==1)throw new Error('测速期间出口或 Relay 路径改变，不能混合不同样本');
 const suggested={};
 for(const [direction,values] of Object.entries(measurements)){
  const lowerTail=percentile(values,0.10);
  suggested[direction]={
   bbrP10Mbps:Number(lowerTail.toFixed(3)),
   candidateMbps:Math.max(0,Math.floor(lowerTail*BRUTAL_CALIBRATION_FRACTION))
  };
 }
 return {
  upload:suggested.upload,download:suggested.download,
  rounds:samples.length,exitId:[...ids][0],relayPath:[...paths][0],
  autoApplyAllowed:false,brutalDefaultEligible:false,
  reason:'仅确认本机发送侧 BBR。服务端发送侧的控制器、链路容量、实际 RTT、公平性及 Hysteria2 对照尚未确认。'
 };
}
