import {splitRuleList} from './ruleLines.js';

const DNS_ACTIONS = new Set(['LOCAL','REMOTE']);
const DNS_RESOLVERS = new Set(['encrypted','exit_system']);

export function normalizeDNSRuleDraft(rule={}) {
  const action=rule.action==='LOCAL'?'LOCAL':'REMOTE';
  return {
    name:String(rule.name??''),
    enabled:rule.enabled!==false,
    action,
    resolver:action==='REMOTE'&&rule.resolver==='exit_system'?'exit_system':'encrypted',
    exit_id:action==='REMOTE'?String(rule.exit_id??''):'',
    domains:splitRuleList(rule.domains).join('\n'),
  };
}

export function buildDNSRule(rule={}) {
  const action=rule.action==='LOCAL'?'LOCAL':'REMOTE';
  return {
    name:String(rule.name??'').trim(),
    enabled:rule.enabled!==false,
    action,
    resolver:action==='REMOTE'&&rule.resolver==='exit_system'?'exit_system':'encrypted',
    exit_id:action==='REMOTE'?String(rule.exit_id??'').trim():'',
    domains:splitRuleList(rule.domains).map(x=>x.toLowerCase().replace(/\.$/,'')),
  };
}

function validDomain(value,allowWildcard=false) {
  let domain=String(value??'').trim().toLowerCase();
  if(allowWildcard&&domain.startsWith('*.'))domain=domain.slice(2);
  if(!domain||domain.includes('*')||domain.length>253||domain!==domain.trim())return false;
  if(/^\d{1,3}(?:\.\d{1,3}){3}$/.test(domain))return false;
  return domain.split('.').every(label=>label.length>0&&label.length<=63&&
    label[0]!=='-'&&label[label.length-1]!=='-'&&/^[a-z0-9-]+$/.test(label));
}

export function validateDNSRule(rule={},fallbackExitId='') {
  if(!DNS_ACTIONS.has(rule.action))return '解析位置无效';
  const domains=splitRuleList(rule.domains);
  if(domains.length<1||domains.length>64)return '至少填写 1 个域名，最多 64 个';
  const seen=new Set();
  for(const domain of domains){
    if(!validDomain(domain,true))return '域名格式无效：'+domain+'（仅支持 example.com 或 *.example.com）';
    const key=domain.toLowerCase().replace(/\.$/,'');
    if(seen.has(key))return '域名重复：'+domain;
    seen.add(key);
  }
  if(rule.action==='LOCAL')return '';
  const resolver=rule.resolver||'encrypted';
  if(!DNS_RESOLVERS.has(resolver))return '解析器类型无效';
  if(resolver==='exit_system'){
    const exitId=String(rule.exit_id||fallbackExitId||'').trim();
    if(!exitId)return '出口系统 DNS 必须指定一个设备出口';
    if(exitId.startsWith('local:'))return '出口系统 DNS 不支持本机自定义代理，请选择设备出口';
  }
  const exitId=String(rule.exit_id||'');
  if(exitId&&(exitId!==exitId.trim()||/[\s/\\]/.test(exitId)||exitId.length>128))return 'DNS 出口 ID 格式无效';
  return '';
}

function validIPAddress(value) {
  const ip=String(value??'').trim();
  if(!ip||ip!==value)return false;
  if(/^\d{1,3}(?:\.\d{1,3}){3}$/.test(ip))return ip.split('.').every(part=>Number(part)>=0&&Number(part)<=255);
  if(!ip.includes(':'))return false;
  try { new URL('http://['+ip+']/'); return true; } catch { return false; }
}

function validDoHURL(value) {
  try {
    const u=new URL(value);
    return u.protocol==='https:'&&!!u.hostname&&u.port===''&&
      !u.username&&!u.password&&!u.search&&!u.hash&&u.pathname!=='/'&&
      !/^\d+(?:\.\d+){3}$/.test(u.hostname)&&u.hostname.includes('.')&&
      /^[a-z0-9.-]+$/i.test(u.hostname);
  } catch { return false; }
}

export function validateDNSSettings(r={},defaultExitId='') {
  const rules=Array.isArray(r.dns_rules)?r.dns_rules:[];
  if(rules.length>128)return 'DNS 规则最多 128 条';
  for(let i=0;i<rules.length;i++){
    const error=validateDNSRule(rules[i],r.dns_exit_id||defaultExitId);
    if(error)return '第 '+(i+1)+' 条 DNS 规则：'+error;
  }
  const upstreams=Array.isArray(r.dns_upstreams)?r.dns_upstreams:[];
  if(upstreams.length>8)return '自定义 DoH 上游最多 8 个';
  for(let i=0;i<upstreams.length;i++){
    const item=upstreams[i]||{};
    if(!validDoHURL(String(item.url||'')))return '第 '+(i+1)+' 个 DoH 上游地址无效：需要 HTTPS 域名和路径，例如 https://dns.example/dns-query';
    if(!validIPAddress(item.bootstrap_ip))return '第 '+(i+1)+' 个 DoH 上游缺少有效的 Bootstrap IP';
  }
  const direct=splitRuleList(r.dns_direct_domains);
  if(direct.length>64)return '本地 DNS 例外最多 64 个域名';
  for(const domain of direct)if(!validDomain(domain,false))return '本地 DNS 例外域名无效：'+domain;
  const blocked=Array.isArray(r.doh_blocked_ips)?r.doh_blocked_ips:[];
  if(blocked.length>256)return '额外 DoH 阻断地址最多 256 条';
  for(const value of blocked){
    const raw=String(value??'').trim();
    const parts=raw.split('/');
    if(parts.length>2||!validIPAddress(parts[0])||(parts.length===2&&(!/^\d+$/.test(parts[1])||Number(parts[1])<1||Number(parts[1])>(parts[0].includes(':')?128:32))))return 'DoH 阻断地址格式无效：'+value;
  }
  if(r.proxy_dns_enabled&&(r.dns_mode==='local'||r.auto_detect_dns))return '代理 DNS 需要选择“代理解析”，不能同时启用本地 DNS 自动检测';
  if(r.proxy_dns_enabled&&r.fake_ip_enabled)return '真实 IP 代理 DNS 与 FakeIP 不能同时启用';
  if(r.fake_ip_enabled&&r.dns_mode==='local')return 'FakeIP 模式需要使用代理 DNS';
  if((r.block_doh_endpoints||r.forward_other_dns)&&!r.fake_ip_enabled)return 'DoH 阻断和 TXT/SRV 转发仅适用于 FakeIP 模式';
  return '';
}
