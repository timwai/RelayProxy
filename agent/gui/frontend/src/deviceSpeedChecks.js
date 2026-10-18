import {deviceSpeedLinkEvidence} from './deviceSpeedLink.js';

// One row = one piece of evidence. A transport not used in this test is
// unknown, NOT a failed connection; a public endpoint is only a candidate.
// The Agent speed RPC returns both directions together, so results are
// revealed one row at a time only AFTER the response, never simulated as
// separate in-flight network probes.
export function deviceSpeedChecks({exitID,exit,connected=true,phase='idle',result=null,revealCount=0}={}){
 const evidence=deviceSpeedLinkEvidence(exitID,exit,result);
 const verified=evidence.verifiedTest;
 const failed=phase==='failed'||!!result?.error||(!!result&&result.exitId!==exitID);
 const checking=phase==='running';
 const reviewing=phase==='reviewing';
 const complete=phase==='done';
 const waiting=checking?'测速中，等待真实数据':reviewing?'正在逐项核对测速结果':failed?'测速未完成，无法确认':'开始测速后核验';
 const rows=[];
 const add=(id,label,state,detail)=>rows.push({id,label,state,detail});
 const auth=!!exitID&&!!exit&&exit.online!==false;
 add('authorization','设备授权与在线状态',auth?'checked':'failed',auth?(exit.name||exit.deviceName||exitID)+' · 当前授权出口':exitID?'设备离线或已撤销授权':'缺少设备标识');
 const count=evidence.verifiedEndpointCount;
 add('candidate','公网直连候选端点',count>0?'checked':evidence.publicAdvertised?'candidate':'unknown',count>0?count+' 个已验证候选（不代表实测可达）':evidence.publicAdvertised?'设备已公布候选，尚未验证端点':'未发现已验证候选');
 add('test','双向链路测速',checking?'progress':reviewing||complete?verified?'checked':'failed':failed?'failed':'waiting',verified?'已收到当前设备的上行与下行结果':failed?(result?.error||'返回设备 ID 不匹配'):checking?'正在执行真实的双向测速':'点击开始测速');
 const checks=[
  ['public','Public Direct',evidence.usedPublic,evidence.publicAdvertised||count>0,evidence.usedPublic?'本次测速使用公网直连':count>0?'存在已验证候选，未在本次测速中使用':evidence.publicAdvertised?'设备报告候选，未在本次测速中使用':'本次测速未确认公网直连'],
  ['p2p','P2P QUIC',evidence.usedP2P,false,evidence.usedP2P?'本次测速使用 P2P QUIC':'本次测速未确认 P2P QUIC'],
  ['relay','Relay',evidence.usedRelay,false,evidence.usedRelay?'本次测速使用 Relay':'本次测速未确认 Relay'],
  ['upload','上行实际路径',!!evidence.uploadPath,false,evidence.uploadPath||'测速未报告上行路径'],
  ['download','下行实际路径',!!evidence.downloadPath,false,evidence.downloadPath||'测速未报告下行路径']
 ];
 for(let i=0;i<checks.length;i++){
  const [id,label,used,candidate,detail]=checks[i];
  if(verified&&(complete||(reviewing&&i<revealCount))){
   add(id,label,used?'checked':candidate?'candidate':'unknown',detail);
  }else add(id,label,'waiting',waiting);
 }
 return rows;
}
export const DEVICE_SPEED_REVIEW_STEPS=5;
