export function formatAuditIngress(ingressId,ingresses=[]){
  if(!ingressId)return '未记录入口';
  const found=ingresses.find(x=>x.id===ingressId);
  if(!found)return '历史入口（已删除或未加载）';
  const port=Number(found.listenPort);
  return port>0?'端口 '+port:'已配置入口';
}

export const auditSortOptions=[
  ['lastSeen:desc','按最近连接（最新优先）'],
  ['connections:desc','按连接次数（从高到低）'],
  ['connections:asc','按连接次数（从低到高）'],
  ['rejected:desc','按拒绝次数（从高到低）'],
  ['forwarded:desc','按转发次数（从高到低）'],
  ['bytes:desc','按累计流量（从高到低）'],
  ['firstSeen:asc','按首次访问（最早优先）']
];

export function parseAuditSort(value='lastSeen:desc'){
  const [rawSort,rawOrder]=String(value||'lastSeen:desc').split(':');
  return {
    sort:rawSort||'lastSeen',
    order:rawOrder==='asc'?'asc':'desc'
  };
}

export function buildAuditGroupsQuery({page=1,pageSize=20,search='',sort='lastSeen:desc'}={}){
  const {sort:sortKey,order}=parseAuditSort(sort);
  const params=new URLSearchParams({
    page:String(page),
    pageSize:String(pageSize),
    search:String(search||''),
    sort:sortKey,
    order
  });
  return params.toString();
}

export function buildSecurityBansQuery({page=1,pageSize=20,search='',kind='',scope='',expiry=''}={}){
  const params=new URLSearchParams({
    page:String(page),
    pageSize:String(pageSize)
  });
  if(search)params.set('search',String(search));
  if(kind)params.set('kind',String(kind));
  if(scope)params.set('scope',String(scope));
  if(expiry)params.set('expiry',String(expiry));
  return params.toString();
}

export function formatBanKind(entry={}){
  if(entry.kind==='manual')return '手动封禁';
  if(entry.kind==='allow')return '白名单';
  if(String(entry.reason||'').startsWith('GEO_'))return 'GeoIP 封禁';
  return '自动封禁';
}

export function formatBanScope(ingressId,ingresses=[]){
  if(!ingressId)return '全局 RDP';
  return formatAuditIngress(ingressId,ingresses);
}
