export function formatAuditIngress(ingressId,ingresses=[]){
  if(!ingressId)return '未记录入口';
  const found=ingresses.find(x=>x.id===ingressId);
  if(!found)return '历史入口（已删除或未加载）';
  const port=Number(found.listenPort);
  return port>0?'端口 '+port:'已配置入口';
}
