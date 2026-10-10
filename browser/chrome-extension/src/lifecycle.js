// Pure browser-sync lifecycle guards, independently testable without Chrome.
export const RECONCILE_INTERVAL_MS = 5 * 60 * 1000;
export const DELIVERY_ACK_TIMEOUT_MS = 15 * 60 * 1000;

export function shouldReconcile(last, now) {
  if (!Number.isFinite(now) || now <= 0) return false;
  if (!Number.isFinite(last) || last < 0 || last > now) return true;
  return now - last >= RECONCILE_INTERVAL_MS;
}

// Only acknowledgements for the latest source-generated message may update
// UI status. A stale ACK cannot overwrite the result of a later snapshot.
export function acceptDeliveryAck(pending, ack, now) {
  if (!pending || !ack || typeof ack !== 'object') return false;
  if (pending.messageId !== ack.messageId || pending.ruleId !== ack.ruleId) return false;
  if (!Number.isFinite(pending.createdAt) || now < pending.createdAt ||
    now-pending.createdAt > DELIVERY_ACK_TIMEOUT_MS) return false;
  return ['RECEIVED','APPLIED','FAILED','CONFLICT'].includes(ack.status);
}

export function isTerminalAck(status) {
  return status === 'APPLIED' || status === 'FAILED' || status === 'CONFLICT';
}

export function nextSessionSequence(localSequence, serverCursor) {
  if (!Number.isSafeInteger(localSequence) || localSequence < 0 ||
      !Number.isSafeInteger(serverCursor) || serverCursor < 0)
    throw new Error('无法安全恢复会话序号');
  const next = Math.max(localSequence, serverCursor) + 1;
  if (!Number.isSafeInteger(next)) throw new Error('会话序号耗尽，必须重新配对');
  return next;
}
