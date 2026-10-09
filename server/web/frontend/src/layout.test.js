import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('./react.css', import.meta.url), 'utf8');
const app = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

test('overview cards share a single grid row without sibling section margins', () => {
  assert.match(app, /className="grid two-one page-section"/);
  assert.match(app, /<Panel title="Relay 节点状态"/);
  assert.match(app, /<Panel title="快捷入口"/);
  // The adjacent-section rule must not target two Panel children of the grid.
  assert.doesNotMatch(css, /#root\s+\.page-section\s*\+\s*\.page-section\s*\{/);
  assert.match(css, /#root\s+\.main-inner\s*>\s*\.page-section\s*\+\s*\.page-section\s*\{\s*margin-top:\s*16px/);
  assert.match(css, /#root\s+\.grid\s*>\s*\.page-section\s*\{[^}]*margin-top:\s*0\s*;/);
  assert.match(css, /#root\s+\.grid\s*>\s*\.page-section\s*\{[^}]*align-self:\s*stretch\s*;/);
});

test('nested grid cards keep narrow-screen stacking', () => {
  assert.match(readFileSync(new URL('./prototype.css',import.meta.url), 'utf8'), /\.grid\.two-one\{grid-template-columns:1fr\}/);
});
