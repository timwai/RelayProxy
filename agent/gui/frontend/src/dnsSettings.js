// Pure UI transitions. Security prerequisites change as one coherent
// routing configuration rather than leaving disabled-looking controls.
export function enableFakeIP(r, enabled) {
  if (!enabled) return {...r,fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false};
  return {...r,dns_mode:'proxy',auto_detect_dns:false,proxy_dns_enabled:false,fake_ip_enabled:true};
}
export function setDoHBlocking(r, enabled) {
  return {...(enabled?enableFakeIP(r,true):r),block_doh_endpoints:enabled};
}
export function setOtherDNSForwarding(r, enabled) {
  return {...(enabled?enableFakeIP(r,true):r),forward_other_dns:enabled};
}
export function setAutoDNS(r, enabled) {
  return enabled
    ? {...r,dns_mode:'local',auto_detect_dns:true,fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false}
    : {...r,auto_detect_dns:false};
}
export function setProxyDNS(r, enabled) {
  return enabled
    ? {...r,auto_detect_dns:false,dns_mode:'proxy'}
    : {...r,auto_detect_dns:false,dns_mode:'local',fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false};
}

// This strategy returns genuine remote-resolved IPs; FakeIP's synthetic
// mappings and its dependent interception guards cannot run simultaneously.
export function setRealProxyDNS(r, enabled) {
  if (!enabled) return {...r,proxy_dns_enabled:false};
  return {...r,proxy_dns_enabled:true,dns_mode:'proxy',auto_detect_dns:false,
    fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false};
}
