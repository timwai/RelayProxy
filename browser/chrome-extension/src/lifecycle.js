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


// An overwrite grant permits at most ONE signed incoming snapshot. It cannot
// be a persistent boolean; legacy grants are rejected after an upgrade.
export const OVERWRITE_GRANT_TTL_MS = 2 * 60 * 1000;
export function validOverwriteGrant(grant, siteOrigin, now, requestId) {
  // A matching, source-signed transfer request ID is required. Old grants
  // without a token never authorize account switching after an upgrade.
  return !!grant && typeof grant === 'object' && !Array.isArray(grant) &&
    typeof siteOrigin === 'string' && siteOrigin.length > 0 &&
    typeof requestId === 'string' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(requestId) &&
    grant.transferRequestId === requestId &&
    grant.siteOrigin === siteOrigin &&
    Number.isSafeInteger(grant.expiresAt) &&
    Number.isFinite(now) && grant.expiresAt > now &&
    grant.expiresAt - now <= OVERWRITE_GRANT_TTL_MS;
}

// Chrome alarms have one-minute cadence. Persist retry state across MV3
// suspension; do not start a tight loop during a WSS failure or auth denial.
export const RECONNECT_BASE_MS = 60 * 1000;
export const RECONNECT_MAX_MS = 15 * 60 * 1000;
export function nextReconnectDelay(failures) {
  if (!Number.isSafeInteger(failures) || failures <= 0) return RECONNECT_BASE_MS;
  return Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS *
    (2 ** Math.min(failures - 1, 10)));
}
export function shouldRetryConnection(retry, origin, now) {
  if (!origin || !Number.isFinite(now) || now <= 0) return false;
  if (!retry || retry.serverOrigin !== origin) return true;
  // A corrupted future timestamp must not disable reconnect indefinitely.
  return !Number.isFinite(retry.nextRetryAt) || retry.nextRetryAt <= now ||
    retry.nextRetryAt > now + RECONNECT_MAX_MS;
}

// Coalesce simultaneous onStartup/onInstalled/alarm invocations while one
// reconciliation runs. A rejected run must never poison future retries.
// The factory has no timers/storage and can be tested independently of Chrome.
export function singleFlight(task) {
  let inFlight = null;
  return (...args) => {
    if (inFlight) return inFlight;
    const promise = Promise.resolve().then(() => task(...args));
    inFlight = promise;
    // Clean up on BOTH success and failure without creating an unobserved
    // rejecting promise from finally().
    promise.finally(() => {
      if (inFlight === promise) inFlight = null;
    }).catch(() => {});
    return promise;
  };
}

// A browser can receive APPLIED before the earlier RELAYED response is
// processed. Never allow that older, non-terminal status to erase the final
// result for the same opaque message ID. A new message may start fresh.
// This pure reducer intentionally stores no website content or credentials.
export function nextDeliveryStatus(previous,state,messageId='',now=Date.now()){
  const terminal=new Set(['APPLIED','FAILED','CONFLICT','UNKNOWN','PARTIAL']);
  if(messageId&&previous?.messageId&&previous.messageId!==messageId&&state!=='SENDING')
    return previous;
  if(messageId&&previous?.messageId===messageId&&terminal.has(previous.state))
    return previous;
  return {state,time:now,...(messageId?{messageId}:{})};
}
