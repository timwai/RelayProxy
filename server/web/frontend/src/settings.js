// Configuration keys and types mirror server/web/index.html / api/server/config.
export const groups = [
  {id:'admin',title:'管理访问',subtitle:'管理后台地址、访问协议',section:'基础服务',fields:[
    ['listen','监听地址','text',':21080'],['tlsEnabled','访问协议','bool-select',null,['HTTP','HTTPS']]]},
  {id:'tunnel',title:'隧道连接',subtitle:'TCP、QUIC 与设备连接容量',section:'基础服务',fields:[
    ['tcpListen','TCP 监听地址','text',':443'],['quicListen','QUIC / UDP 监听地址','text',':443'],['tlsEnabled','启用隧道 TLS','checkbox'],
    ['heartbeatSec','心跳间隔（秒）','number'],['maxConnections','全局设备连接上限','number'],['maxStreamsPerDevice','每设备并发流上限','number']]},
  {id:'direct',title:'公网直连',subtitle:'Public Direct QUIC 与 Agent 监听端口',section:'网络能力',fields:[
    ['enabled','启用 Public Direct','checkbox'],['portStart','Agent UDP 端口起始','number'],['portEnd','Agent UDP 端口结束','number']]},
  {id:'p2p',title:'P2P 直连',subtitle:'UDP 打洞、Rendezvous、UPnP',section:'网络能力',fields:[
    ['upnpEnabled','启用 UPnP 自动映射','checkbox'],['rendezvousListen','Rendezvous 监听地址','text',':3479'],
    ['rendezvousAdvertise','Rendezvous 公网地址','text','relay.example.com:3479'],['portStart','Agent UDP 端口起始','number'],['portEnd','Agent UDP 端口结束','number']]},
  {id:'exit',key:'serverExit',title:'Server 网络出口',subtitle:'本机出口、上游代理、身份授权',section:'网络能力',fields:[
    ['enabled','启用 Server 网络出口','checkbox'],['allowInternet','允许访问互联网','checkbox'],['allowPrivateNetwork','允许私有网络','checkbox'],['allowLoopback','允许回环地址','checkbox'],
    ['upstreamMode','上游网络','select',null,[['direct','Server 本机直连'],['socks5','SOCKS5'],['http','HTTP CONNECT'],['https','HTTPS CONNECT']]],
    ['upstreamAddress','上游代理地址','text','proxy.example.com:1080'],['upstreamUsername','上游用户名','text'],['upstreamPassword','上游密码','password'],
    ['accessMode','目标访问模式','select',null,[['','不附加筛选'],['allow','允许列表'],['deny','拒绝列表']]],['domains','匹配域名（每行一条）','lines'],['cidrs','IP / CIDR（每行一条）','lines']]},
  {id:'rdp',key:'rdpIngress',title:'RDP 公网入口',subtitle:'公网监听端口、来源和速率限制',section:'安全与访问',fields:[
    ['enabled','启用公网入口服务','checkbox'],['listen','监听地址','text','0.0.0.0:0'],['portStart','自动端口起始','number'],['portEnd','自动端口结束','number'],
    ['sourceCidrs','全局来源 CIDR（每行一条）','lines'],['rateLimitPerMinute','每来源 IP 每分钟上限','number']]},
  {id:'certificate',title:'证书管理',subtitle:'HTTPS / QUIC 证书路径',section:'安全与访问',fields:[
    ['certFile','证书链 PEM 路径','text','/data/config/fullchain.pem'],['keyFile','私钥 PEM 路径','text','/data/config/privkey.pem']]},
  {id:'acl',key:'relayACL',title:'目标访问权限',subtitle:'服务端统一 ACL 与目标匹配规则',section:'安全与访问',fields:[
    ['allowInternet','允许互联网','checkbox'],['allowPrivateNetwork','允许私有网络','checkbox'],['allowLoopback','允许回环地址','checkbox'],
    ['accessMode','匹配模式','select',null,[['','不附加筛选'],['allow','允许列表'],['deny','拒绝列表']]],['domains','域名（每行一条）','lines'],['cidrs','IP / CIDR（每行一条）','lines']]}
];
export const getGroup = id => groups.find(g=>g.id===id)||groups[0];
export const normalizeForSave = (draft, original) => {
  // Do not strip unknown config fields sent by newer servers.
  const out=structuredClone(original);
  for(const grp of groups){const key=grp.key||grp.id;out[key]={...out[key],...draft[key]};}
  return out;
};
export function configFieldCount() {return groups.reduce((n,g)=>n+g.fields.length,0);}