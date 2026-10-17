import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=relative=>readFileSync(new URL(relative,import.meta.url),'utf8');

test('Server page actions share the title bar on wide desktop windows',()=>{
 const jsx=read('./App.jsx'),css=read('./react.css');
 assert.match(jsx,/import \{createPortal\} from 'react-dom'/);
 assert.match(jsx,/getElementById\('server-page-actions'\)/);
 assert.match(jsx,/if\(wide&&actions\)return <div className="page-head page-actions-toolbar page-head-portal-placeholder"/);
 assert.match(jsx,/createPortal\(<div className="head-actions">\{actions\}<\/div>,target\)/);
 assert.match(jsx,/<div id="server-page-actions" className="topbar-page-actions"\/>/);
 assert.match(css,/#root \.page-head\.page-head-portal-placeholder\{position:absolute;width:1px;height:1px/);
 assert.match(css,/#root \.topbar-page-actions \.head-actions\{display:flex;align-items:center;justify-content:flex-start;flex-wrap:nowrap/);
});

test('Server narrow layouts place page help and buttons together, not at opposite edges',()=>{
 const css=read('./react.css');
 assert.match(css,/#root \.page-head\.page-actions-toolbar\{justify-content:flex-start;gap:8px 12px\}/);
 assert.match(css,/#root \.page-head\.page-actions-toolbar>\.page-context-help\{flex:0 1 auto;max-width:min\(480px,100%\)/);
 assert.match(css,/#root \.page-head\.page-actions-toolbar>\.head-actions\{flex:0 1 auto;margin-left:0;justify-content:flex-start;width:auto/);
 assert.match(css,/#root \.page-head\.page-actions-toolbar>\.head-actions>\.btn\{flex:0 0 auto\}/);
});

test('Browser Sync uses a compact inline context row instead of a second large page header',()=>{
 const jsx=read('./BrowserSync.jsx');
 assert.match(jsx,/className="page-head page-actions-toolbar browser-sync-toolbar"/);
 assert.match(jsx,/<h1 className="sr-only">浏览器同步<\/h1>/);
 assert.match(jsx,/className="page-context-help"/);
 assert.match(jsx,/className="head-actions"/);
 assert.doesNotMatch(jsx,/<div><h1>浏览器同步<\/h1>/);
});
