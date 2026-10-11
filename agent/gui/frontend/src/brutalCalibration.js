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
export function proposeBrutalFromSpeedTests(samples){
 if(!Array.isArray(samples)||samples.length<BRUTAL_CALIBRATION_ROUNDS)throw new Error('至少需要 5 轮有效的双向 BBR 测速');
 const paths=new Set();
 const measurements={upload:[],download:[]};
 for(const sample of samples){
  for(const direction of ['upload','download']){
   const entry=sample?.[direction];
   if(!entry||!entry.quic)throw new Error('无法确认 Relay QUIC 路径：保留 BBR');
   const path=String(entry.path||'');
   if(!/relay/i.test(path)||/p2p|direct/i.test(path))throw new Error('测速路径不是固定 Relay QUIC，无法估算 Brutal');
   paths.add(path.toLowerCase());
   if(entry.quic.congestion_controller!=='bbr-aggressive'||entry.quic.brutal_fallback_reason)throw new Error('需要纯 BBR Aggressive 测速，当前存在 Brutal 或保护回退');
   const mbps=Number(entry.megabitsPerSecond);
   const milliseconds=Number(entry.durationMs);
   if(!Number.isFinite(mbps)||mbps<=0||!Number.isFinite(milliseconds)||milliseconds<1000){
    throw new Error('吞吐或时长无效，保留 BBR');
   }
   measurements[direction].push(mbps);
  }
 }
 if(paths.size!==1)throw new Error('测速途中 Relay 路径改变，不能混合不同路径样本');
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
  rounds:samples.length,relayPath:[...paths][0],
  autoApplyAllowed:false,brutalDefaultEligible:false,
  reason:'仅基于当前出口的 BBR 应用吞吐，尚未验证共享链路容量、实际 RTT 和 Hysteria2 对照'
 };
}
