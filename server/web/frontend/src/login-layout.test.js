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
 assert.ok(css.includes('grid-template-columns:1fr;overflow:visible'));
 assert.match(css,/#root \.login-promo\{[^}]*padding/);
});
test('full-bleed login fills the viewport rather than a centered card',()=>{
 assert.match(css,/#root \.login-screen\{[^}]*width:100vw;height:100dvh/);
 assert.match(css,/#root \.login-shell\{[^}]*grid-template-columns:minmax\(0,55fr\) minmax\(0,45fr\)/);
 assert.match(css,/#root \.login-shell\{[^}]*border-radius:0;box-shadow:none/);
 assert.match(css,/#root \.login-card > form/);
});
test('brand mark is actual SVG vector art, reused in login and navigation',()=>{
 assert.match(app,/src="\/img\/logo\.svg"/);
 assert.doesNotMatch(app,/src="\/img\/logo\.png"/);
 const mark=readFileSync(new URL('../../img/logo.svg',import.meta.url),'utf8');
 assert.match(mark,/<svg[^>]*viewBox="0 0 64 64"/);
 assert.match(mark,/<path[^>]*stroke=/);
 assert.doesNotMatch(mark,/<image\b|data:image\//);
});

test('SVG brand mark has no background frame or nested CSS frame',()=>{
 const svg=readFileSync(new URL('../../img/logo.svg',import.meta.url),'utf8');
 assert.doesNotMatch(svg,/<rect\b/);
 assert.match(svg,/<path\b[^>]*stroke="url\(#rp-mark-gradient\)"/);
 const brand=css.match(/#root \.brand-image\{[^}]+\}/)?.[0]||'';
 assert.match(brand,/background:transparent/);
 assert.match(brand,/border:0/);
 assert.match(brand,/box-shadow:none/);
 assert.match(brand,/padding:0/);
 const promo=css.match(/#root \.login-promo \.brand-image\{[^}]+\}/)?.[0]||'';
 assert.doesNotMatch(promo,/rgba\(255,255,255,.95\)/);
 assert.match(promo,/background:transparent/);
});
