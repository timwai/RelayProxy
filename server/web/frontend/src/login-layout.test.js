import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const css=readFileSync(new URL('./react.css',import.meta.url),'utf8');
const app=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
test('desktop login has a left feature panel and right sign-in section',()=>{
 assert.match(app,/className="login-shell"/);
 assert.match(app,/className="login-promo"/);
 assert.match(app,/className="login-card" aria-label="管理控制台登录"/);
 assert.ok(app.indexOf('className="login-promo"')<app.indexOf('className="login-card"'));
 assert.match(css,/#root \.login-shell\{[^}]*grid-template-columns/);
});
test('login preserves credential validation and full-width controls',()=>{
 assert.match(app,/function Login\(\{onLogin,error\}\)/);
 assert.match(app,/await onLogin\(username,password\)/);
 assert.match(css,/#root \.login-card \.btn\{[^}]*width:100%/);
 assert.match(css,/#root \.login-card \.field input\{[^}]*min-height/);
});
test('compact login page fits phones without horizontal overflow',()=>{
 assert.match(css,/@media\(max-width:760px\)/);
 assert.match(css,/#root \.login-shell\{grid-template-columns:1fr/);
 assert.match(css,/#root \.login-promo\{[^}]*padding/);
});
