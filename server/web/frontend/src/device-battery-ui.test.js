import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const app = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

test('registered device list shows battery only for a reporting online device', () => {
 assert.match(app, /d\.status==='online'&&d\.battery&&/);
 assert.match(app, /d\.battery\.level/);
 assert.match(app, /d\.battery\.charging/);
 assert.match(app, /d\.battery\.receivedAt/);
});

test('device details show the live battery status without requiring telemetry', () => {
 assert.match(app, /battery=\{device\.battery\}/);
 assert.match(app, /online&&battery&&/);
 assert.match(app, /battery\.level/);
 assert.match(app, /battery\.receivedAt/);
});
