import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=p=>readFileSync(new URL(p,import.meta.url),'utf8');
const app=read('./App.jsx'),styles=read('./styles.css'),filePage=read('./FileSharingPage.jsx');
const fileCss=read('./fileSharing.css'),browser=read('./FileBrowserV4.jsx'),browserCss=read('./fileBrowserV4.css');

test('PageHead keeps buttons together in one row for wide and narrow Agent windows',()=>{
 assert.match(app,/className="actions page-action-buttons" role="group" aria-label="当前页面快捷操作" tabIndex=\{0\}/);
 assert.match(app,/if\(wide\)return target\?createPortal\(buttons,target\):null/);
 assert.match(styles,/\.page-head\.page-actions-inline>\.page-action-buttons\{[^}]*flex-wrap:nowrap;[^}]*overflow-x:auto/);
 assert.match(styles,/@media\(max-width:820px\)\{\s*\.page-head\.page-actions-inline>\.page-action-buttons\{[^}]*flex-wrap:nowrap/);
 assert.match(styles,/\.page-action-buttons>\.btn\{flex:0 0 auto;white-space:nowrap\}/);
 assert.match(styles,/\.topbar-page-actions \.page-action-buttons\{[^}]*flex-wrap:nowrap;[^}]*overflow-x:auto/);
 assert.doesNotMatch(styles,/\.page-head\.page-actions-inline>\.page-action-buttons\{[^}]*flex-wrap:wrap/);
});

test('sharing header keeps the primary add-folder control first and all actions unwrapped',()=>{
 const header=filePage.slice(filePage.indexOf('className="file-share-list-head"'),filePage.indexOf('{!state&&<div className="empty">'));
 assert.ok(header.indexOf('＋ 添加共享文件夹')<header.indexOf('浏览其他设备文件'));
 assert.match(header,/className="file-share-list-actions" role="group" aria-label="共享文件夹操作" tabIndex=\{0\}/);
 assert.match(fileCss,/\.file-share-list-actions\{[^}]*flex-wrap:nowrap;[^}]*overflow-x:auto/);
 assert.match(fileCss,/\.file-share-list-actions>\.btn\{[^}]*flex:0 0 auto;white-space:nowrap/);
 assert.match(fileCss,/@media\(max-width:800px\)\{\.file-share-list-actions\{[^}]*flex-wrap:nowrap;overflow-x:auto/);
});

test('remote file controls keep the search and upload tools in one row even when narrow',()=>{
 assert.match(browser,/className="v4-toolbar" role="group" aria-label="文件浏览器工具栏" tabIndex=\{0\}/);
 assert.match(browserCss,/\.v4-toolbar\{[^}]*flex-wrap:nowrap;[^}]*overflow-x:auto/);
 assert.match(browserCss,/\.v4-toolbar>\.btn\{flex:0 0 auto;white-space:nowrap\}/);
 assert.match(browserCss,/\.v4-toolbar>select\{flex:0 0 auto;/);
 assert.match(browserCss,/\.v4-toolbar>input\{min-width:120px;flex:1 0 150px/);
 assert.match(browserCss,/@media\(max-width:640px\)\{\s*\.v4-toolbar\{gap:6px\}/);
});
