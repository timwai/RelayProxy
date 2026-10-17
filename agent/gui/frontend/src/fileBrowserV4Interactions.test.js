import assert from 'node:assert/strict';
import test from 'node:test';
import {readFileSync} from 'node:fs';

const read=(file)=>readFileSync(new URL(file,import.meta.url),'utf8');
test('folder activation uses navigation instead of selecting the row',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 assert.match(jsx,/onClick={e=>entry\.isDir\?navigate\(device,share,join\(path,entry\.name\)\):clickFile\(e,entry\)}/);
 assert.match(jsx,/onDoubleClick={e=>\{if\(!entry\.isDir\)setDetails\(true\);else e\.preventDefault\(\)\}}/);
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
test('web independent page uses a direct user activated hyperlink, native uses Wails',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 assert.match(jsx,/href={newWindowURL\(\)} target="_blank" rel="noopener noreferrer"/);
 assert.match(jsx,/window\.goOpenFileBrowserWindow/);
 assert.doesNotMatch(jsx,/window\.open\(/);
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
 assert.match(page,/className="file-share-header-actions"/);
});
