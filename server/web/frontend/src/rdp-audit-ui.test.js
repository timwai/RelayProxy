import test from 'node:test';
import assert from 'node:assert/strict';
import {formatAuditIngress} from './rdp-audit-ui.js';
test('RDP audit shows port rather than internal rdping ID',()=>{
 assert.equal(formatAuditIngress('rdping_123',[{id:'rdping_123',listenPort:33089}]),'端口 33089');
 assert.match(formatAuditIngress('rdping_old',[]),/历史入口/);
 assert.equal(formatAuditIngress('',[]),'未记录入口');
});
