import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');
test('devices, messages and RDP audit use one responsive filter system',()=>{
 for(const name of ['filter-three device-filters','filter-four','filter-audit'])assert.ok(app.includes(name),name);
 assert.ok(app.includes('role="search" aria-label="已登记设备筛选"'));
 assert.ok(app.includes('role="search" aria-label="消息历史筛选"'));
 assert.ok(app.includes('role="search" aria-label="RDP 审计筛选"'));
 for(const selector of ['#root .filter-bar{','#root .filter-bar.filter-three{','#root .filter-bar.filter-four{','#root .filter-bar.filter-audit{'])assert.ok(css.includes(selector),selector);
});
test('filter controls do not claim whole rows on desktop and stack on phones',()=>{
 assert.match(css,/#root \.filter-bar>\.input\{[^}]*width:100%/);
 assert.match(css,/@media\(max-width:760px\)/);
 assert.match(css,/@media\(max-width:520px\)/);
 assert.match(css,/#root \.filter-bar\{grid-template-columns:minmax\(0,1fr\)/);
});
