import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=file=>readFileSync(new URL(file,import.meta.url),'utf8');

test('collapsed Agent navigation remains labeled and exposes current page',()=>{
 const app=read('./App.jsx');
 assert.match(app,/title=\{x\[2\]\} aria-label=\{x\[2\]\} aria-current=\{\(x\[0\]==='routing'\?CONFIG_IDS\.has\(page\):page===x\[0\]\)\?'page':undefined\}/);
 assert.match(app,/title="设置" aria-label="设置" aria-current=/);
});

test('Agent shell and native controls respect viewport and chosen theme',()=>{
 const css=read('./styles.css');
 assert.match(css,/html\[data-theme="dark"\]\{color-scheme:dark\}/);
 assert.match(css,/@supports\(height:100dvh\)\{\.app-shell,\.workspace\{height:100dvh\}\}/);
 assert.match(css,/a:focus-visible,\.nav-item:focus-visible\{outline:2px solid var\(--brand\)/);
});

test('file rows can be selected with Space and activated with Enter',()=>{
 const jsx=read('./FileBrowserV4.jsx'),css=read('./fileBrowserV4.css');
 assert.match(jsx,/role="list" aria-label="共享目录文件"/);
 assert.match(jsx,/role="listitem" aria-label=/);
 assert.match(jsx,/if\(e.target!==e.currentTarget\)return;if\(e.key===' '\)\{e.preventDefault\(\);clickFile\(e,entry\)\}/);
 assert.match(jsx,/aria-label=\{'更多操作：'\+entry.name\}/);
 assert.match(css,/\.v4-entry:focus-visible\{outline:2px solid var\(--brand\)/);
});
