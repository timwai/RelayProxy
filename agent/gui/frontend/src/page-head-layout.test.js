import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
const read=p=>readFileSync(new URL(p,import.meta.url),'utf8');
const app=read('./App.jsx'),styles=read('./styles.css');
test('page actions are contextual rather than portaled into global topbar',()=>{
 assert.match(app,/function PageHead\(\{actions\}\)/);
 assert.match(app,/page-actions-contextual/);
 assert.match(app,/page-more-menu/);
 assert.doesNotMatch(app,/createPortal|id="agent-page-actions"/);
 assert.match(styles,/\.page-head\.page-actions-contextual\{/);
});
test('contextual menu works without action wrapping',()=>{
 assert.match(app,/aria-label="更多页面操作"/);
 assert.match(app,/aria-expanded=\{menuOpen\}/);
 assert.match(app,/e\.key==='Escape'/);
 assert.match(styles,/\.page-head\.page-actions-contextual\{[^}]*flex-wrap:nowrap/);
 assert.match(styles,/\.page-actions-contextual \.page-more-menu\{/);
});
test('file sharing keeps its content-first controls',()=>{
 const css=read('./fileSharing.css');
 assert.match(css,/\.file-share-list-actions\{display:flex/);
});
