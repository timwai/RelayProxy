import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const css = readFileSync(new URL('./react.css', import.meta.url), 'utf8');
const app = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

test('login card is wider than the legacy compact 430px design', () => {
  assert.match(css, /#root \.login-card\{width:min\(620px,100%\)/);
  assert.doesNotMatch(css, /#root \.login-card\{width:min\(430px,100%\)/);
  assert.match(css, /#root \.login-screen\{[^}]*display:flex;align-items:center;justify-content:center/);
});

test('login form fills available width and has touch-friendly controls', () => {
  assert.match(app, /function Login\(\{onLogin,error\}\)/);
  assert.match(app, /className="login-screen"><div className="login-card"/);
  assert.match(css, /#root \.login-card \.field input\{[^}]*min-height:46px/);
  assert.match(css, /#root \.login-card \.btn\{[^}]*height:44px/);
});

test('login layout remains within the viewport on mobile', () => {
  assert.match(css, /@media\(max-width:640px\)\{[\s\S]*?#root \.login-card\{width:100%;padding:30px 26px/);
  assert.match(css, /@media\(max-width:400px\)\{[\s\S]*?#root \.login-card\{padding:24px 20px/);
});
