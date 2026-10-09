// The dashboard exposes current counters rather than a historical time series.
// Keep a bounded, in-memory history of actual API samples for the active identity.
export const SAMPLE_INTERVAL_MS = 10_000;
export const MAX_SAMPLES = 60;

export const nonnegative = value => {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : 0;
};

export function sessionCounters(sessions = []) {
  const totals = new Map();
  for (const session of Array.isArray(sessions) ? sessions : []) {
    if (!session || !session.clientDeviceId) continue;
    // A reconnect changes connectedAt and must not produce negative or fake spikes.
    const key = `${session.clientDeviceId}\u0000${session.connectedAt || ''}`;
    totals.set(key, {up: nonnegative(session.bytesUp), down: nonnegative(session.bytesDown)});
  }
  return totals;
}

export function routeBreakdown(sessions = []) {
  const counts = {p2p: 0, publicDirect: 0, relay: 0, unknown: 0};
  for (const session of Array.isArray(sessions) ? sessions : []) {
    const path = session?.peerDiagnostics?.payload?.status?.directPath;
    if (path === 'p2p_quic') counts.p2p++;
    else if (path === 'public_direct_quic') counts.publicDirect++;
    else if (path === 'relay_quic' || path === 'relay_tls') counts.relay++;
    else counts.unknown++;
  }
  return counts;
}

export function recordTelemetry(dashboard, sessions, p2pSessions, previous, timestamp) {
  if (!Number.isFinite(timestamp)) throw new Error('invalid telemetry timestamp');
  const nowCounters = sessionCounters(sessions);
  const elapsed = previous ? (timestamp - previous.timestamp) / 1000 : 0;
  let upBps = null, downBps = null;
  if (previous && elapsed > 0) {
    let uploaded = 0, downloaded = 0;
    for (const [key, values] of nowCounters) {
      const before = previous.counters.get(key);
      // Only compare matching, continuous sessions. Newly connected clients
      // have no prior counter, so their pre-sample traffic is not estimated.
      if (!before) continue;
      if (values.up >= before.up) uploaded += values.up - before.up;
      if (values.down >= before.down) downloaded += values.down - before.down;
    }
    upBps = uploaded / elapsed;
    downBps = downloaded / elapsed;
  }
  const currentExits = Array.isArray(dashboard.exitNodes) ? dashboard.exitNodes : [];
  const exits = currentExits.map(exit => ({
    id: String(exit.deviceId || ''),
    name: String(exit.deviceName || exit.deviceId || '未命名出口'),
    streams: nonnegative(exit.activeStreams)
  }));
  const sample = {
    timestamp,
    online: nonnegative(dashboard.onlineDevices),
    connections: nonnegative(dashboard.activeConnections),
    p2p: nonnegative(dashboard.activeP2PSessions),
    availableExits: nonnegative(dashboard.onlineExits),
    todayUpload: nonnegative(dashboard.todayUpload),
    todayDownload: nonnegative(dashboard.todayDownload),
    uploadBps: upBps,
    downloadBps: downBps,
    exits,
    routes: routeBreakdown(sessions),
    sessionCount: Array.isArray(sessions) ? sessions.length : 0,
    p2pNegotiations: Array.isArray(p2pSessions) ? p2pSessions.length : 0
  };
  return {sample, counters: nowCounters, timestamp};
}

export function appendSample(samples, sample, limit = MAX_SAMPLES) {
  return [...samples, sample].slice(-limit);
}

export function formatRate(value) {
  if (value == null) return '等待采样';
  const bytes = nonnegative(value);
  const units = ['B/s', 'KiB/s', 'MiB/s', 'GiB/s', 'TiB/s'];
  let n = bytes, unit = 0;
  while (n >= 1024 && unit < units.length - 1) { n /= 1024; unit++; }
  return `${n.toFixed(unit === 0 ? 0 : n >= 100 ? 0 : n >= 10 ? 1 : 2)} ${units[unit]}`;
}
