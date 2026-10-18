import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const source=file=>readFileSync(new URL(file,import.meta.url),'utf8');
const app=source('./App.jsx'),css=source('./styles.css');
const slice=(start,end)=>{const a=app.indexOf(start),b=app.indexOf(end,a);assert.ok(a>=0&&b>a);return app.slice(a,b)};

test('messages open directly to search and cards with one compact type filter',()=>{
 const page=slice('function MessagesPage(','function MessageSettingsPage(');
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
