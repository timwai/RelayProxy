import test from 'node:test';
import assert from 'node:assert/strict';
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
