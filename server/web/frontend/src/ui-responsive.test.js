import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=file=>readFileSync(new URL(file,import.meta.url),'utf8');

test('collapsed Server nav always exposes page name and selected route',()=>{
 const app=read('./App.jsx');
 assert.match(app,/title=\{label\} aria-label=\{label\} aria-current=\{page===p\?'page':undefined\}/);
 assert.match(app,/aria-label=\{'账户管理：'\+\(user.username\|\|'当前账户'\)\}/);
});

test('theme switching stays available in mobile drawer',()=>{
 const app=read('./App.jsx'),css=read('./react.css');
 assert.match(app,/className="mobile-theme-controls" role="group" aria-label="主题模式"/);
 assert.match(app,/aria-pressed=\{theme===v\}/);
 assert.match(css,/#root \.mobile-theme-controls\{display:none\}/);
 assert.match(css,/#root \.mobile-theme-controls\{display:flex;gap:4px/);
 assert.match(css,/html\[data-theme="dark"\] #root\{color-scheme:dark\}/);
});

test('closed mobile nav is hidden from keyboard and Escape restores focus',()=>{
 const app=read('./App.jsx'),css=read('./react.css');
 assert.match(app,/if\(!mobileNavOpen\)return;const previous=document.activeElement/);
 assert.match(app,/if\(e.key==='Escape'\)\{e.preventDefault\(\);setMobileNavOpen\(false\)\}/);
 assert.match(app,/if\(previous instanceof HTMLElement&&previous.isConnected\)previous.focus\(\)/);
 assert.match(css,/#root #app \.sidebar\{visibility:hidden;pointer-events:none\}/);
 assert.match(css,/#root #app\.mobile-nav-open \.sidebar\{visibility:visible;pointer-events:auto\}/);
});
