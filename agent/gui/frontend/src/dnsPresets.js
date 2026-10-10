// Project the existing DNS flags into a small, stable UI model.
// Keep legacy fields on disk so older Agents can continue reading configs.
export function currentDNSPreset(r={}) {
  if (r.fake_ip_enabled) return 'fake_ip';
  if (r.proxy_dns_enabled) return 'auto';
  return 'real_ip';
}
export function applyDNSPreset(r={}, preset) {
  const base={...r,dns_mode:'proxy',auto_detect_dns:false,dns_association_enabled:true};
  switch(preset) {
    case 'auto':
      // Capture system DNS and return real IPs via the selected proxy exit.
      return {...base,proxy_dns_enabled:true,fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false};
    case 'real_ip':
      // Observe ordinary DNS responses without synthesizing addresses.
      return {...base,proxy_dns_enabled:false,fake_ip_enabled:false,block_doh_endpoints:false,forward_other_dns:false};
    case 'fake_ip':
      return {...base,proxy_dns_enabled:false,fake_ip_enabled:true};
    default: throw new Error('Unknown DNS preset: '+preset);
  }
}
