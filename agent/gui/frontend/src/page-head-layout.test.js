import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const read = relative => readFileSync(new URL(relative, import.meta.url), 'utf8');
const styles = read('./styles.css');
const app = read('./App.jsx');
const rule = selector => {
 const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
 return styles.match(new RegExp(escaped + '\\{([^}]*)\\}'))?.[1] || '';
};

test('top bar is the only global page title; actions live in a compact row',()=>{
 assert.match(app,/function PageHead\(\{actions\}\)/);
 assert.match(app,/page-head page-actions-toolbar/);
 assert.doesNotMatch(app,/function PageHead\([^)]*\)[^{]*\{return <div className="page-head"><div><h1>/);
 assert.match(app,/<header className="topbar"><div className="crumb"><b>{title}<\/b>/);
 assert.match(app,/<PageHead title="分流规则"[^\n]*DNS 设置 ›[^\n]*＋ 新建规则/);
 assert.match(rule('.page-head'),/justify-content:flex-end/);
 assert.match(rule('.page-head>.actions'),/flex-wrap:\s*wrap/);
 assert.match(rule('.page-head>.actions>.btn'),/white-space:\s*nowrap/);
});

test('compact desktop and mobile header do not stack large duplicate titles',()=>{
 assert.match(rule('.topbar'),/height:44px/);
 assert.match(rule('.main'),/padding:14px 20px 26px/);
 const narrow=styles.split('@media(max-width:820px)')[1]?.split('@media(')[0]||'';
 assert.match(narrow,/\.page-head\{flex-direction:row\}/);
 assert.match(narrow,/\.page-head>\.actions\{justify-content:flex-end;overflow-x:auto\}/);
});
