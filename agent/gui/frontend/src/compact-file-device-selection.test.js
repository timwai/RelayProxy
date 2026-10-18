import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=p=>readFileSync(new URL(p,import.meta.url),'utf8');
const browser=read('./FileBrowserV4.jsx'),files=read('./FileSharingPage.jsx');
const css=read('./fileBrowserV4.css'),fileCss=read('./fileSharing.css');
const slice=(start,end)=>{const a=browser.indexOf(start),b=browser.indexOf(end,a);assert.ok(a>=0&&b>a);return browser.slice(a,b)};

test('browser has ONE top navigation bar and peer is first breadcrumb, not a title/select row',()=>{
 const toolbar=slice('   <div className="v4-tools v4-unified-pathbar">','   <div className="v4-toolbar">');
 assert.doesNotMatch(browser,/<div className="v4-head">/);
 assert.doesNotMatch(browser,/className="v4-device-switch"/);
 assert.match(toolbar,/aria-label="文件夹路径"/);
 assert.match(toolbar,/aria-label="切换共享设备"/);
 assert.match(toolbar,/className="v4-peer-trigger"/);
 assert.match(toolbar,/className="v4-peer-trigger-name"/);
 assert.match(toolbar,/role="dialog" aria-modal="false" aria-label="选择共享设备"/);
 assert.match(toolbar,/aria-label="搜索共享设备"/);
 assert.match(toolbar,/availablePeers\.map\(peer=><button type="button" key=\{peer\.deviceId\} role="option"/);
 assert.match(toolbar,/className="v4-path-actions"/);
 assert.match(css,/\.file-browser-v4 \.v4-unified-pathbar\{position:relative;flex:0 0 auto;min-height:44px/);
 assert.match(css,/\.v4-main \.v4-peer-popover\{position:absolute;z-index:35/);
});

test('searchable device selector only lists the authorized peer inventory, keyboard and outside dismiss',()=>{
 assert.match(browser,/const availablePeers=peers\.filter\(peer=>\[peer\.name,peer\.deviceId\]/);
 assert.match(browser,/if\(!peers\.some\(peer=>peer\.deviceId===id\)\)return/);
 assert.match(browser,/if\(id!==device\)openDevice\(id\)/);
 assert.match(browser,/peerSearchRef\.current\?\.focus\(\)/);
 assert.match(browser,/e\.key==='Enter'&&availablePeers\.length/);
 assert.match(browser,/e\.key==='Escape'/);
 assert.match(browser,/document\.addEventListener\('pointerdown',outside\)/);
 assert.match(browser,/ref=\{peerButtonRef\}/);
 assert.match(browser,/aria-expanded=\{peerPickerOpen\}/);
 assert.match(css,/@media\(max-width:640px\)\{[\s\S]*?\.v4-main \.v4-peer-popover\{top:auto!important;bottom:8px/);
});

test('return, standalone and focus actions are in popover rather than a header band',()=>{
 const toolbar=slice('   <div className="v4-tools v4-unified-pathbar">','   <div className="v4-toolbar">');
 assert.match(toolbar,/aria-label="更多浏览器操作"/);
 assert.match(toolbar,/role="menu" aria-label="更多文件浏览器操作"/);
 assert.match(toolbar,/返回我的共享/);
 assert.match(toolbar,/原生独立窗口/);
 assert.match(toolbar,/className="btn v4-window-link"/);
 assert.match(toolbar,/专注模式/);
 assert.match(files,/onBackToShares=\{\(\)=>setTab\('local'\)\}/);
 assert.doesNotMatch(files,/className="file-share-remote-back"/);
 assert.match(browser,/window\.goOpenFileBrowserWindow\(device\|\|'',share\|\|'',path\)/);
});

test('folder ACL permission editor uses candidate-backed searchable multi-select below folder configuration',()=>{
 assert.match(files,/aria-label="选择已审批的访问设备"/);
 assert.match(files,/const \[grantPickerOpen,setGrantPickerOpen\]=useState\(false\)/);
 assert.match(files,/const \[permission,setPermission\]=useState\('read'\)/);
 assert.match(files,/candidates\.filter\(item=>!draft\.permissions\?\.\[item\.deviceId\]/);
 assert.match(files,/type="checkbox" checked=\{grantSelected\.includes\(item\.deviceId\)\}/);
 assert.match(files,/function addPermissions\(\)/);
 assert.match(files,/candidates\.some\(item=>item\.deviceId===id&&!draft\.permissions\?\.\[id\]\)/);
 assert.match(files,/Object\.fromEntries\(eligible\.map\(id=>\[id,permission\]\)\)/);
 assert.match(files,/goGetFileShareCandidates/);
 assert.match(fileCss,/\.file-share-grant-picker\{border:1px solid var\(--line\)/);
 assert.match(fileCss,/\.file-share-grant-list\{display:flex;flex-direction:column/);
});
