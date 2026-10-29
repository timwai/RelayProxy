import {splitRuleList} from './ruleLines.js';

function gatewayPatch(g={}) {
  return {enabled:!!g.enabled,interface:g.interface||'',source_cidrs:splitRuleList(g.source_cidrs),exit_id:g.exit_id||'',port:Number(g.port)||0,max_connections:Number(g.max_connections)||0};
}

// An unchanged disabled gateway must not request nftables cleanup when the
// user only saves local listeners or the system transparent-proxy switch.
export function changedGatewayPatch(current,saved) {
  const patch=gatewayPatch(current);
  return JSON.stringify(patch)===JSON.stringify(gatewayPatch(saved))?undefined:patch;
}

// Save only fields owned by each page. Never round-trip unrelated policy
// fields through the UI defaults: an older GUI must not erase DNS settings
// when the user merely reorders a routing rule.
export function routingPagePatch(r) {
  return {mode:r.mode,default_action:r.default_action,rules:Array.isArray(r.rules)?r.rules:[],subscriptions:Array.isArray(r.subscriptions)?r.subscriptions:[]};
}
export function dnsPagePatch(r) {
  return {
    dns_mode:r.dns_mode,
    auto_detect_dns:!!r.auto_detect_dns,
    dns_association_enabled:r.dns_association_enabled!==false,
    proxy_dns_enabled:!!r.proxy_dns_enabled,
    fake_ip_enabled:!!r.fake_ip_enabled,
    block_doh_endpoints:!!r.block_doh_endpoints,
    forward_other_dns:!!r.forward_other_dns,
    dns_exit_id:r.dns_exit_id||'',
    dns_upstreams:Array.isArray(r.dns_upstreams)?r.dns_upstreams:[],
    dns_direct_domains:splitRuleList(r.dns_direct_domains),
    doh_blocked_ips:Array.isArray(r.doh_blocked_ips)?r.doh_blocked_ips:[],
  };
}
