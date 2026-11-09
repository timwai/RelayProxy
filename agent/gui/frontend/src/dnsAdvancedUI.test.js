import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

test('DNS page exposes a complete, always-visible configuration workflow',()=>{
 const source=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
 const begin=source.indexOf('function DNSPage(');
 const end=source.indexOf('function RDPPage(',begin);
 assert.ok(begin>=0&&end>begin,'DNSPage must be present');
 const page=source.slice(begin,end);
 for(const feature of [
  'DNS 解析模式','按域名选择 DNS 出口','DNS 出口与域名关联',
  '自定义 DoH 上游','本地 DNS 例外','FakeIP 与 DoH 防护','DNS 出口诊断',
  'validateDNSSettings','moveRule(rules,i,i-1)','moveRule(rules,i,i+1)',
  'normalizeDNSRuleDraft','validateDNSRule','保存 DNS 配置'
 ])assert.ok(page.includes(feature),'missing DNS page feature '+feature);
 assert.match(page,/role="radio" aria-label="DNS 解析模式"/);
 assert.match(page,/aria-label="规则上移"/);
 assert.match(page,/aria-label="规则下移"/);
 assert.match(page,/call\('goProbeDNS'\)/);
 assert.match(page,/role="alert"/);
 assert.match(page,/disabled=\{!dirty\|\|saving\}/);
 assert.doesNotMatch(page,/advancedOpen/,'DNS settings must not hide primary controls behind an advanced toggle');
});

test('DNS upstream editor commits validated entries from an explicit dialog',()=>{
 const source=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
 const begin=source.indexOf('function DNSUpstreamsEditor(');
 const end=source.indexOf('function DNSPage(',begin);
 const editor=source.slice(begin,end);
 assert.match(editor,/validateDNSSettings/);
 assert.match(editor,/Bootstrap IP/);
 assert.match(editor,/onChange\(next\)/);
 assert.match(editor,/添加 DoH 上游/);
});
