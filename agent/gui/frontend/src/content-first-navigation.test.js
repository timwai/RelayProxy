import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const load=file=>readFileSync(new URL(file,import.meta.url),'utf8');
const app=load('./App.jsx'),sharing=load('./FileSharingPage.jsx'),browser=load('./FileBrowserV4.jsx');
const css=load('./styles.css'),sharingCSS=load('./fileSharing.css'),browserCSS=load('./fileBrowserV4.css');
const fragment=(source,from,to)=>{const a=source.indexOf(from),b=source.indexOf(to,a);assert.ok(a>=0&&b>a,`missing section: ${from}`);return source.slice(a,b)};
const routing=fragment(app,'function RoutingPage(','// The three presets intentionally');

test('routing page leads with a compact policy strip, then rules and subscriptions',()=>{
 const policy=routing.indexOf('<Card className="routing-policy">');
 const rules=routing.indexOf('<Card title="规则列表"');
 const subscriptions=routing.indexOf('<Card title="规则订阅"');
 const save=routing.indexOf('<SaveBar dirty={dirty} label="保存分流"');
 assert.ok(policy>=0&&rules>policy&&subscriptions>rules&&save>subscriptions);
 assert.match(routing,/role="radiogroup" aria-label="路由模式"/);
 assert.match(routing,/onClick=\{\(\)=>setv\('routing\.mode',v\)\}/);
 assert.match(routing,/aria-label="规则未命中时" disabled=\{mode!=='rule'\}/);
 // The default exit is switched on the 出口 page (immediate), not saved here.
 assert.doesNotMatch(routing,/label="默认代理出口"|setv\('defaultExitId'/);
 assert.match(routing,/onGoto\('exits'\)/);
 assert.match(routing,/onSave=\{\(\)=>save\(\{routing:routingPagePatch\(r\)\}\)\}/);
 assert.doesNotMatch(routing,/routing-strategy-disclosure/);
 assert.match(css,/\.routing-policy-row\{display:flex/);
});

test('local file sharing leads with folder cards, keeps service switches available below',()=>{
 const page=fragment(sharing,"  {tab==='local'&&<>","  {tab==='remote'&&<FileBrowserV4");
 const folder=page.indexOf('DIRECTORIES ·');
 const prefs=page.indexOf('<details className="file-share-advanced"');
 const metrics=page.indexOf('<div className="cards-3">');
 assert.ok(folder>=0&&prefs>folder&&metrics>prefs);
 assert.match(page,/shares\.map\(share=><div className="file-share-item"/);
 assert.match(page,/aria-label="启用文件共享"/);
 assert.match(page,/aria-label="允许文件专用 TCP 与 QUIC UPnP 映射"/);
 assert.match(page,/onClick=\{\(\)=>commit\(s=>\(\{\.\.\.s,enabled:!s\.enabled\}\)\)\}/);
 assert.match(sharingCSS,/\.file-share-advanced>summary/);
});

test('remote browser sidebar contains only the ACL-filtered tree for the selected device',()=>{
 const tree=fragment(browser,'  <aside className="v4-tree">','  <div className="v4-splitter v4-tree-split"');
 const head=fragment(browser,'   <div className="v4-tools v4-unified-pathbar">','   <div className="v4-toolbar" role="group"');
 assert.match(tree,/visibleShares\.map\(s=><div className="v4-tree-share"/);
 assert.match(tree,/renderTreeBranch\(device,s\.id,'\.'\)/);
 assert.doesNotMatch(tree,/peers\.map\(/);
 assert.doesNotMatch(tree,/v4-device-row/);
 assert.match(head,/aria-label="切换共享设备"/);
 assert.match(browser,/availablePeers\.map\(peer=><button type="button" key=\{peer\.deviceId\}/);
 assert.match(browser,/const visibleShares=selectedPeer\?shares\.filter\(item=>selectedPeer\.shares\.some\(allowed=>allowed\.id===item\.id\)\):\[\]/);
 assert.match(browserCSS,/\.v4-unified-pathbar \.v4-crumbs \.v4-peer-trigger/);
});

test('initial default selection respects authorization and does not jump to another device after revocation',()=>{
 assert.match(sharing,/const result=onlyVerifiedFilePeers\(raw\)/);
 assert.match(sharing,/const initialAutoSelected=useRef\(false\)/);
 assert.match(sharing,/if\(!initialAutoSelected\.current\)\{/);
 assert.match(sharing,/initialAutoSelected\.current=true;\s*void openRemoteDevice\(result\[0\]\.deviceId\)/);
 assert.match(sharing,/if\(!current\)\{[\s\S]*?selectedRemote\.current=\{device:'',share:''\}/);
 assert.match(browser,/onClick=\{\(\)=>navigate\(device,s\.id,'\.'\)\}/);
});
