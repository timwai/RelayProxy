import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=read('./App.jsx'),files=read('./FileSharingPage.jsx'),css=read('./styles.css');

test('device cards expose capabilities and an explicit, guarded set-default action',()=>{
 assert.match(app,/function DeviceCenter\(\{status,exits,targets,filePeers,config,selectedExit,onSelectExit,onSpeed,onConnectRDP,onFiles\}\)/);
 const view=app.slice(app.indexOf('function DeviceCenter('),app.indexOf('function AccessPage('));
 assert.match(view,/onConnectRDP\(d.id\)/);
 assert.match(view,/onSpeed\(d.id\)/);
 assert.match(view,/setDetail\(d.id\)/);
 // Switching the default exit goes through the App-level select callback only.
 assert.doesNotMatch(view,/goSelectExit|save\(/);
 assert.match(view,/isDefault=selectedExit===d\.id/);
 assert.match(view,/isDefault&&<Badge tone="blue">默认出口<\/Badge>/);
 assert.match(view,/d\.proxy&&!isDefault&&<button type="button" className="device-quick-action" disabled=\{!onSelectExit\|\|!online\|\|d\.proxy\.online===false\}/);
 assert.match(view,/d\.shares\.length>0&&<Badge/);
 assert.match(view,/selected\.shares\.map/);
 assert.match(view,/d\.rdp&&<button/);
 assert.match(view,/d\.proxy&&<button/);
});

test('routing page saves policy and rules only; default exit lives on the 出口 page',()=>{
 const rules=app.slice(app.indexOf('function RoutingPage('),app.indexOf('function DNSUpstreamsEditor('));
 assert.doesNotMatch(rules,/label="默认代理出口"|setv\('defaultExitId'/);
 assert.match(rules,/aria-label="规则未命中时"/);
 assert.match(rules,/save\(\{routing:routingPagePatch\(r\)\}\)/);
 assert.match(rules,/跟随默认出口/);
});

test('file shortcut uses only ACL-verified shares and never unapproved host entries',()=>{
 assert.match(app,/onlyVerifiedFilePeers\(await callJSON\('goGetFileSharePeers',\[\]\)\)/);
 assert.match(app,/navigationTarget=\{fileTarget\}/);
 assert.match(files,/export default function FileSharingPage\(\{toast,initialDevice='',active=true,navigationTarget=null/);
 assert.match(files,/const peer=result\.find\(x=>x\.deviceId===initialPeerID\)/);
 assert.match(files,/if\(peer\.shares\.length===1\)\{setRemoteShares\(peer\.shares\);void openRemoteDir/);
 assert.match(files,/else void openRemoteDevice\(peer\.deviceId\)/);
 assert.match(files,/onlyVerifiedFilePeers\(raw\)/);
});

test('device card controls are responsive, focusable and labelled',()=>{
 assert.match(app,/className="device-center-card-name" type="button" onClick=\{\(\)=>setDetail\(d.id\)\}/);
 assert.match(app,/aria-label=\{'远程连接 '\+name\}/);
 assert.match(app,/aria-label=\{'测速 '\+name\}/);
 assert.match(css,/\.device-center-grid\{display:grid;grid-template-columns:repeat\(auto-fill/);
 assert.match(css,/\.device-quick-action:focus-visible/);
 assert.match(css,/@media\(max-width:640px\)\{[\s\S]*?\.device-center-grid\{grid-template-columns:minmax\(0,1fr\)/);
});
