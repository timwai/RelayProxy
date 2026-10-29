// Windows native Wails builds expose a Windows-only Network Service bridge.
// Older native binaries may omit the platform field from GetConfig, so keep
// their transparent-proxy controls visible based on that trusted bridge.
export function showWindowsCapture(platform, hasNetworkServiceBridge) {
  const value = String(platform ?? '').trim().toLowerCase();
  return value === 'windows' || (value === '' && !!hasNetworkServiceBridge);
}

export function showTransparentCapture(platform, hasNetworkServiceBridge) {
  return String(platform ?? '').trim().toLowerCase() === 'linux' || showWindowsCapture(platform, hasNetworkServiceBridge);
}

export function showWindowsService(platform, hasNetworkServiceBridge) {
  return showWindowsCapture(platform, hasNetworkServiceBridge) && !!hasNetworkServiceBridge;
}

export function transparentCaptureReady(caps={}) {
  return !!(caps.tcp&&caps.udp&&caps.process_identity&&caps.original_destination&&caps.reply_injection&&caps.loop_bypass&&!caps.unavailable_reason);
}

export function captureProtocolLabel(caps={},diagnostics={}) {
  const ipv6=diagnostics.captureFamily?diagnostics.captureFamily==='dual':!!caps.ipv6;
  return 'TCP / UDP · '+(ipv6?'IPv4 / IPv6':'仅 IPv4');
}
