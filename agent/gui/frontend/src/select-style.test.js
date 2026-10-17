import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const styles=readFileSync(new URL('./styles.css',import.meta.url),'utf8');
const browser=readFileSync(new URL('./fileBrowserV4.css',import.meta.url),'utf8');
const fileShare=readFileSync(new URL('./fileSharing.css',import.meta.url),'utf8');

test('Agent single-selects use a unified 44px target and 48px mobile height',()=>{
 assert.match(styles,/select:not\(\[multiple\]\):not\(\[size\]\)\{[\s\S]*?min-height:44px;[\s\S]*?height:44px;[\s\S]*?font-size:14px/);
 assert.match(styles,/@media\(max-width:640px\)\{\s*select:not\(\[multiple\]\):not\(\[size\]\)\{min-height:48px;height:48px/);
 assert.match(styles,/select:not\(\[multiple\]\):not\(\[size\]\) option\{background:var\(--surface2\);color:var\(--text\)\}/);
});

test('Agent select chevrons and interactive states adapt to light/dark appearance',()=>{
 assert.match(styles,/:root\{--select-chevron:url\(/);
 assert.match(styles,/html\[data-theme="dark"\]\{--select-chevron:url\(/);
 assert.match(styles,/background-image:var\(--select-chevron\)/);
 assert.match(styles,/appearance:none;/);
 assert.match(styles,/select:not\(\[multiple\]\):not\(\[size\]\):focus-visible\{outline:2px solid var\(--brand\)/);
 assert.match(styles,/select:not\(\[multiple\]\):not\(\[size\]\):disabled\{cursor:not-allowed/);
});

test('file explorer sorting and sharing permission dropdowns inherit universal select sizing',()=>{
 assert.match(browser,/\.v4-toolbar>select\{/);
 assert.match(fileShare,/\.file-share-permission select\.control\{/);
 assert.match(styles,/select:not\(\[multiple\]\):not\(\[size\]\)\{/);
});
