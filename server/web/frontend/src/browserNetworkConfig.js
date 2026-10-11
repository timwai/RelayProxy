// Browser Sync network-proxy settings share the administrator's revisioned
// /server/config contract. No direct mutation of a live proxy listener.
export function normalizeBrowserNetworkDraft(draft) {
  return {
    listen:String(draft.listen||'').trim(),
    advertise:String(draft.advertise||'').trim(),
    allowed:[...new Set((draft.allowed||[]).map(x=>String(x).trim()).filter(Boolean))]
  };
}

export function validateBrowserNetworkDraft(draft,relayPort='') {
  const d=normalizeBrowserNetworkDraft(draft);
  if(Boolean(d.listen)!==Boolean(d.advertise))
    return '启用网络代理时，监听地址和公网地址必须同时填写；两者都留空表示关闭。';
  if(d.listen && d.listen!=='relay'){
    const listen=/^(:|[a-zA-Z0-9.-]+:|\[[0-9a-fA-F:]+\]:)([0-9]{1,5})$/.exec(d.listen);
    if(!listen||Number(listen[2])<1||Number(listen[2])>65535)
      return '监听地址无效，示例 :21292 或 0.0.0.0:21292';
  }
  if(!d.listen && d.allowed.length)
    return '停用网络代理时必须清除 Agent 共享白名单；可点击“停用网络出口共享”快捷完成。';
  if(d.advertise){
    if(d.allowed.length>32 || d.allowed.some(id=>!/^dev_.{4,124}$/.test(id)))
      return '最多允许 32 台 Agent，且必须选择 dev_ 开头的真实 Agent 设备 ID。';
    const publicAddr=/^([a-z0-9.-]+):([0-9]{1,5})$/i.exec(d.advertise);
    if(!publicAddr||publicAddr[1].startsWith('.')||publicAddr[1].endsWith('.')||
      Number(publicAddr[2])<1||Number(publicAddr[2])>65535)
      return '对外地址必须为证书匹配的 DNS 域名与端口，例如 relay.example.com:21292';
    if(d.listen==='relay' && (!relayPort || String(Number(publicAddr[2]))!==String(Number(relayPort))))
      return '复用中继端口时，公网代理地址端口必须与 Server TCP 中继端口一致';
    if(!d.allowed.length)
      return '先至少勾选一个管理员允许共享的 Agent 出口；默认不得对所有设备开放。';
  }
  return '';
}

export function applyBrowserNetworkDraft(config,draft) {
  const d=normalizeBrowserNetworkDraft(draft);
  return {...config,browserSync:{
    ...config.browserSync,
    networkProxyListen:d.listen,networkProxyAdvertise:d.advertise,
    allowedExitIds:d.allowed
  }};
}
