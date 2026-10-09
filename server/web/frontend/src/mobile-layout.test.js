import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const jsx=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');

test('mobile navigation drawer has access to every existing desktop page',()=>{
 assert.match(jsx,/const \[mobileNavOpen,setMobileNavOpen\]=useState\(false\)/);
 assert.match(jsx,/className=\{mobileNavOpen\?'mobile-nav-open':''\}/);
 assert.match(jsx,/className="mobile-menu-toggle"/);
 assert.match(jsx,/aria-expanded=\{mobileNavOpen\}/);
 assert.match(jsx,/aria-controls="server-navigation"/);
 assert.match(jsx,/className="mobile-nav-overlay"/);
 assert.match(jsx,/setMobileNavOpen\(false\);location\.hash/);
 assert.match(css,/#root #app\.mobile-nav-open \.sidebar\{transform:translateX\(0\)\}/);
 assert.match(css,/#root #app \.sidebar \.nav-item>span:nth-child\(2\)\{display:inline\}/);
});

test('mobile content uses available viewport with horizontal table scrolling',()=>{
 for(const rule of ['grid-template-columns:minmax(0,1fr);width:100%;height:100dvh','#root .main-inner{padding:14px 10px 65px}','#root .table-wrap{max-width:100%;overflow-x:auto}'])assert.ok(css.includes(rule),rule);
 assert.match(css,/#root \.filter-bar\.filter-three,#root \.filter-bar\.filter-four,#root \.filter-bar\.filter-audit,#root \.filter-bar\{grid-template-columns:minmax\(0,1fr\)\}/);
});
