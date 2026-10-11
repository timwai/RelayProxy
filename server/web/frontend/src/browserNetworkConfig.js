// Browser Sync network-proxy settings share the administrator's revisioned
// /server/config contract. No direct mutation of a live proxy listener.
export function normalizeBrowserNetworkDraft(draft) {
  return {
    listen:String(draft.listen||'').trim(),
    advertise:String(draft.advertise||'').trim(),
    allowed:[...new Set((draft.allowed||[]).map(x=>String(x).trim()).filter(Boolean))]
  };
}

export function validateBrowserNetworkDraft(draft) {
  const d=normalizeBrowserNetworkDraft(draft);
  if(Boolean(d.listen)!==Boolean(d.advertise))
    return '启用网络代理时，监听地址和公网地址必须同时填写；两者都留空表示关闭。';
  if(d.listen){
    const listen=/^(:|[a-zA-Z0-9.-]+:|\[[0-9a-fA-F:]+\]:)([0-9]{1,5})$/.exec(d.listen);
    if(!listen||Number(listen[2])<1||Number(listen[2])>65535)
      return '监听地址无效，示例 :21292 或 0.0.0.0:21292';
  }
  if(d.advertise){
    const publicAddr=/^([a-z0-9.-]+):([0-9]{1,5})$/i.exec(d.advertise);
    if(!publicAddr||publicAddr[1].startsWith('.')||publicAddr[1].endsWith('.')||
      Number(publicAddr[2])<1||Number(publicAddr[2])>65535)
      return '对外地址必须为证书匹配的 DNS 域名与端口，例如 relay.example.com:21292';
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
