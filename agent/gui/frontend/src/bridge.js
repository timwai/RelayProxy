const servicePrefix = 'relayproxy/agent/gui.WailsService.';

export function hasBridge(name) {
  return typeof window[name] === 'function';
}

export async function call(name, ...args) {
  let lastError = null;
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const fn = window[name];
    if (typeof fn === 'function') {
      try {
        return await fn(...args);
      } catch (error) {
        lastError = error;
        if (!/wails runtime is not ready/i.test(String(error?.message || error))) {
          throw error;
        }
      }
    }
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  throw lastError || new Error(`${name} 接口尚未就绪`);
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

// Config snapshots include read-only password-presence indicators. Never
// echo them into the strictly decoded ConfigUpdate API, and never turn a
// redacted/blank password input into a request to clear a stored secret.
// Clearing is only possible through an explicit clearPassword action.
export function normalizeConfigUpdate(payload) {
  const cleaned={...payload};
  if(payload?.exit?.upstream){
    const {hasPassword,clearPassword,password,...fields}=payload.exit.upstream;
    const upstream={...fields};
    if(clearPassword===true)upstream.password='';
    else if(typeof password==='string' && password.length>0)upstream.password=password;
    cleaned.exit={...payload.exit,upstream};
  }
  if(Array.isArray(payload?.proxy?.customExits)){
    cleaned.proxy={...payload.proxy,customExits:payload.proxy.customExits.map(item=>{
      const {hasPassword,clearPassword,password,...fields}=item;
      if(clearPassword===true)return {...fields,password:''};
      if(typeof password==='string' && password.length>0)return {...fields,password};
      return fields;
    })};
  }
  return cleaned;
}

export async function saveConfig(revision, payload) {
  const body = { ...normalizeConfigUpdate(payload), revision };
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
