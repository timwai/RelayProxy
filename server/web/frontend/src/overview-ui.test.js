import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');
const icons=readFileSync(new URL('./icons.jsx',import.meta.url),'utf8');

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
test('overview has real-data metrics, visual topology, varied colors, and proper row gap',()=>{
 assert.match(app,/aria-label="RelayProxy 网络拓扑"/);
 assert.match(app,/className="network-visual"/);
 assert.match(app,/className="grid g4 overview-metrics"/);
 assert.match(app,/className="grid two-one page-section"/);
 assert.match(app,/className="overview-shortcuts"/);
 assert.match(app,/todayUpload/);
 assert.match(app,/todayDownload/);
 assert.match(css,/#root \.main-inner > \.overview-metrics \+ \.grid\.two-one\{margin-top:18px\}/);
 assert.match(css,/#root \.overview-metric \.metric-icon/);
});
