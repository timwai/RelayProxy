import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const read = file => readFileSync(new URL(file,import.meta.url),'utf8');

test('all Agent dialogs expose their title and a named close control',()=>{
 const app=read('./App.jsx');
 assert.match(app,/function Modal\(\{open,title,children,footer,onClose,wide\}\)/);
 assert.match(app,/role="dialog" aria-modal="true" aria-label=\{title\}/);
 assert.match(app,/className="close" aria-label="关闭对话框"/);
});

test('Agent modal traps keyboard focus and restores the trigger after closing',()=>{
 const app=read('./App.jsx');
 assert.match(app,/useDialogFocus\(open,ref,onClose\)/);
 assert.match(app,/e\.key==='Escape'/);
 assert.match(app,/e\.key!=='Tab'/);
 assert.match(app,/e\.shiftKey&&\(active===first\|\|!dialog\.contains\(active\)\)/);
 assert.match(app,/previous instanceof HTMLElement&&previous\.isConnected/);
});

test('compact type tokens keep secondary content readable',()=>{
 const css=read('./styles.css');
 assert.match(css,/--ui-body-size:14px/);
 assert.match(css,/--ui-caption-size:12px/);
 assert.match(css,/--ui-label-size:11px/);
 assert.match(css,/\.badge\{font-size:11px\}/);
});
