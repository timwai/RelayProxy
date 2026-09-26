// Leading underscore keeps this Node regression test out of Go's embedded assets.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const source = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1] + '\n' + fs.readFileSync(path.join(__dirname,'routing.js'),'utf8');

function element() {
  const classes = new Set();
  return {
    value: '', checked: false, disabled: false, textContent: '', dataset: {}, children: [],
    classList: {
      add(...names) { names.forEach(name => classes.add(name)); },
      remove(...names) { names.forEach(name => classes.delete(name)); },
      contains(name) { return classes.has(name); },
      toggle(name, force) { const add = force === undefined ? !classes.has(name) : force; add ? classes.add(name) : classes.delete(name); return add; }
    },
    appendChild(child) { this.children.push(child); }, setAttribute() {}, removeAttribute() {}, focus() {},
    replaceChildren(...children) { this.children = children; }, querySelector() { return null; },
    get innerHTML() { return this.markup || ''; },
    set innerHTML(value) { this.markup = value; this.children = []; }
  };
}

function fixture(options = {}) {
  const elements = new Map();
  const get = id => { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); };
  const buttons = [element(), element()];
  let cfg = {
    revision: 'revision-1', serverAddress: 'relay.example.test', configPath: 'agent.yaml',
    quicPort: 443, tcpPort: 443, transport: 'auto', tlsEnabled: true,
    socks5: { listen: '127.0.0.1', port: 1080, enabled: true },
    http: { listen: '127.0.0.1', port: 8080, enabled: true },
    routing: { mode: 'direct', default_action: 'REJECT', rules: [{ name: 'existing', enabled: true, processes:['browser.exe'], targets:['example.test','192.0.2.0/24'], ports:['443'], protocols:['tcp','udp'], action: 'PROXY', datagram_required: true }] },
    networkMode: '', networkCapabilities: { unavailable_reason: 'interceptor unavailable', hostnames: false },
    network: { mode: '', exclude_processes: ['trusted.exe'] },
    runtime: { networkMode: '', socks5: { listen: '127.0.0.1', port: 1080 }, http: { listen: '127.0.0.1', port: 8080 } },
    restartRequired: false, restartFields: [], reloadPending: false,
    ...options.config
  };
  const saves = [];
  let reloads = 0;
  let loads = 0;
  const context = {
    console, setTimeout() { return 1; }, clearTimeout() {}, setInterval() {}, requestAnimationFrame(fn) { fn(); return 1; },
    URL, confirm() { return options.confirm !== false; },
    document: { getElementById: get, documentElement: element(), addEventListener() {},
      querySelectorAll(selector) { return selector === '[data-config-write]' ? [...buttons, get('routing-rule-save')] : []; },
      createElement: element, createTextNode(text) { return text; } },
    goGetConfig: async () => { loads++; return JSON.stringify(cfg); },
    goGetStatus: async () => JSON.stringify(options.status || { connected: false }),
    goGetRemoteDesktopTargets: async () => JSON.stringify(options.desktopTargets || []),
    goSaveConfig: async raw => {
      const payload = JSON.parse(raw);
      saves.push(payload);
      if (options.rejectSave) return JSON.stringify({ ok: false, message: 'invalid policy' });
      if (payload.routing) cfg.routing = payload.routing;
      if (payload.network) {
        if ('mode' in payload.network) cfg.networkMode = payload.network.mode;
        if ('excludeProcesses' in payload.network) cfg.network.exclude_processes = payload.network.excludeProcesses;
      }
      if (payload.server) {
        Object.assign(cfg, payload.server);
        if ('address' in payload.server) cfg.serverAddress = payload.server.address;
      }
      if (payload.transport) cfg.transport = payload.transport;
      if (payload.proxy) {
        for (const [prefix, key] of [['socks5','socks5'], ['http','http']]) {
          for (const [suffix, target] of [['Enabled','enabled'],['Listen','listen'],['Port','port']]) {
            if (prefix + suffix in payload.proxy) cfg[key][target] = payload.proxy[prefix + suffix];
          }
        }
        if ('defaultExitId' in payload.proxy) cfg.defaultExitId = payload.proxy.defaultExitId;
      }
      if (payload.exit) {
        if ('enabled' in payload.exit) cfg.exitEnabled = payload.exit.enabled;
        for (const key of ['allowInternet','allowPrivateNetwork','allowLoopback']) if (key in payload.exit) cfg[key] = payload.exit[key];
        if (payload.exit.access) { cfg.accessMode = payload.exit.access.mode; cfg.accessDomains = payload.exit.access.domains; cfg.accessCidrs = payload.exit.access.cidrs; }
      }
      if (payload.gui) Object.assign(cfg, payload.gui);
      cfg.revision = 'revision-' + (saves.length + 1);
      const revision = cfg.revision;
      if (options.afterSave) options.afterSave(cfg);
      return JSON.stringify({ ok: true, revision, message: 'saved', restartRequired: cfg.restartRequired });
    },
    goReloadConfig: async () => { reloads++; cfg.reloadPending = false; return JSON.stringify({ ok: true, message: 'reloaded' }); }
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(source, context, { filename: 'index.html' });
  return { context, get, saves, buttons, get reloads() { return reloads; }, get loads() { return loads; }, get config() { return cfg; }, set config(value) { cfg = value; } };
}

