import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=read('./App.jsx'),css=read('./styles.css');
const a=app.indexOf('function DeviceSpeedLinkDetails('),b=app.indexOf('function SpeedModal(',a);
const view=app.slice(a,b);

test('every link check has icon, title, result and status in a single row',()=>{
 assert.match(view,/role="listitem" className=\{'device-speed-check is-'\+row\.state\}/);
 assert.match(view,/<strong className="device-speed-check-label">\{row\.label\}<\/strong>/);
 assert.match(view,/<span className="device-speed-check-detail">\{row\.detail\}<\/span>/);
 assert.match(view,/<span className="device-speed-check-state">\{stateLabel\[row\.state\]\}<\/span>/);
 assert.doesNotMatch(view,/device-speed-check-main/);
});

test('long names and errors stay accessible while truncating visually instead of wrapping',()=>{
 assert.match(view,/title=\{row\.label\+' · '\+row\.detail\}/);
 assert.match(view,/aria-label=\{row\.label\+'，'\+row\.detail\+'，'\+stateLabel\[row\.state\]\}/);
 assert.match(css,/\.device-speed-check\{display:grid;grid-template-columns:24px/);
 assert.match(css,/\.device-speed-check\{[^}]*white-space:nowrap\}/);
 assert.match(css,/\.device-speed-check-label\{[^}]*text-overflow:ellipsis;white-space:nowrap/);
 assert.match(css,/\.device-speed-check-detail\{[^}]*text-overflow:ellipsis;white-space:nowrap/);
 assert.match(css,/@media\(max-width:640px\)\{[\s\S]*?\.device-speed-check\{grid-template-columns:22px/);
});
