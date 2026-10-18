import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const get=p=>readFileSync(new URL(p,import.meta.url),'utf8');
const app=get('./App.jsx'),files=get('./FileSharingPage.jsx'),browser=get('./FileBrowserV4.jsx'),css=get('./fileSharing.css');

test('there is one persistent file-workspace owner across Agent routes',()=>{
 assert.match(app,/else if\(page==='files'\)content=null/);
 assert.match(app,/<FileSharingPage toast=\{toast\} active=\{page==='files'\} navigationTarget=\{fileTarget\} transferOpen=\{transferPanelOpen\}/);
 assert.match(app,/<div id="agent-file-transfer-portal"\/>/);
 assert.doesNotMatch(app,/<FileSharingPage key=/);
 assert.match(files,/saveHandles=useRef\(new Map\(\)\)/);
 assert.match(files,/const timer=setInterval\(\(\)=>void refreshTransfers\(\),1200\)/);
 assert.match(files,/if\(entry==='browser'\)/);
 assert.match(files,/response\.body\.pipeTo\(writable\)/);
 assert.match(files,/completedUploads\.current\.add\(task\.id\)/);
});

test('local list is the landing view without tab row, while remote browsing is a dedicated action',()=>{
 assert.doesNotMatch(files,/<div className="file-share-tabs"/);
 assert.doesNotMatch(files,/tab==='transfers'/);
 assert.match(files,/tab==='local'&&<>/);
 assert.match(files,/file-share-list-head/);
 assert.match(files,/浏览其他设备文件/);
 assert.doesNotMatch(files,/className="file-share-remote-back"/);
 assert.match(files,/onBackToShares=\{\(\)=>setTab\('local'\)\}/);
 assert.match(browser,/返回我的共享/);
 assert.match(files,/setTab\('local'\)/);
 assert.match(files,/onlyVerifiedFilePeers\(raw\)/);
 assert.match(files,/const requested=navigationTarget\?\.device/);
 assert.match(files,/initialDeviceRef\.current=requested/);
 assert.match(files,/initialAutoSelected\.current=true/);
 assert.match(browser,/aria-label="切换共享设备"/);
 assert.match(css,/\.file-sharing-host\[hidden\]\{display:none!important\}/);
});

test('file transfers are global and closing the panel cannot destroy running tasks',()=>{
 assert.match(app,/className="global-transfer-trigger"/);
 assert.match(app,/aria-expanded=\{transferPanelOpen\}/);
 assert.match(files,/createPortal\(/);
 assert.match(files,/className="global-transfer-panel" role="dialog" aria-modal="true"/);
 assert.match(files,/onTransferOpenChange\?\.\(false\)/);
 assert.match(files,/onOpenTransfers=\{\(\)=>onTransferOpenChange\?\.\(true\)\}/);
 assert.match(files,/onTransferSummary\?\.\(transfers\.filter\(t=>t\.state==='running'\)\.length,transfers\.length\)/);
 assert.match(files,/useDialogFocus\(transferOpen,drawerRef/);
 assert.match(files,/goCancelFileDownload/);
 assert.match(files,/goClearInactiveFileTransfers/);
 assert.match(files,/goResumeFileDownload/);
});
