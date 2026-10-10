// One DoH URL and its literal bootstrap IP per line. Never silently drop
// incomplete lines: invalid entries must reach backend validation on Save.
export function formatDNSUpstreamLines(entries=[]) {
  return (Array.isArray(entries)?entries:[])
    .map(entry=>(entry.url||'')+' | '+(entry.bootstrap_ip||''))
    .join('\n');
}
export function parseDNSUpstreamLines(text='') {
  return String(text).split(/\r?\n/)
    .filter(line=>line.trim()!=='')
    .map(line=>{
      const at=line.indexOf('|');
      return {
        url:(at<0?line:line.slice(0,at)).trim(),
        bootstrap_ip:(at<0?'':line.slice(at+1)).trim(),
      };
    });
}
