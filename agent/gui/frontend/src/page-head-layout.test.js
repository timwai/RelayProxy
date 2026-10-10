import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const read = relative => readFileSync(new URL(relative, import.meta.url), 'utf8');
const styles = read('./styles.css');
const app = read('./App.jsx');

// CSS-only regression guard: header buttons were inheriting .actions'
// flex-wrap:wrap and ended up on separate lines in a narrowed Wails window.
const rule = selector => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return styles.match(new RegExp(escaped + '\\{([^}]*)\\}'))?.[1] || '';
};

test('page header buttons never wrap while the title can shrink', () => {
  assert.match(app, /function PageHead\([^)]*\)[^{]*\{return <div className="page-head">/);
  assert.match(app, /title="分流规则"[\s\S]*?actions=\{<>\s*<Button[^>]*>DNS 设置/);
  assert.match(app, /<Button primary onClick=\{\(\)=>edit\(-1\)\}>＋ 新建规则<\/Button>/);
  assert.match(rule('.page-head>div:first-child'), /min-width:\s*0/);
  assert.match(rule('.page-head>.actions'), /flex-wrap:\s*nowrap/);
  assert.match(rule('.page-head>.actions'), /flex:\s*0 0 auto/);
  assert.match(rule('.page-head>.actions>.btn'), /white-space:\s*nowrap/);
  assert.match(rule('.page-head>.actions>.btn'), /flex:\s*0 0 auto/);
});

test('narrow layout stacks heading above a single horizontal action row', () => {
  const narrow = styles.split('@media(max-width:820px)')[1]?.split('@media(')[0] || '';
  assert.match(narrow, /\.page-head\{flex-direction:column\}/);
  assert.match(narrow, /\.page-head>\.actions\{justify-content:flex-start;overflow-x:auto\}/);
});
