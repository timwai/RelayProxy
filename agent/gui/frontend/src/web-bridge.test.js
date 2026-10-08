import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Script} from 'node:vm';
import test from 'node:test';

const goSource = readFileSync(new URL('../../web.go', import.meta.url), 'utf8');
const embedded = goSource.match(/const webBridgeJS = `([\s\S]*?)`/);
assert.ok(embedded, 'embedded web bridge source exists');

// This test intentionally compiles the Go-embedded JavaScript. Static API
// string checks missed a literal backslash-n that made the entire bridge
// fail to parse and silently disabled both configuration and exit.
const bridge = new Script(embedded[1], {filename: 'web-bridge.js'});

function createBridge({native = false} = {}) {
  const requests = [];
  const nativeMessages = [];
  const window = {};
  if (native) {
    window.webkit = {messageHandlers: {
      relayproxyLifecycle: {postMessage: message => nativeMessages.push({handler:'lifecycle', message})},
      relayproxyRDP: {postMessage: message => nativeMessages.push({handler:'rdp', message})}
    }};
  }
  const context = {
    window,
    fetch: async (path, options) => {
      requests.push({path, options});
      return {ok: true, text: async () => path === '/api/config'
        ? '{"serverAddress":"previous.relay.test","revision":"123"}'
        : path === '/api/rdp/connect'
          ? '{"ok":true,"listenAddr":"127.0.0.1:13389"}'
          : '{"ok":true}'};
    }
  };
  bridge.runInNewContext(context);
  return {window, requests, nativeMessages};
}

test('browser bridge loads persisted config over the authenticated HTTP API', async () => {
  const {window, requests} = createBridge();
  assert.equal(typeof window.goGetConfig, 'function');
  const config = JSON.parse(await window.goGetConfig());
  assert.equal(config.serverAddress, 'previous.relay.test');
  assert.equal(requests[0].path, '/api/config');
  assert.equal(requests[0].options.credentials, 'same-origin');
});

test('browser quit sends POST and does not depend on native shell', async () => {
  const {window, requests} = createBridge();
  await window.goQuit();
  assert.equal(requests.length, 1);
  assert.equal(requests[0].path, '/api/quit');
  assert.equal(requests[0].options.method, 'POST');
});

test('macOS shell quit is owned by native lifecycle handler', async () => {
  const {window, requests, nativeMessages} = createBridge({native: true});
  assert.deepEqual(nativeMessages, []);
  const result = JSON.parse(await window.goQuit());
  assert.equal(result.ok, true);
  assert.deepEqual(nativeMessages, [{handler:'lifecycle', message:'quit'}]);
  assert.equal(requests.length, 0);
  await window.goRestart();
  assert.deepEqual(nativeMessages, [
    {handler:'lifecycle', message:'quit'},
    {handler:'lifecycle', message:'restart'}
  ]);
});


test('macOS native shell owns Windows App launch after RDP tunnel is ready', async () => {
  const {window, requests, nativeMessages} = createBridge({native: true});
  const result = JSON.parse(await window.goConnectRDP('dev-rdp', true));
  assert.equal(result.listenAddr, '127.0.0.1:13389');
  assert.equal(requests.length, 1);
  assert.equal(requests[0].path, '/api/rdp/connect');
  const body = JSON.parse(requests[0].options.body);
  assert.equal(body.targetId, 'dev-rdp');
  assert.equal(body.autoLaunch, false);
  assert.deepEqual(nativeMessages, [{
    handler: 'rdp',
    message: {address: '127.0.0.1:13389'}
  }]);
});

test('browser RDP keeps agent-side auto launch behavior', async () => {
  const {window, requests, nativeMessages} = createBridge();
  await window.goConnectRDP('dev-rdp', true);
  assert.equal(JSON.parse(requests[0].options.body).autoLaunch, true);
  assert.deepEqual(nativeMessages, []);
});
