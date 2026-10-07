const servicePrefix = 'relayproxy/agent/gui.WailsService.';

export function hasBridge(name) {
  return typeof window[name] === 'function';
}

export async function call(name, ...args) {
  const fn = window[name];
  if (typeof fn !== 'function') {
    throw new Error(`${name} 接口尚未就绪`);
  }
  return await fn(...args);
}

export function parseJSON(value, fallback = null) {
  if (value == null || value === '') return fallback;
  if (typeof value !== 'string') return value;
  try {
    return JSON.parse(value);
  } catch {
    return fallback;
  }
}

export async function callJSON(name, fallback, ...args) {
  return parseJSON(await call(name, ...args), fallback);
}

export function parseMutation(value) {
  if (value === 'ok') return { ok: true };
  if (value && typeof value === 'object') return value;
  const parsed = parseJSON(value, null);
  if (parsed) return parsed;
  return { ok: false, message: String(value || '操作没有返回结果') };
}

export async function saveConfig(revision, payload) {
  const body = { ...payload, revision };
  return parseMutation(await call('goSaveConfig', JSON.stringify(body)));
}

export function installNativeHooks(handlers) {
  const previous = {
    onGoStatus: window.onGoStatus,
    onGoLog: window.onGoLog,
    onRelayMessage: window.onRelayMessage,
    applyTheme: window.applyTheme,
    syncAutostart: window.syncAutostart,
  };

  window.onGoStatus = handlers.onStatus || (() => {});
  window.onGoLog = handlers.onLog || (() => {});
  window.onRelayMessage = handlers.onMessage || (() => {});
  window.applyTheme = handlers.onTheme || (() => {});
  window.syncAutostart = handlers.onAutostart || (() => {});

  return () => {
    Object.entries(previous).forEach(([key, value]) => {
      if (value === undefined) delete window[key];
      else window[key] = value;
    });
  };
}

export async function invokeRuntime(method, ...args) {
  if (!globalThis.wails?.Call?.ByName) {
    throw new Error('Wails runtime is not ready');
  }
  return globalThis.wails.Call.ByName(servicePrefix + method, ...args);
}
