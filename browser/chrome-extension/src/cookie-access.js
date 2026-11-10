// Chrome checks read access using the Cookie's own domain and Secure attribute,
// rather than the webpage's URL. An HTTPS page can use parent-domain cookies
// and non-Secure cookies. Grant exact hosts only, never wildcard subdomains.
export function cookieAccessHosts(origin){
  const url=new URL(origin);
  if(!['http:','https:'].includes(url.protocol))throw new Error('Cookie 仅支持 HTTP/HTTPS 站点');
  const host=url.hostname,hosts=[host];
  if(!host.includes(':')&&!/^\d+(?:\.\d+){3}$/.test(host)){
    const labels=host.split('.');
    for(let i=1;i<labels.length-1;i++)hosts.push(labels.slice(i).join('.'));
  }
  return hosts;
}

export function cookieAccessOrigins(origin){
  const schemes=new URL(origin).protocol==='https:'?['https:','http:']:['http:'];
  return cookieAccessHosts(origin).flatMap(host=>schemes.map(scheme=>scheme+'//'+host+'/*'));
}

export async function requireCookieAccess(origin){
  if(!await chrome.permissions.contains({origins:cookieAccessOrigins(origin)}))
    throw new Error('Cookie 访问权限不完整，请在“站点授权”点击“补全 Cookie 权限”后重试');
}
