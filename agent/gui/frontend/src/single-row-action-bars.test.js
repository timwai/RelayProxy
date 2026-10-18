import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=p=>readFileSync(new URL(p,import.meta.url),'utf8');
const app=read('./App.jsx'),styles=read('./styles.css'),filePage=read('./FileSharingPage.jsx');
const fileCss=read('./fileSharing.css'),browser=read('./FileBrowserV4.jsx'),browserCss=read('./fileBrowserV4.css');

test('PageHead uses a primary action and a compact overflow menu',()=>{
 assert.match(app,/className="page-head page-actions-contextual"/);
 assert.match(app,/className="page-more-menu"/);
 assert.match(app,/aria-label="更多页面操作"/);
 assert.match(styles,/\.page-head\.page-actions-contextual\{[^}]*flex-wrap:nowrap/);
 assert.match(app,/createPortal\(controls,host\)/);
});

test('sharing header exposes file browsing directly with compact accessible actions',()=>{
 const header=filePage.slice(filePage.indexOf('className="card-head file-share-list-head"'),filePage.indexOf('{!state&&<div className="empty">'));
 assert.match(header,/className="file-share-list-actions" role="group" aria-label="共享文件夹操作"/);
 assert.match(header,/<button type="button" className="btn primary" aria-label="浏览其他设备文件"[^>]*onClick=\{\(\)=>\{setTab\('remote'\);void refreshPeers\(\)\}\}><Icon name="devices" size=\{15\}\/>浏览文件<\/button>/);
 assert.match(header,/aria-label="添加共享文件夹"[^>]*onClick=\{\(\)=>edit\(null\)\} disabled=\{!state\|\|busy\}>＋ 添加<\/button>/);
 assert.match(header,/aria-label="刷新共享列表" title="刷新共享列表" onClick=\{refresh\} disabled=\{busy\}><Icon name="refresh" size=\{16\}\/><\/button>/);
 assert.doesNotMatch(header,/<details|<summary/);
 assert.match(fileCss,/\.file-share-list-head\{flex-wrap:nowrap;min-width:0\}/);
 assert.match(fileCss,/\.file-share-list-actions\{[^}]*max-width:100%;[^}]*overflow:visible/);
 assert.match(fileCss,/\.file-share-list-actions>\.file-share-refresh\{[^}]*width:34px;height:34px/);
 assert.match(fileCss,/@media\(max-width:800px\)\{\.file-share-list-head\{flex-wrap:nowrap\}\.file-share-list-actions\{[^}]*overflow:visible/);
});

test('remote file controls keep the search and upload tools in one row even when narrow',()=>{
 assert.match(browser,/className="v4-toolbar" role="group" aria-label="文件浏览器工具栏" tabIndex=\{0\}/);
 assert.match(browserCss,/\.v4-toolbar\{[^}]*flex-wrap:nowrap;[^}]*overflow-x:auto/);
 assert.match(browserCss,/\.v4-toolbar>\.btn\{flex:0 0 auto;white-space:nowrap\}/);
 assert.match(browserCss,/\.v4-toolbar>select\{flex:0 0 auto;/);
 assert.match(browserCss,/\.v4-toolbar>input\{min-width:120px;flex:1 0 150px/);
 assert.match(browserCss,/@media\(max-width:640px\)\{\s*\.v4-toolbar\{gap:6px\}/);
});
