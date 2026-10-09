import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const jsx=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');

test('page title actions are a wrapping group, and standard actions use normal button size',()=>{
  assert.match(jsx, /function Head\([^]*?className="head-actions"/);
  assert.match(jsx, /className=\{'btn '\+tone\}/);
  assert.doesNotMatch(jsx, /className=\{'btn sm '\+tone\}/);
  assert.match(css, /#root \.head-actions\{[^}]*display:flex;[^}]*flex-wrap:wrap;[^}]*gap:var\(--control-gap-sm\)/);
});

test('tables, filters, card controls and editor buttons have explicit separation',()=>{
  for(const selector of [
    '#root .table-action{',
    '#root .inline-controls>.input{',
    '#root .card>.inline-controls+.table-wrap{',
    '#root .rule-card .form-actions{',
    '#root .channel-card>.form-actions{',
    '#root .config-card-head{'
  ])assert.ok(css.includes(selector),'missing '+selector);
  assert.match(css, /#root \.table-action\{[^}]*gap:8px;[^}]*flex-wrap:wrap/);
  assert.match(css, /#root \.card>\.inline-controls\+\.table-wrap\{margin-top:var\(--control-gap-md\)\}/);
  assert.doesNotMatch(css, /#root \.rule-card \.form-actions\{margin:0 0 8px\}/);
});

test('login, dialogs, bottom save and mobile controls keep intentional spacing',()=>{
  assert.match(css, /#root \.create-dialog-form>\.form-actions\{[^}]*margin-top:20px;[^}]*padding-top:14px/);
  assert.match(css, /#root \.dialog-header\{[^}]*gap:var\(--control-gap-md\)/);
  assert.match(css, /#root \.settings-save \.actions\{[^}]*gap:var\(--control-gap-sm\)/);
  assert.match(css, /#root \.login-card \.btn\{min-height:44px/);
  assert.match(css, /@media\(max-width:520px\)/);
  assert.match(css, /#root \.inline-controls>\.input,#root \.inline-controls>input\.input\{flex:1 1 100%/);
});
