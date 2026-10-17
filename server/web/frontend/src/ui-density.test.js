import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=relative=>readFileSync(new URL(relative,import.meta.url),'utf8');

test('admin tables support keyboard scrolling and column semantics',()=>{
 const jsx=read('./App.jsx');
 assert.match(jsx,/className="table-wrap" role="region" tabIndex=\{0\}/);
 assert.match(jsx,/aria-label=\{'数据表格：'\+columns\.slice\(0,2\)\.join\('、'\)\}/);
 assert.match(jsx,/<th scope="col" key=\{i\}>\{col\}<\/th>/);
});

test('long admin tables scroll with a sticky header instead of scrolling the entire console',()=>{
 const css=read('./react.css');
 assert.match(css,/#root \.table-wrap\{max-width:100%;max-height:min\(68dvh,720px\);overflow:auto/);
 assert.match(css,/#root \.table-wrap:focus-visible\{outline:2px solid var\(--brand\)/);
 assert.match(css,/#root \.table-wrap thead th\{position:sticky;top:0;z-index:2/);
 assert.match(css,/#root \.table-wrap tbody td\{padding-block:11px\}/);
});
