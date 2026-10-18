import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const src=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const extract=(from,to)=>{
 const first=src.indexOf(from),last=src.indexOf(to,first);
 assert.ok(first>=0&&last>first,`Missing section ${from}`);
 return src.slice(first,last);
};
const nav=extract('const NAV=[','const CONFIG_SECTIONS=[');
const config=extract('const CONFIG_SECTIONS=[','const SETTINGS_SECTIONS=[');
const settings=extract('const SETTINGS_SECTIONS=[','const CONFIG_IDS=');
const ex=extract('function Exits(','function ProxyPage(');
const proxy=extract('function ProxyPage(','function ExitShare(');

test('quick access stays operational; frequently edited network rules default to Configuration',()=>{
 assert.match(nav,/\['routing','split','网络配置'\]/);
 assert.match(nav,/\['overview','dashboard'/);
 assert.match(nav,/\['exits','globe'/);
 assert.match(nav,/\['rdp','monitor'/);
 assert.match(nav,/\['messages','bell'/);
 assert.match(nav,/\['diagnostics','terminal'/);
 assert.doesNotMatch(nav,/\['connection','route'/);
 assert.match(config,/\['routing','split','分流规则'/);
 for(const key of ['dns','proxy','connection','exitshare']){
  assert.match(config,new RegExp("\\['"+key+"'"));
  assert.doesNotMatch(settings,new RegExp("\\['"+key+"'"));
 }
});

test('Settings only includes low-frequency preferences and device identity',()=>{
 for(const key of ['devices','rdp-settings','message-settings']){
  assert.match(settings,new RegExp("\\['"+key+"'"));
  assert.doesNotMatch(config,new RegExp("\\['"+key+"'"));
 }
 assert.match(src,/function SettingsNavigation\(\{page,onGoto\}\)/);
 assert.match(src,/function ConfigNavigation\(\{page,onGoto\}\)/);
 assert.match(src,/aria-label=\{configuration\?'网络配置分类':'设置分类'\}/);
});

test('legacy page IDs remain reachable and each group keeps active state',()=>{
 assert.match(src,/const PAGE_IDS=new Set\(\[\.\.\.NAV\.flatMap\(group=>group.items.map\(item=>item\[0\]\)\),\.\.\.CONFIG_IDS,\.\.\.SETTINGS_IDS,'settings'\]\)/);
 assert.match(src,/if \(PAGE_IDS.has\(requested\)\) return requested/);
 assert.match(src,/CONFIG_IDS\.has\(page\)&&<ConfigNavigation/);
 assert.match(src,/SETTINGS_IDS\.has\(page\)&&<SettingsNavigation/);
 assert.match(src,/x\[0\]==='routing'\?CONFIG_IDS\.has\(page\):page===x\[0\]/);
});

test('exit selection is operational and custom exit CRUD lives in Configuration',()=>{
 assert.match(ex,/customs\.map\(e=><button/);
 assert.match(ex,/onClick=\{\(\)=>onSelect\(e.id\)\}/);
 assert.match(ex,/管理自定义出口/);
 assert.doesNotMatch(ex,/<CustomExitManager/);
 assert.match(proxy,/<CustomExitManager config=\{config\} save=\{save\} toast=\{toast\} dirty=\{dirty\}\/>/);
 assert.match(src,/if\(dirty\)\{toast\('请先保存代理配置或放弃未保存的修改/);
 assert.match(src,/exitName\(\[\.\.\.exits,\.\.\.arr\(config\.customExits\)\]/);
});
