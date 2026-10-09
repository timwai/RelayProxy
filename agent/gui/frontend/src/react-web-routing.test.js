import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const read = relative => readFileSync(new URL(relative, import.meta.url), 'utf8');

test('PowerShell packages Agent React before Linux and macOS binaries', () => {
  const script = read('../../../../scripts/build.ps1');
  const frontend = script.indexOf('# --- Shared Agent React frontend (Web, Wails and macOS) ---');
  const linux = script.indexOf('# --- Linux server/agent');
  const build = script.indexOf('& npm run build', frontend);
  const testStep = script.indexOf('& npm test', frontend);
  const verify = script.indexOf('Assert-ReactBundle', frontend);
  assert.ok(frontend > -1 && linux > -1 && frontend < linux, 'Agent UI must be built before cross-platform Go binaries');
  assert.ok(frontend < testStep && testStep < build && build < verify && verify < linux,
    'React tests, Vite build and bundle validation must precede all Agent binaries');
});

test('Unix release builds embed React before the Agent', () => {
  const script = read('../../../../scripts/build.sh');
  const frontend = script.indexOf('\nbuild_react_frontend\n');
  const agent = script.indexOf('build_one linux amd64 ./cmd/relay-agent');
  assert.ok(frontend > -1 && agent > frontend, 'Agent bundle must exist before Linux builds');
});

test('React accepts only known deep-linked pages', () => {
  const app = read('./App.jsx');
  const server = read('../../web.go');
  assert.match(app, /const PAGE_IDS = new Set/);
  assert.match(app, /new URLSearchParams\(window\.location\.search\)\.get\('page'\)/);
  assert.match(app, /if \(PAGE_IDS\.has\(requested\)\) return requested;/);
  assert.match(app, /useState\(initialPage\)/);
  assert.match(server, /http\.Redirect\(rw, r, "\/\?page=monitor", http\.StatusSeeOther\)/);
});