test('loading and saving preserves existing routing and sends its revision', async () => {
  const f = fixture();
  await f.context.refreshAll();
  assert.equal(f.get('cfg-routing-mode').value, 'direct');
  assert.equal(f.get('cfg-routing-default').value, 'REJECT');
  assert.equal(f.get('routing-rules-body').children.length, 1);
  assert.equal(await f.context.saveRouting(), true);
  assert.equal(f.saves[0].revision, 'revision-1');
  assert.equal(f.saves[0].routing.rules[0].name, 'existing');
  assert.equal(f.saves[0].routing.rules[0].datagram_required, true);
  assert.equal(f.saves[0].routing.mode, 'direct');
});

test('empty routing list is submitted explicitly', async () => {
  const f = fixture();
  await f.context.refreshAll();
  f.context.removeRuleRow(0);
  assert.equal(await f.context.saveRouting(), true);
  assert.deepEqual(f.saves[0].routing.rules, []);
  assert.equal(f.get('routing-rules-body').children.length, 1); // empty-state row
});

test('failed or incomplete configuration loads cannot save defaults', async () => {
  const f = fixture();
  f.config = { configError: 'invalid YAML' };
  await f.context.refreshAll();
  assert.equal(await f.context.saveRouting(), false);
  assert.equal(f.saves.length, 0);
  assert.ok(f.buttons.every(button => button.disabled));
  assert.match(f.get('config-state').textContent, /invalid YAML/);
});

test('client-only server grant does not lock local exit sharing configuration', async () => {
  const f = fixture({ status: { connected: true, mode: 'CLIENT' } });
  await f.context.refreshAll();
  assert.equal(f.get('cfg-exit-on').disabled, false);
  assert.equal(f.get('cfg-exit-internet').disabled, false);
  assert.match(f.get('share-role-hint').textContent, /仅授权客户端能力/);
  assert.match(f.get('share-role-hint').textContent, /服务端/);
  assert.equal(await f.context.saveShare(), true);
  assert.equal(f.saves[0].exit.enabled, true);
});

test('running endpoints remain separate from saved settings and pending restart', async () => {
  const f = fixture({ config: { socks5: { listen: '127.0.0.1', port: 1088 }, restartRequired: true, restartFields: ['SOCKS5 端口'] } });
  await f.context.refreshAll();
  assert.equal(f.get('cfg-socks-port').value, 1088);
  assert.equal(f.get('home-socks5').textContent, '127.0.0.1:1080');
  assert.match(f.get('config-state').textContent, /SOCKS5 端口/);
  await f.context.saveRouting();
  assert.match(f.get('config-state').textContent, /需重启/);
});

test('shared editor round-trips compound selectors and exclusions without enabling interception', async () => {
  const f = fixture();
  await f.context.refreshAll();
  assert.equal(f.reloads, 0);
  await f.context.saveRouting();
  assert.deepEqual(f.saves[0].routing.rules[0].processes, ['browser.exe']);
  assert.deepEqual(f.saves[0].routing.rules[0].targets, ['example.test','192.0.2.0/24']);
  assert.deepEqual(f.saves[0].routing.rules[0].ports, ['443']);
  assert.equal(f.saves[0].routing.rules[0].datagram_required, true);
  assert.equal(f.saves[0].network.mode, undefined);
  assert.deepEqual(f.saves[0].network.excludeProcesses, ['trusted.exe']);
  assert.equal(f.get('cfg-tun-on').disabled, true);
  await f.context.reloadConfig();
  assert.equal(f.reloads, 1);
});

