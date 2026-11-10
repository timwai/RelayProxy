import {pathLabel,activePathLabel} from '../../../../internal/webui/pathLabel.mjs';
export function proxyPathStatus(status={}) {
 const connected=!!status.connected;
 const rawPath=status.directPath||status.p2pPath||status.transport||'';
 return {
  path:connected?(Array.isArray(status.activeProxyPaths)?activePathLabel(status.activeProxyPaths):(rawPath||'等待路径')):'未连接',
  directReady:connected&&/public[_-]direct/i.test(rawPath),
  p2pReady:connected&&/p2p/i.test(rawPath),
  relay:connected&&/relay|^(quic|tcp)$/i.test(rawPath),
 };
}

export function rdpPathLabel(path, details) {
 if (Array.isArray(details) && details.length) return [...new Set(details.map(p=>pathLabel(p)))].join(' + ');
 if (details?.path) return pathLabel(details);
 return String(path||'waiting').split(' + ').map(value=>({
  waiting:'等待客户端流量',disabled:'未启用',relay:'Relay',tcp_p2p:'P2P (TCP)',udp_p2p:'P2P (UDP)',
 }[value]||value)).join(' + ');
}
