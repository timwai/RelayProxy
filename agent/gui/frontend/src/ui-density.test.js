import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=relative=>readFileSync(new URL(relative,import.meta.url),'utf8');

test('remote file explorer fills the remaining workspace instead of using fixed subtractions',()=>{
 const css=read('./fileBrowserV4.css');
 assert.match(css,/\.main:has\(> \.file-browser-v4\)\{display:flex;flex-direction:column;overflow:hidden/);
 assert.match(css,/\.main:has\(> \.file-browser-v4\)>\.file-browser-v4\{flex:1 1 auto;min-height:0;height:auto/);
 assert.match(css,/html\.file-browser-focus \.main:has\(> \.file-browser-v4\)>\.file-browser-v4\{margin-top:0/);
});

test('transfer dock begins collapsed and expands when a transfer starts',()=>{
 const jsx=read('./FileBrowserV4.jsx');
 assert.match(jsx,/\[dockOpen,setDockOpen\]=useState\(false\)/);
 assert.match(jsx,/useEffect\(\(\)=>\{if\(runningTransfers\.length>0\)setDockOpen\(true\)\},\[runningTransfers\.length\]\)/);
 assert.match(jsx,/aria-expanded=\{dockOpen\}/);
});

test('monitor renders a bounded list and exposes a load-more action',()=>{
 const jsx=read('./App.jsx'),css=read('./styles.css');
 assert.match(jsx,/\[visibleLimit,setVisibleLimit\]=useState\(100\)/);
 assert.match(jsx,/const visibleRows=rows\.slice\(0,visibleLimit\)/);
 assert.match(jsx,/visibleRows\.map\(\(x,i\)=>/);
 assert.match(jsx,/rows\.length>visibleLimit&&<div className="monitor-load-more"/);
 assert.match(jsx,/role="region" aria-label="实时连接列表" tabIndex=\{0\}/);
 assert.match(css,/\.conn-table\{max-height:min\(64dvh,720px\)/);
 assert.match(css,/\.conn-table \.conn-head\{position:sticky;top:0/);
});