test('native UDP requirement is read-only in the list and edited through the modal', async () => {
  const f = fixture();
  await f.context.refreshAll();
  const row = f.get('routing-rules-body').children[0].innerHTML;
  assert.match(row, /routing-datagram/);
  assert.match(row, /UDP 原生数据报/);
  assert.doesNotMatch(row, /<input|<select|<textarea|onchange=/);

  f.context.editRuleRow(0);
  assert.equal(f.get('routing-rule-datagram').checked, true);
  f.get('routing-rule-datagram').checked = false;
  f.context.saveRuleEditor();

  await f.context.saveRouting();
  assert.equal(f.saves[0].routing.rules[0].datagram_required, false);
});

test('validation failure is shown without reporting success or discarding edits', async () => {
  const f = fixture({ rejectSave: true });
  await f.context.refreshAll();
  f.context.removeRuleRow(0);
  assert.equal(await f.context.saveRouting(), false);
  assert.equal(f.get('toast-msg').textContent, 'invalid policy');
  assert.equal(f.get('routing-rules-body').children.length, 1);
});

test('rule text is escaped before it is used in an HTML attribute', () => {
  const f = fixture();
  const row = f.context.createRuleRow({ name: '"><img src=x onerror=alert(1)>', targets: ['</textarea><img src=x>'], exit_id: '<exit>', action: 'PROXY' }, 0);
  assert.ok(!row.innerHTML.includes('<img'));
  assert.match(row.innerHTML, /&quot;&gt;&lt;img/);
  assert.match(row.innerHTML, /&lt;exit&gt;/);
});

test('saving one section preserves drafts in the other sections and updates revision', async () => {
  const f = fixture();
  await f.context.refreshAll();
  f.get('cfg-server').value = '192.168.31.14';
  f.get('cfg-socks-port').value = '1099';
  f.get('cfg-acl-domains').value = 'draft.example';
  assert.equal(await f.context.saveRouting(), true);
  assert.equal(f.get('cfg-server').value, '192.168.31.14');
  assert.equal(f.get('cfg-socks-port').value, '1099');
  assert.equal(f.get('cfg-acl-domains').value, 'draft.example');
  assert.equal(f.get('tab-btn-connection').dataset.dirty, 'true');
  assert.equal(await f.context.saveProxy(), true);
  assert.equal(f.saves[1].revision, 'revision-2');
  assert.equal(f.config.socks5.port, 1099);
  assert.equal(f.get('cfg-server').value, '192.168.31.14');
});

test('save all merges shared proxy and network sections into a single atomic update', async () => {
  const f = fixture();
  await f.context.refreshAll();
  f.get('cfg-socks-port').value = '1099';
  f.get('cfg-exit-id').value = 'exit-b';
  f.get('cfg-network-excludes').value = 'updater.exe';
  assert.equal(await f.context.saveAllChanges(), true);
  assert.equal(f.saves.length, 1);
  assert.equal(f.saves[0].proxy.socks5Port, 1099);
  assert.equal(f.saves[0].proxy.defaultExitId, 'exit-b');
  assert.equal(f.saves[0].network.mode, '');
  assert.deepEqual(f.saves[0].network.excludeProcesses, ['updater.exe']);
  assert.ok(f.get('draft-bar').classList.contains('hidden'));
});

test('invalid port text is rejected without silently saving its numeric prefix', async () => {
  const f = fixture();
  await f.context.refreshAll();
  f.get('cfg-socks-port').value = '1080junk';
  assert.equal(await f.context.saveProxy(), false);
  assert.equal(f.saves.length, 0);
  assert.match(f.get('config-form-error').textContent, /SOCKS5.*整数/);
});

test('refresh retains drafts while explicit discard reads the file again', async () => {
  const f = fixture();
  await f.context.refreshAll();
  f.get('cfg-server').value = 'draft.example';
  await f.context.refreshAll();
  assert.equal(f.loads, 1);
  assert.equal(f.get('cfg-server').value, 'draft.example');
  await f.context.discardAllChanges();
  assert.equal(f.loads, 2);
  assert.equal(f.get('cfg-server').value, 'relay.example.test');
  assert.equal(f.reloads, 0);
});

