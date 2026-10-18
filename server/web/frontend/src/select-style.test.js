import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const styles=readFileSync(new URL('./react.css',import.meta.url),'utf8');
const base=readFileSync(new URL('./prototype.css',import.meta.url),'utf8');

test('Server Settings, audit filters and approval controls use 44px selects',()=>{
 assert.match(styles,/#root select:not\(\[multiple\]\):not\(\[size\]\)\{[\s\S]*?min-height:44px;[\s\S]*?height:44px;[\s\S]*?font-size:14px/);
 assert.match(styles,/@media\(max-width:640px\)\{#root select:not\(\[multiple\]\):not\(\[size\]\)\{min-height:48px;height:48px/);
 assert.match(styles,/#root \.browser-sync-refined \.browser-sync-approve-controls select\{width:100%;min-height:38px\}/);
 assert.match(base,/\.input,select,textarea\{border:1px solid var\(--line2\)/);
});

test('Server select styling is theme-aware and preserves native multiple listboxes',()=>{
 assert.match(styles,/#root\{--select-chevron:url\(/);
 assert.match(styles,/html\[data-theme="dark"\] #root\{--select-chevron:url\(/);
 assert.match(styles,/#root select:not\(\[multiple\]\):not\(\[size\]\):hover:not\(:disabled\)/);
 assert.match(styles,/#root select:not\(\[multiple\]\):not\(\[size\]\):focus-visible\{outline:2px solid var\(--brand\)/);
 assert.match(styles,/#root select:not\(\[multiple\]\):not\(\[size\]\) option\{background:var\(--surface2\)/);
});
