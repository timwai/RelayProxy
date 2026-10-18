import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const read=relative=>readFileSync(new URL(relative,import.meta.url),'utf8');
const styles=read('./styles.css'),app=read('./App.jsx');

test('global title and page actions occupy one header row on spacious desktop windows',()=>{
 assert.match(app,/import \{createPortal\} from 'react-dom'/);
 assert.match(app,/function PageHead\(\{actions\}\)/);
 assert.match(app,/getElementById\('agent-page-actions'\)/);
 assert.match(app,/if\(wide\)return target\?createPortal\(buttons,target\):null/);
 assert.match(app,/<div id="agent-page-actions" className="topbar-page-actions"\/>/);
 assert.match(app,/<header className="topbar"><div className="crumb">\{category&&/);
 assert.match(app,/<b>\{title\}<\/b>/);
 assert.match(app,/<PageHead title="分流规则"[^\n]*DNS 设置 ›[^\n]*＋ 新建规则/);
 assert.match(styles,/\.topbar-page-actions \.page-action-buttons\{display:flex;align-items:center;flex-wrap:nowrap/);
});

test('narrow Agent windows show controls from the left without a blank right-only row',()=>{
 assert.match(app,/if\(wide\)return target\?createPortal\(buttons,target\):null;\s*return <div className="page-head page-actions-inline">/);
 assert.match(styles,/\.page-head\{display:flex;align-items:center;justify-content:flex-start/);
 assert.match(styles,/\.page-head\.page-actions-inline\{justify-content:flex-start/);
 assert.match(styles,/\.page-head\.page-actions-inline>\.page-action-buttons\{justify-content:flex-start/);
 assert.match(styles,/@media\(max-width:820px\)\{[\s\S]*?\.page-head>\.actions\{justify-content:flex-start;overflow-x:auto\}/);
 assert.match(styles,/\.topbar\{height:44px;min-height:44px/);
 assert.match(styles,/\.main\{flex:1 1 auto;height:0;[\s\S]*?padding:14px 20px 26px/);
});

test('file sharing is tab-free and local folder actions remain accessible on narrow layouts',()=>{
 const css=read('./fileSharing.css');
 assert.match(css,/\.file-share-list-actions\{display:flex;align-items:center;justify-content:flex-end;flex-wrap:wrap/);
 assert.match(css,/\.file-sharing-host\.is-remote:not\(\[hidden\]\)\{display:flex;flex-direction:column/);
});
