import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');
const chartCss=readFileSync(new URL('./overview-charts.css',import.meta.url),'utf8');
const jsx=readFileSync(new URL('./OverviewCharts.jsx',import.meta.url),'utf8');

test('live dashboard uses aligned equal-height cards and consistent section gaps',()=>{
 assert.match(jsx,/className="grid g4 rt-metrics"/);
 assert.match(jsx,/className="rt-two-col"/);
 assert.match(chartCss,/#root \.rt-metrics\{margin:0 0 22px\}/);
 assert.match(chartCss,/#root \.rt-two-col\{display:grid;grid-template-columns:repeat\(2,minmax\(0,1fr\)\);gap:16px/);
 assert.match(chartCss,/#root \.rt-two-col>\.rt-panel\{height:100%\}/);
 assert.doesNotMatch(css,/#root\s+\.page-section\s*\+\s*\.page-section\s*\{/);
});
test('chart panels stack on narrower screens without overflow',()=>{
 assert.match(chartCss,/@media\(max-width:1140px\)/);
 assert.match(chartCss, /#root \.rt-two-col\{grid-template-columns:1fr\}/);
 assert.match(chartCss, /@media\(max-width:640px\)/);
 assert.match(chartCss, /#root \.rt-metrics\{grid-template-columns:1fr\}/);
});
