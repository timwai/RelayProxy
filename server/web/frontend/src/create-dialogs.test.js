import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');

test('all creation entry points open dialogs, not inline create cards',()=>{
 for(const name of ['IdentityCreate','IngressCreate','SecurityCreate','SystemGrantEditor']){
  assert.match(app,new RegExp('function '+name+'\\('));
 }
 for(const inline of ['创建身份','创建受控入口','新增 IP 策略']){
  assert.ok(!app.includes('<Panel title="'+inline+'"'),'inline card present: '+inline);
 }
 assert.match(app,/open\('创建身份',<IdentityCreate ctx=\{ctx\}\/>\)/);
 assert.match(app,/ctx\.open\('创建 RDP 公网入口',<IngressCreate/);
 assert.match(app,/ctx\.open\('新增 IP 安全策略',<SecurityCreate/);
 assert.match(app,/ctx\.open\(g\?'编辑 Server Exit 身份授权':'授权 Server Exit 身份',<SystemGrantEditor/);
 assert.match(app,/open\('新建推送渠道',<ChannelEditor/);
});
test('create forms use existing API endpoints and close after success',()=>{
 const endpoints=['/identities','/rdp/ingress','/rdp/security/bans','/system-identity-grants'];
 for(const endpoint of endpoints)assert.ok(app.includes("api('"+endpoint+"'"),endpoint);
 const dialogs=['IdentityCreate','IngressCreate','SecurityCreate','SystemGrantEditor'];
 for(const name of dialogs){
  const from=app.indexOf('function '+name+'('),until=app.indexOf('\nfunction ',from+10);
  assert.ok(from>=0,name+' not found');
  const section=app.slice(from,until>0?until:undefined);
  assert.match(section,/ctx\.close\(\)/,name);
  assert.match(section,/type="submit"/,name);
 }
});
