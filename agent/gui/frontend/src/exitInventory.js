export function exitInventoryKey(status = {}) {
  const summary = Array.isArray(status.proxyExits) ? status.proxyExits : [];
  return String(status.proxyExitRevision || 0) + '|' + summary.map(exit => [
    exit?.deviceId || exit?.id || '',
    exit?.name || '',
    exit?.online === false ? '0' : '1',
    exit?.authorizationSource || '',
  ].join(':')).join(';');
}
