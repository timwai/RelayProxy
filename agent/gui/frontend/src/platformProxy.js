// Windows native Wails builds expose a Windows-only Network Service bridge.
// Older native binaries may omit the platform field from GetConfig, so keep
// their transparent-proxy controls visible based on that trusted bridge.
export function showWindowsCapture(platform, hasNetworkServiceBridge) {
  const value = String(platform ?? '').trim().toLowerCase();
  return value === 'windows' || (value === '' && !!hasNetworkServiceBridge);
}

export function showWindowsService(platform, hasNetworkServiceBridge) {
  return showWindowsCapture(platform, hasNetworkServiceBridge) && !!hasNetworkServiceBridge;
}
