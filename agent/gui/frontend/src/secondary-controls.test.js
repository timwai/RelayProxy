import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const source=file=>readFileSync(new URL(file,import.meta.url),'utf8');
const app=source('./App.jsx'),css=source('./styles.css');
const slice=(start,end)=>{const a=app.indexOf(start),b=app.indexOf(end,a);assert.ok(a>=0&&b>a);return app.slice(a,b)};

test('messages open directly to search and cards with one compact type filter',()=>{
 const page=slice('function MessagesPage(','function MonitorPage(');
 assert.match(page,/className="filter-bar message-filter-compact"/);
 assert.match(page,/aria-label="消息类型"/);
 assert.match(page,/<option value="code">验证码<\/option>/);
 assert.doesNotMatch(page,/className="segment"/);
 assert.match(page,/className="message-list"/);
 assert.match(css,/\.message-type-filter\{display:flex;align-items:center/);
});

test('monitor shows connection list before collapsible aggregates and retains all filtering',()=>{
 const page=slice('function MonitorPage(','function DiagnosticsPage(');
 const first=page.indexOf('<Card title="连接列表"'),metrics=page.indexOf('monitor-stats-disclosure');
 assert.ok(first>=0&&metrics>first);
 assert.match(page,/role="region" aria-label="实时连接列表" tabIndex=\{0\}/);
 assert.match(page,/aria-label="连接状态"/);
 assert.match(page,/aria-label="连接协议"/);
 assert.match(page,/aria-label="连接动作"/);
 assert.match(page,/className="cards-4"><Metric label="活跃连接"/);
 assert.match(css,/\.monitor-stats-disclosure>summary/);
});

test('messages expose clear history directly beside filters instead of hidden page overflow',()=>{
 const page=slice('function MessagesPage(','function MonitorPage(');
 assert.match(page,/<PageHead title="消息中心"[^\n]*\/?>/);
 assert.doesNotMatch(page,/<PageHead[^\n]*actions=\{/);
 assert.match(page,/className="message-filter-actions" aria-label="消息操作"/);
 assert.match(page,/<Button onClick=\{refresh\}>刷新<\/Button>/);
 assert.match(page,/<Button danger disabled=\{!messages\.length\} onClick=\{clear\}>清空历史<\/Button>/);
 assert.match(css,/\.message-filter-actions\{display:flex;align-items:center/);
 assert.match(css,/@media\(max-width:760px\)\{[^\n]*?\.message-filter-actions\{margin-left:auto/);
});

test('clear history continues to call backend and only updates local count after success',()=>{
 assert.match(app,/confirm\('清空本设备的消息历史？'\)/);
 assert.match(app,/await call\('goClearMessages'\);messageIDs\.current\.clear\(\);setMessages\(\[\]\);setUnreadMessages\(0\)/);
 assert.match(app,/清空消息失败/);
});
