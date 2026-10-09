import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');

test('pending approval and registered device authorization share structured device header',()=>{
 assert.match(app,/function DeviceAuthSummary\(/);
 assert.match(app,/function Approval\(/);
 assert.match(app,/function DeviceDetails\(/);
 assert.match(app,/className="device-auth-summary"/);
 assert.match(app,/className="device-auth-dialog device-auth-manage"/);
 assert.match(app,/open\('设备授权审批',<Approval[^\n]*>,true\)/);
});

test('permission cards are grouped with descriptions and accessible selection',()=>{
 assert.match(app,/visibleCapabilityGroups\(allowed\)/);
 assert.match(app,/className="device-auth-cap-grid"/);
 assert.match(app,/aria-label=\{cap.title\}/);
 assert.match(app,/toggleDeviceCapability\(value,cap.id,e.target.checked,allowed\)/);
 assert.match(css,/#root \.device-auth-cap\.is-selected\{/);
 assert.match(css,/#root \.device-auth-cap:focus-within\{/);
 assert.match(css,/#root \.device-auth-cap-group/);
});

test('form actions are separate from destructive device management actions',()=>{
 assert.match(app,/className="device-auth-footer-actions"/);
 assert.match(app,/className="device-auth-danger"/);
 assert.match(app,/role="group" aria-label="设备授权管理"/);
 assert.match(app,/aria-pressed=\{tab===key\}/);
 assert.match(app,/api\('\/enrollments\/'.*\/approve'/);
 assert.match(app,/api\('\/devices\/'.*\/capabilities'/);
 assert.match(css,/#root \.device-auth-danger\{/);
});

test('device authorization remains usable on mobile and does not add colored stripes',()=>{
 assert.match(css,/@media\(max-width:760px\)\{[\s\S]*?#root \.device-auth-cap-grid\{grid-template-columns:minmax\(0,1fr\)\}/);
 assert.match(css,/@media\(max-width:440px\)\{/);
 assert.doesNotMatch(css,/#root \.device-auth-cap\{[^}]*border-top:3px/);
});
