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
  const raw=String(value??'');
  if(!raw||raw!==raw.trim()||raw.length>253)return false;
  let domain=raw.toLowerCase();
  if(allowWildcard&&domain.startsWith('*.'))domain=domain.slice(2);
  domain=domain.replace(/\.$/,'');
  if(!domain||domain.includes('*'))return false;
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
    // An empty rule exit intentionally follows the current default device exit.
    // DNSExitID is only for encrypted DNS and must not be used as this fallback.
    const exitId=String(rule.exit_id||fallbackExitId||'').trim();
    if(exitId.startsWith('local:'))return '出口系统 DNS 不支持本机自定义代理，请选择设备出口或跟随设备默认出口';
  }
  const exitId=String(rule.exit_id||'');
  if(exitId&&(exitId!==exitId.trim()||/[\s/\\]/.test(exitId)||exitId.length>128))return 'DNS 出口 ID 格式无效';
  return '';
}

function parseIPAddress(value) {
  const ip=String(value??'').trim();
  if(!ip||ip!==value||ip.includes('%'))return null;
  if(/^\d{1,3}(?:\.\d{1,3}){3}$/.test(ip)){
    const parts=ip.split('.');
    if(!parts.every(part=>/^(0|[1-9][0-9]{0,2})$/.test(part)))return null;
    const octets=parts.map(Number);
    if(!octets.every(part=>part>=0&&part<=255))return null;
    return {family:4,octets};
  }
  if(!ip.includes(':'))return null;
  try {
    const hostname=new URL('http://['+ip+']/').hostname;
    return {family:6,text:hostname.slice(1,-1).toLowerCase()};
  } catch { return null; }
}

function isUnicastIP(ip) {
  if(!ip)return false;
  if(ip.family===4){
    const [a,b]=ip.octets;
    return a!==0&&a!==127&&a<224&&!(a===169&&b===254);
  }
  const text=ip.text;
  if(text==='::'||text==='::1'||text.startsWith('ff')||text.startsWith('::ffff:'))return false;
  const first=parseInt(text.split(':')[0]||'0',16);
  return !(first>=0xfe80&&first<=0xfebf);
}

function isPublicIP(ip) {
  if(!isUnicastIP(ip))return false;
  if(ip.family===4){
    const [a,b]=ip.octets;
    return a!==10&&!(a===172&&b>=16&&b<=31)&&!(a===192&&b===168);
  }
  const first=parseInt(ip.text.split(':')[0]||'0',16);
  return !(first>=0xfc00&&first<=0xfdff);
}

function validDoHURL(value) {
  try {
    if(typeof value!=='string'||!value||value!==value.trim()||value.length>512)return false;
    const u=new URL(value);
    const authorityAndPath=value.slice(8).split(/[?#]/,1)[0];
    const pathSeparator=authorityAndPath.indexOf('/');
    const authority=authorityAndPath.slice(0,pathSeparator);
    const host=u.hostname;
    if(u.protocol!=='https:'||!host||!(pathSeparator>0)||u.port!==''||
      u.username||u.password||u.search||u.hash||
      !/^[a-z0-9.-]+(?::443)?$/i.test(authority)||
      /^\d+(?:\.\d+){3}$/.test(host)||
      !host.includes('.')||host.length>253||!/^[a-z0-9.-]+$/i.test(host))return false;
    return host.split('.').every(label=>label.length>0&&label.length<=63&&
      label[0]!=='-'&&label[label.length-1]!=='-');
  } catch { return false; }
}

export function validateDNSSettings(r={},defaultExitId='') {
  const dnsExitID=String(r.dns_exit_id||'');
  if(dnsExitID&&(dnsExitID!==dnsExitID.trim()||/[\s/\\]/.test(dnsExitID)||dnsExitID.length>128))return '专用 DNS 出口 ID 格式无效';
  const rules=Array.isArray(r.dns_rules)?r.dns_rules:[];
  if(rules.length>128)return 'DNS 规则最多 128 条';
  for(let i=0;i<rules.length;i++){
    const error=validateDNSRule(rules[i],defaultExitId);
    if(error)return '第 '+(i+1)+' 条 DNS 规则：'+error;
  }
  const upstreams=Array.isArray(r.dns_upstreams)?r.dns_upstreams:[];
  if(upstreams.length>8)return '自定义 DoH 上游最多 8 个';
  for(let i=0;i<upstreams.length;i++){
    const item=upstreams[i]||{};
    if(!validDoHURL(String(item.url||'')))return '第 '+(i+1)+' 个 DoH 上游地址无效：需要 HTTPS 域名和路径，例如 https://dns.example/dns-query';
    if(!isUnicastIP(parseIPAddress(item.bootstrap_ip)))return '第 '+(i+1)+' 个 DoH 上游缺少有效的 Bootstrap IP';
  }
  const direct=splitRuleList(r.dns_direct_domains);
  if(direct.length>64)return '本地 DNS 例外最多 64 个域名';
  for(const domain of direct)if(!validDomain(domain,false))return '本地 DNS 例外域名无效：'+domain;
  const blocked=splitRuleList(r.doh_blocked_ips);
  if(blocked.length>256)return '额外 DoH 阻断地址最多 256 条';
  for(const value of blocked){
    const raw=String(value??'').trim();
    const parts=raw.split('/');
    const ip=parseIPAddress(parts[0]);
    const minimum=ip?.family===4?24:48,maximum=ip?.family===4?32:128;
    if(raw!==value||parts.length>2||!isPublicIP(ip)||(parts.length===2&&(!/^\d+$/.test(parts[1])||Number(parts[1])<minimum||Number(parts[1])>maximum)))return 'DoH 阻断地址格式无效：'+value;
  }
  if(r.dns_mode&&r.dns_mode!=='proxy'&&r.dns_mode!=='local')return 'DNS 模式无效：请选择代理解析或本地解析';
  if(r.proxy_dns_enabled&&(r.dns_mode==='local'||r.auto_detect_dns))return '代理 DNS 需要选择“代理解析”，不能同时启用本地 DNS 自动检测';
  if(r.proxy_dns_enabled&&r.fake_ip_enabled)return '真实 IP 代理 DNS 与 FakeIP 不能同时启用';
  if(r.auto_detect_dns&&r.fake_ip_enabled)return '本地 DNS 自动检测与 FakeIP 不能同时启用';
  if(r.fake_ip_enabled&&r.dns_mode==='local')return 'FakeIP 模式需要使用代理 DNS';
  if((r.block_doh_endpoints||r.forward_other_dns)&&!r.fake_ip_enabled)return 'DoH 阻断和 TXT/SRV 转发仅适用于 FakeIP 模式';
  return '';
}
