import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {parseRuleKeywords,createMessageRule,changeRuleType,validateDraftMessageRules} from './message-rule-editor.js';
test('keywords keep separators during editing and commit on blur',()=>{
 assert.deepEqual(parseRuleKeywords('登录,短信，验证码\n验证码'),['登录','短信','验证码']);
});
test('new message rule requires match before saving',()=>{
 const rule=createMessageRule();
 assert.match(validateDraftMessageRules([rule]),/至少需要一个关键词/);
 rule.match.keywords=['登录'];
 assert.equal(validateDraftMessageRules([rule]),'');
});
test('switching type adds or removes verification extractor',()=>{
 const rule=createMessageRule();
 const verify=changeRuleType(rule,'verification_code');
 assert.equal(verify.verification.type,'auto');
 const msg=changeRuleType(verify,'message');
 assert.equal(msg.verification,undefined);
});

test('rule editor blur commits keyword text and newly created card is visible',()=>{
 const source=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
 assert.ok(source.includes('onBlur={onBlur}'),'Field must forward onBlur to native input');
 assert.ok(source.includes("onBlur={()=>um('keywords',parseRuleKeywords(keywordsText))}"),'keyword edits must be committed');
 assert.ok(source.includes('scrollIntoView({block:\'nearest\',behavior:\'smooth\'})'),'new rule must be scrolled into view');
 assert.ok(source.includes("messageRules:rules.map(({__uiKey,...value})=>value)"),'view keys must not be sent to API');
});

test('newly added rule renders immediately next to its add button, ahead of default fallback',()=>{
 const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
 const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');
 const begin=app.indexOf("{tab==='rules'&&");
 const end=app.indexOf("{tab==='routes'&&",begin);
 assert.ok(begin>=0&&end>begin,'rule tab must exist');
 const layout=app.slice(begin,end);
 const custom=layout.indexOf("rules.map((r,i)=>r.default?null:");
 const add=layout.indexOf('className="rule-add-area"');
 const fallback=layout.indexOf("rules.map((r,i)=>!r.default?null:");
 assert.ok(custom>=0&&custom<add&&add<fallback,
   'custom rules must precede the add button, with default fallback after it');
 assert.ok(app.includes("setRules(old=>[...old.filter(r=>!r.default),fresh,...old.filter(r=>r.default)])"),
   'draft order must keep the fallback rule last');
 assert.ok(layout.includes('aria-live="polite"'),'addition needs accessible feedback');
 assert.ok(app.includes('className="rule-add-area"'));
 assert.ok(app.includes('className="rule-add-feedback"'));
 assert.ok(app.includes('className="rule-new-indicator"'));
 assert.ok(css.includes('#root .rule-card-new{scroll-margin-top:16px'));
 assert.ok(css.includes('#root .rule-add-area{display:flex'));
});
