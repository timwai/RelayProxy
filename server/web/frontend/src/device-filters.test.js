import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const app = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');
const css = readFileSync(new URL('./react.css', import.meta.url), 'utf8');

test('registered-device filters have a dedicated horizontal layout', () => {
  assert.match(app, /title="已登记设备"[\s\S]*?className="device-filters"/);
  assert.match(app, /className="device-filters"><input[^>]*placeholder="设备名称 \/ ID \/ 身份"/);
  assert.match(css, /#root \.device-filters\s*\{[^}]*display:grid;/);
  assert.match(css, /grid-template-columns:minmax\(220px, 1fr\) repeat\(2, minmax\(140px, 185px\)\)/);
  assert.match(css, /#root \.device-filters > \.input\s*\{[^}]*width:100%;[^}]*min-width:0;/);
});

test('registered-device filters remain responsive on narrow screens', () => {
  assert.match(css, /@media \(max-width: 860px\)\s*\{[\s\S]*?#root \.device-filters > input\.input\s*\{grid-column:1 \/ -1;\}/);
  assert.match(css, /@media \(max-width: 520px\)\s*\{[\s\S]*?#root \.device-filters\s*\{grid-template-columns:minmax\(0, 1fr\);\}/);
  assert.match(app, /aria-label="按设备状态筛选"/);
  assert.match(app, /aria-label="按设备角色筛选"/);
});
