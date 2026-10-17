import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const read = file => readFileSync(new URL(file,import.meta.url),'utf8');

test('Server console preserves semantic h1 but does not repeat the visible page title',()=>{
 const jsx=read('./App.jsx'),css=read('./react.css');
 assert.match(jsx,/function Head\(\{title,desc,actions\}\)/);
 assert.match(jsx,/className="page-head page-actions-toolbar"/);
 assert.match(jsx,/<h1 className="sr-only">\{title\}<\/h1>/);
 assert.match(jsx,/className="page-context-help"/);
 assert.match(css,/#root \.page-head\.page-actions-toolbar>\.sr-only\{position:absolute/);
 assert.match(css,/#root \.page-head\.page-actions-toolbar>\.head-actions\{/);
});

test('Server modal can be fully used from keyboard',()=>{
 const jsx=read('./App.jsx');
 assert.match(jsx,/useDialogFocus\(!!dialog,ref,close\)/);
 assert.match(jsx,/<Action onClick={close} aria-label="关闭对话框">✕<\/Action>/);
 assert.match(jsx,/role="dialog" aria-modal="true" aria-label=\{dialog\.title\}/);
 assert.match(jsx,/e\.key==='Escape'/);
 assert.match(jsx,/e\.key!=='Tab'/);
 assert.match(jsx,/previous instanceof HTMLElement&&previous\.isConnected/);
});

test('Server header is compact but retains space for controls',()=>{
 const css=read('./prototype.css'),react=read('./react.css');
 assert.match(css,/\.topbar\{height:52px;min-height:52px/);
 assert.match(react,/#root \.page-head\.page-actions-toolbar\{display:flex;flex-direction:row/);
 assert.match(react,/@media\(max-width:760px\)\{/);
});
