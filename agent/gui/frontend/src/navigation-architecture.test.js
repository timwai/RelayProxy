import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const src=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./styles.css',import.meta.url),'utf8');
const extract=(from,to)=>{
 const first=src.indexOf(from),last=src.indexOf(to,first);
 assert.ok(first>=0&&last>first,`Missing section ${from}`);
 return src.slice(first,last);
};
const nav=extract('const NAV=[','const PAGE_NAMES=');
const exitsPage=extract('function ExitsPage(','function DeviceCenter(');
const deviceCenter=extract('function DeviceCenter(','function AccessPage(');
const access=extract('function AccessPage(','function ExitShare(');
const server=extract('function ServerPage(','function CustomExitManager(');
const shell=extract('export default function App(){','\n}');

test('one flat sidebar: every destination is a single click and nothing is nested',()=>{
 for(const group of ['工作台','网络','协作','观察'])assert.match(nav,new RegExp("group:'"+group+"'"));
 for(const id of ['overview','exits','routing','dns','access','server','exitshare','files','rdp','messages','monitor','diagnostics'])
  assert.match(nav,new RegExp("\\['"+id+"','"),`sidebar must list ${id}`);
 for(const legacy of ['devices','connection','proxy','gateway','rdp-settings','message-settings'])
  assert.doesNotMatch(nav,new RegExp("\\['"+legacy+"','"),`${legacy} was merged and must not be a sidebar item`);
 assert.doesNotMatch(src,/const CONFIG_SECTIONS|const SETTINGS_SECTIONS|function SectionNavigation|function ConfigNavigation|function SettingsNavigation/);
 assert.doesNotMatch(css,/\.settings-navigation|\.routing-strategy-disclosure|\.nav-category-crumb/);
 assert.match(shell,/aria-current=\{page===x\[0\]\?'page':undefined\}/);
 assert.match(shell,/<div className="crumb"><b>\{title\}<\/b>\{pageDesc&&<span className="crumb-desc">\{pageDesc\}<\/span>\}<\/div>/);
});

test('merged pages keep their old IDs reachable through aliases',()=>{
 assert.match(src,/const PAGE_ALIASES=\{devices:'server',connection:'server',proxy:'access',gateway:'access','rdp-settings':'rdp','message-settings':'messages'\}/);
 assert.match(src,/const resolvePage=id=>PAGE_IDS\.has\(id\)\?id:PAGE_ALIASES\[id\]\|\|''/);
 assert.match(src,/const requested=resolvePage\(new URLSearchParams\(window\.location\.search\)\.get\('page'\)\)/);
 assert.match(src,/resolvePage\(sessionStorage\.getItem\('relayproxy-react-page'\)\)\|\|'overview'/);
 assert.match(shell,/const requestPage=useCallback\(raw=>\{const next=resolvePage\(raw\)\|\|'overview';/);
 assert.match(shell,/window\.relayNavigate=requestPage/);
});

test('出口 page owns devices, groups and custom proxies with one default-exit control',()=>{
 assert.match(src,/const EXIT_TABS=\[\['devices','设备出口'\],\['groups','设备分组'\],\['custom','自定义代理'\]\]/);
 assert.match(exitsPage,/<DeviceCenter [^\n]*onSelectExit=\{dirty\?null:onSelectExit\}/);
 assert.match(exitsPage,/<DeviceGroups [^\n]*onSelectExit=\{dirty\?null:onSelectExit\}/);
 assert.match(exitsPage,/<CustomExitManager [^\n]*onSelectExit=\{dirty\?null:onSelectExit\}/);
 assert.match(exitsPage,/className="exits-current"/);
 assert.match(deviceCenter,/d\.proxy&&!isDefault&&<button type="button" className="device-quick-action"[^\n]*onClick=\{\(\)=>onSelectExit\(d\.id\)\}/);
 assert.match(deviceCenter,/d\.shares\.length>0&&onFiles&&<button[^\n]*onClick=\{\(\)=>onFiles\(d\.id\)\}/);
 assert.match(shell,/else if\(page==='exits'\)content=<ExitsPage [^\n]*selectedExit=\{selectedExit\} onSelectExit=\{select\}/);
 assert.doesNotMatch(shell,/<DeviceGroups |<CustomExitManager /);
});

test('接入 page carries inbound listeners, system takeover and the Linux LAN gateway in one SaveBar',()=>{
 assert.match(access,/<Card title="SOCKS5" eyebrow="LOCAL PROXY"/);
 assert.match(access,/<Card title="HTTP \/ HTTPS CONNECT" eyebrow="LOCAL PROXY"/);
 assert.match(access,/isMac&&<Card title="macOS 系统代理" eyebrow="SYSTEM TAKEOVER"/);
 assert.match(access,/transparentCaptureAvailable&&<Card title=\{captureTitle\} eyebrow="SYSTEM TAKEOVER"/);
 assert.match(access,/<summary>Network Service 维护<\/summary>/);
 assert.match(access,/isLinux&&<GatewaySection /);
 assert.match(access,/gateway:isLinux\?changedGatewayPatch\(config\.gateway,savedGateway\):undefined/);
 assert.match(access,/<SaveBar dirty=\{dirty\} label="保存接入设置"/);
 assert.doesNotMatch(src,/function ProxyPage\(|function GatewayPage\(/);
});

test('服务器 page merges connection policy with device identity and drops runtime path cards',()=>{
 assert.match(server,/<Card title="Relay Server" eyebrow="SERVER"/);
 assert.match(server,/<Card title="设备身份" eyebrow="DEVICE"/);
 assert.match(server,/<Card title="Direct \/ P2P" eyebrow="PATH POLICY"/);
 assert.match(server,/label="保存服务器与身份"/);
 assert.doesNotMatch(server,/代理出口可用路径|RDP TCP 实际路径/);
 assert.doesNotMatch(src,/function Devices\(|function Connection\(/);
});

test('top bar exposes a global exit switcher that is locked while drafts exist',()=>{
 assert.match(src,/function ExitSwitcher\(\{exits,customExits,selectedExit,connected,dirty,onSelect\}\)/);
 assert.match(shell,/<ExitSwitcher exits=\{groupExits\} customExits=\{config\.customExits\} selectedExit=\{selectedExit\} connected=\{!!status\.connected\} dirty=\{dirty\} onSelect=\{select\}\/>/);
 assert.match(src,/className=\{cx\('exit-switcher-trigger',open&&'open'\)\} aria-haspopup="menu" aria-expanded=\{open\}[^\n]*disabled=\{dirty\}/);
 assert.match(src,/const select=useCallback\(async id=>\{if\(dirty\)\{toast\('存在未保存的修改，请先保存或放弃后再切换出口','warn'\);return\}/);
 assert.match(css,/\.exit-switcher-menu\{position:absolute/);
});