test('external edit after save cannot silently rebase another dirty section', async () => {
  const f = fixture({ afterSave: cfg => { cfg.revision = 'external-edit'; } });
  await f.context.refreshAll();
  f.get('cfg-server').value = 'draft.example';
  assert.equal(await f.context.saveRouting(), false);
  assert.equal(f.get('cfg-server').value, 'draft.example');
  assert.ok(f.buttons.every(button => button.disabled));
  assert.match(f.get('config-state').textContent, /其他操作修改/);
});


test('remote desktop target card reports GPU backend formats and zero-copy directions', async () => {
  const target = {
    deviceId: 'target-gpu',
    name: 'GPU Host',
    online: true,
    capabilities: {
      relayDesktop: true,
      gpu: {
        backend: 'd3d11',
        encodeZeroCopy: true,
        decodeZeroCopy: true,
        displayZeroCopy: true,
        formats: ['nv12', 'ayuv']
      }
    }
  };
  const f = fixture({ desktopTargets: [target] });
  await f.context.refreshRemoteDesktopTargets(false);

  assert.equal(
    f.context.remoteDesktopGPUCapabilityLabel(target),
    'GPU D3D11 NV12/AYUV E/D/R'
  );
  const card = f.get('desktop-targets').children[0];
  const info = card.children[0];
  const meta = info.children[1];
  assert.match(meta.textContent, /GPU D3D11 NV12\/AYUV E\/D\/R/);
});

test('remote desktop GPU path summary distinguishes E2E and display fallback', async () => {
  const target = {
    deviceId: 'target-gpu',
    online: true,
    capabilities: {
      relayDesktop: true,
      gpu: {
        backend: 'd3d11',
        encodeZeroCopy: true,
        decodeZeroCopy: true,
        displayZeroCopy: true,
        formats: ['ayuv']
      }
    }
  };
  const f = fixture({ desktopTargets: [target] });
  await f.context.refreshRemoteDesktopTargets(false);

  const status = { targetId: 'target-gpu', codec: 'h265', chroma: '444' };
  const e2e = {
    captureFormat: 'd3d11-ayuv',
    encoderBackend: 'vendor-encode-d3d11-zero-copy',
    encoderHardware: true,
    decoderBackend: 'vendor-decode-d3d11-zero-copy',
    decoderHardware: true,
    renderBackend: 'd3d11-zero-copy'
  };
  assert.equal(
    f.context.remoteDesktopGPUPathSummary(status, e2e),
    'GPU AYUV E✓/D✓/R✓'
  );

  const fallback = { ...e2e, renderBackend: 'cpu-bgra' };
  assert.equal(
    f.context.remoteDesktopGPUPathSummary(status, fallback),
    'GPU AYUV E✓/D✓/R×'
  );
});

test('remote desktop GPU path summary marks unadvertised runtime format', async () => {
  const f = fixture({
    desktopTargets: [{
      deviceId: 'target-legacy',
      online: true,
      capabilities: { relayDesktop: true }
    }]
  });
  await f.context.refreshRemoteDesktopTargets(false);
  assert.equal(
    f.context.remoteDesktopGPUPathSummary(
      { targetId: 'target-legacy', codec: 'h264', chroma: '420' },
      {
        captureFormat: 'd3d11-nv12',
        encoderBackend: 'media-foundation-d3d11',
        decoderBackend: 'media-foundation-d3d11-zero-copy',
        renderBackend: 'd3d11-zero-copy'
      }
    ),
    'GPU NV12 未声明 E✓/D✓/R✓'
  );
});


test('remote desktop NVIDIA self-test is manual and attached to diagnostics UI', () => {
  assert.match(html, /id="desktop-nvcodec-self-test-btn"/);
  assert.match(html, /onclick="runRemoteDesktopNVCodecSelfTest\(\)"/);
  assert.match(source, /goRunRemoteDesktopNVCodecSelfTest/);
  assert.match(source, /goGetRemoteDesktopNVCodecSelfTest/);
  assert.match(source, /goGetRemoteDesktopNVCodecValidation/);
  assert.match(source, /NVIDIA 验证凭证：当前有效/);
  assert.match(source, /历史记录，不作为当前有效验证/);
  assert.match(source, /qualificationPasses/);
  assert.match(source, /requiredPasses/);
  assert.match(source, /资格 /);
  assert.match(source, /renderRemoteDesktopNVCodecSelfTest/);
  assert.match(source, /NVIDIA GPU 自检通过/);
  assert.match(source, /NVIDIA GPU 自检失败/);
});
