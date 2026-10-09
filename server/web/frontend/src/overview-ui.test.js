import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');
const icons=readFileSync(new URL('./icons.jsx',import.meta.url),'utf8');
const dashboard=readFileSync(new URL('./OverviewCharts.jsx',import.meta.url),'utf8');
const chartCss=readFileSync(new URL('./overview-charts.css',import.meta.url),'utf8');

test('brand uses official existing web asset on login and navigation',()=>{
 assert.match(app,/function BrandMark\(/);
 assert.match(app,/src="\/img\/logo\.png"/);
 assert.match(app,/<BrandMark login\/>/);
 assert.match(app,/<BrandMark\/>/);
});
test('navigation uses GUI stroke icons instead of Unicode placeholders',()=>{
 assert.match(app,/import \{Icon\} from '\.\/icons\.jsx'/);
 assert.match(app,/name=\{navIcons\[p\]\}/);
 assert.match(icons,/viewBox="0 0 24 24"/);
 assert.match(icons,/stroke="currentColor"/);
 assert.ok(css.includes('#root .sidebar .nav-item>span:nth-child(2){display:none}'));
});
test('overview replaces decorative topology with live charts',()=>{
 assert.match(app,/function Overview\(\{ctx\}\)\{return <OverviewRealtime ctx=\{ctx\}\/>/);
 assert.doesNotMatch(app,/className="network-visual"/);
 assert.doesNotMatch(app,/aria-label="RelayProxy 网络拓扑"/);
 for(const keyword of ['实时上下行速率','连接与设备趋势','当前路径分布','出口实时负载','今日累计上传','最近设备']){
   assert.ok(dashboard.includes(keyword),keyword);
 }
 assert.match(dashboard,/className="grid g4 rt-metrics"/);
 assert.match(chartCss,/#root \.rt-two-col\{display:grid/);
});
test('overview polling uses real endpoints and bounded history',()=>{
 assert.match(dashboard,/api\('\/dashboard',options\)/);
 assert.match(dashboard,/api\('\/sessions\/active',options\)/);
 assert.match(dashboard,/api\('\/p2p\/sessions',options\)/);
 assert.match(dashboard,/setInterval\(poll,SAMPLE_INTERVAL_MS\)/);
 assert.match(dashboard,/visibilitychange/);
 assert.match(dashboard,/appendSample\(old,packet\.sample\)/);
 assert.doesNotMatch(dashboard,/Math\.random\(/);
});

test('cards use neutral borders without decorative colored edge stripes',()=>{
 assert.doesNotMatch(css,/\.card-title-area::before/);
 assert.doesNotMatch(css,/#root \.channel-card\{border-top:3px/);
 assert.doesNotMatch(css,/#root \.exit-card\{border-top:3px/);
 assert.doesNotMatch(css,/#root \.rule-card\{border-left:3px/);
 assert.match(css,/#root \.card\{border-color:/);
});
