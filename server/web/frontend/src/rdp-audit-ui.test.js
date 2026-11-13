import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {
  formatAuditIngress,
  auditSortOptions,
  buildAuditGroupsQuery,
  buildSecurityBansQuery,
  formatBanKind,
  formatBanScope
} from './rdp-audit-ui.js';

const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');

test('RDP audit shows port rather than internal rdping ID',()=>{
 assert.equal(formatAuditIngress('rdping_123',[{id:'rdping_123',listenPort:33089}]),'端口 33089');
 assert.match(formatAuditIngress('rdping_old',[]),/历史入口/);
 assert.equal(formatAuditIngress('',[]),'未记录入口');
});

test('RDP audit sort query supports connection count and direction',()=>{
 assert.ok(auditSortOptions.some(([k])=>k==='connections:desc'));
 assert.equal(
  buildAuditGroupsQuery({page:2,pageSize:50,search:'198.51.100',sort:'connections:desc'}),
  'page=2&pageSize=50&search=198.51.100&sort=connections&order=desc'
 );
});

test('active IP policy query builder and formatters support pagination and filters',()=>{
 assert.equal(
  buildSecurityBansQuery({page:3,pageSize:20,search:'203.0.113',kind:'auto',scope:'global',expiry:'permanent'}),
  'page=3&pageSize=20&search=203.0.113&kind=auto&scope=global&expiry=permanent'
 );
 assert.equal(formatBanKind({kind:'manual'}),'手动封禁');
 assert.equal(formatBanKind({kind:'allow'}),'白名单');
 assert.equal(formatBanKind({kind:'auto',reason:'GEO_NON_CN:US'}),'GeoIP 封禁');
 assert.equal(formatBanKind({kind:'auto',reason:'persistent_scan'}),'自动封禁');
 assert.equal(formatBanScope('',[]),'全局 RDP');
});

test('console exposes one-click permanent ban, auto-clear logs, sort and ban pagination',()=>{
 assert.match(app,/aria-label="排序方式"/);
 assert.match(app,/aria-label="活跃 IP 策略筛选"/);
 assert.match(app,/封禁后自动清除记录/);
 assert.match(app,/永久封禁/);
 assert.match(app,/\/permanent/);
});
