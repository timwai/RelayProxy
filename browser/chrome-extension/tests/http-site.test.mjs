import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const src=name=>readFileSync(new URL('../src/'+name,import.meta.url),'utf8');
const manifest=JSON.parse(readFileSync(new URL('../manifest.json',import.meta.url),'utf8'));

test('optional host permissions permit both HTTP and HTTPS without changing Server TLS',()=>{
  assert.ok(manifest.optional_host_permissions.includes('http://*/*'));
  assert.ok(manifest.optional_host_permissions.includes('https://*/*'));
  const rules=src('rules.js');
  assert.match(rules,/url.protocol !== 'https:'/);
  assert.match(rules,/\['http:', 'https:'\].includes\(url.protocol\)/);
});

test('site picker, popup and permission confirmation support HTTP origins',()=>{
  const opts=src('options.js'),popup=src('popup.js');
  assert.match(opts,/\['http:','https:'\].includes\(u.protocol\)/);
  assert.match(popup,/\['http:', 'https:'\].includes\(uri.protocol\)/);
  assert.match(opts,/HTTP.*window.confirm|window.confirm\([\s\S]*?HTTP/);
  assert.match(opts,/chrome.permissions.request/);
  assert.match(src('options.html'),/选择 HTTP \/ HTTPS 站点/);
  assert.match(opts,/if\(rule.status==='active' && rule.valid && rule.policy.siteOrigin.startsWith\('https:\/\/'\)\)/);
});

test('HTTP snapshots cannot export secure cookies or HTTP data as legacy v1',()=>{
  const cookieCode=src('cookies-all.js');
  assert.match(cookieCode,/c.secure \|\| site \|\| c.sameSite==='no_restriction'/);
  const envelopeCode=src('envelope.js');
  assert.match(envelopeCode,/旧版 Cookie 规则仅支持 HTTPS/);
  assert.match(src('session-sync.js'),/policy.siteOrigin.startsWith\('http:\/\/'\)/);
});
