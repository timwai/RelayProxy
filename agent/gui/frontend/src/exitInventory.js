export function exitInventoryKey(status = {}) {
  const summary = Array.isArray(status.proxyExits) ? status.proxyExits : [];
  return String(status.proxyExitRevision || 0) + '|' + summary.map(exit => [
    exit?.deviceId || exit?.id || '',
    exit?.name || '',
    exit?.online === false ? '0' : '1',
    exit?.authorizationSource || '',
  ].join(':')).join(';');
}

// exitUsable reports whether an exit id can be the target of an action such as a
// speed test: it must exist in the inventory and must not be marked offline.
export function exitUsable(exits, id) {
  if (!id) return false;
  const list = Array.isArray(exits) ? exits : [];
  return list.some(exit => (exit?.deviceId || exit?.id) === id && exit?.online !== false);
}

// selectedExitUnavailable mirrors the exit list semantics: an exit missing from
// the authoritative inventory is unavailable regardless of connection state, but
// a cached online=false flag only counts while the Relay session is live.
export function selectedExitUnavailable({ exitsReady, connected, exits, selected }) {
  if (!exitsReady || !selected) return false;
  const list = Array.isArray(exits) ? exits : [];
  const item = list.find(exit => (exit?.deviceId || exit?.id) === selected);
  if (!item) return true;
  return !!connected && item.online === false;
}
