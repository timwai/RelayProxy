// Shared by the Agent and Server consoles. Old path values remain compatible;
// network scope is displayed only when supplied for this particular transport.
export function pathLabel(info, fallback = '') {
 const path = info?.path || fallback;
 const legacy = {public_direct_quic:'公网端点直连 · QUIC',p2p_quic:'P2P 直连 · QUIC',direct_quic:'直连 · QUIC',direct_tls:'直连 · TLS',relay_quic:'中继 · QUIC',relay_tls:'中继 · TLS',relay:'中继',tcp_p2p:'P2P 直连 · TCP',udp_p2p:'P2P 直连 · UDP'};
 if (!info || (fallback && info.path !== fallback)) return legacy[fallback] || fallback || '未知路径';
 if (path.startsWith('relay')) return legacy[path] || '中继';
 const network = {lan:'局域网直连',public:path.includes('p2p')?'P2P 公网直连':'公网直连',virtual:'虚拟网络直连',loopback:'本机直连',unknown:'直连（网络范围未知）'}[info.network] || '直连（网络范围未知）';
 return network + (info.transport ? ' · ' + info.transport.toUpperCase() : '');
}

export function activePathLabel(paths) {
 if (!Array.isArray(paths)) return '未提供实际连接路径';
 const labels = [...new Set(paths.map(p => pathLabel(p)))];
 return labels.length ? labels.join(' + ') : '暂无活动代理连接';
}
