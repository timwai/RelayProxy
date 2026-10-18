import assert from 'node:assert/strict';
import test from 'node:test';
import {readFileSync} from 'node:fs';

const read=(file)=>readFileSync(new URL(file,import.meta.url),'utf8');
test('single click selects a folder; double click opens it',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 assert.match(jsx,/onClick={e=>clickFile\(e,entry\)}/);
 assert.match(jsx,/onDoubleClick={\(\)=>entry\.isDir\?navigate\(device,share,join\(path,entry\.name\)\):setDetails\(true\)}/);
 assert.match(jsx,/onClick={e=>e\.stopPropagation\(\)}/);
});
test('selection controls overlay file viewport without shifting the rows',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 const css=read('./fileBrowserV4.css');
 const selected=jsx.indexOf('selected.length>0&&<div className="v4-selection"');
 const scroll=jsx.indexOf("v4-file-scroll");
 const list=jsx.indexOf("v4-file-list",scroll);
 assert.ok(scroll>=0&&selected>scroll&&list>selected);
 assert.match(css,/\.v4-file-scroll>\.v4-selection\s*\{\s*position:absolute/);
});
test('standalone explorer uses same tab, avoiding popup blockers',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 assert.match(jsx,/href={focus\?returnToAppURL\(\):newWindowURL\(\)}/);
 assert.match(jsx,/hasBridge\('goOpenFileBrowserWindow'\)/);
 assert.match(jsx,/window\.goOpenFileBrowserWindow/);
 assert.match(jsx,/hasBridge\('goOpenFileBrowserWindow'\)&&/);
 assert.match(jsx,/<a className="btn v4-window-link"/);
 const win=read('../../file_browser_windows.go');
 assert.match(win,/window\.SetURL\("\/\?" \+ query\.Encode\(\)\)/);
 assert.doesNotMatch(jsx,/<a[^>]+target="_blank"/);
 assert.doesNotMatch(jsx,/window\.open\(/);
 const css=read('./fileBrowserV4.css');
 assert.match(css,/html\.file-browser-focus \.main>\.file-share-header\{display:none\}/);
});
test('transfer clear actions are available in both browser surfaces',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 const page=read('./FileSharingPage.jsx');
 assert.match(jsx,/onClearInactiveTransfers/);
 assert.match(jsx,/onClearTransfer/);
 assert.match(page,/goClearFileTransfer/);
 assert.match(page,/goClearInactiveFileTransfers/);
 assert.match(page,/item\.state!=='running'/);
});
test('every page has one compact title in the topbar and actions moved below',()=>{
 const app=read('./App.jsx');
 const page=read('./FileSharingPage.jsx');
 assert.match(app,/function PageHead\(\{actions\}\)/);
 assert.doesNotMatch(page,/<h1>文件共享<\/h1>/);
 assert.match(page,/className="file-share-list-actions"/);
 assert.doesNotMatch(page,/className="file-share-tabs"/);
});

// Moving a page into Settings must not invalidate its existing deep link.
test('settings consolidates device identity, proxy, routing, DNS, exit sharing and diagnostics',()=>{
 const app=read('./App.jsx');
 for(const id of ['devices','proxy','routing','dns','exitshare','diagnostics'])
  assert.match(app,new RegExp("'"+id+"'"));
 assert.match(app,/SETTINGS_IDS=new Set/);
 assert.match(app,/SETTINGS_SECTIONS\.map/);
 assert.match(app,/\.\.\.SETTINGS_IDS,'settings'/);
 assert.match(app,/SettingsNavigation page={page} onGoto={requestPage}/);
});
test('authorized folder browsing prewarms direct paths without embedding download tickets',()=>{
 const page=read('./FileSharingPage.jsx');
 const bridge=read('../../web.go');
 assert.match(page,/goPrewarmRemoteFileDirect/);
 assert.match(page,/remoteDevice,remoteShare/);
 assert.match(bridge,/\/api\/file-shares\/prewarm/);
 assert.match(bridge,/window\.goPrewarmRemoteFileDirect/);
});
