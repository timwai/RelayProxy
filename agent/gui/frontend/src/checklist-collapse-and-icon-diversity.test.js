import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {fileIconKind,fileIconLabel} from './fileIconKind.js';

const source=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=source('./App.jsx');
const speeds=app.slice(app.indexOf('function DeviceSpeedLinkDetails('),app.indexOf('function SpeedModal('));
const glyph=source('./FileGlyph.jsx');
const css=source('./fileBrowserV4.css');
const styles=source('./styles.css');

test('speed checklist is collapsible with a visible live summary and accessible control',()=>{
 assert.match(speeds,/const \[checkExpanded,setCheckExpanded\]=useState\(true\)/);
 assert.match(speeds,/className="device-speed-check-toggle"/);
 assert.match(speeds,/aria-expanded=\{checkExpanded\}/);
 assert.match(speeds,/aria-controls="device-speed-check-content"/);
 assert.match(speeds,/onClick=\{\(\)=>setCheckExpanded\(open=>!open\)\}/);
 assert.match(speeds,/className="device-speed-check-summary" role="status" aria-live="polite"/);
 assert.match(speeds,/\{checkExpanded&&<div id="device-speed-check-content"/);
 assert.match(speeds,/className="device-speed-check-list" role="list"/);
 assert.match(speeds,/const summary=phase==='running'\?'测速中'/);
 assert.match(styles,/\.device-speed-check-toggle\{width:100%;border:0/);
});

test('file glyph illustrations cover specialized developer, creative, data and reading files',()=>{
 const entries={
  'main.ts':'typescript','index.js':'javascript','cli.py':'python','server.go':'golang','bridge.rs':'rust',
  'readme.mdx':'markdown','data.json':'json','component.html':'web','view.css':'stylesheet',
  'plot.ipynb':'notebook','schema.sql':'database','dataset.parquet':'data',
  'logo.svg':'vector','mockup.sketch':'design','house.glb':'model3d',
  'captions.srt':'subtitle','driver.iso':'disk','manual.epub':'ebook','identity.p12':'certificate',
 };
 for(const [name,kind] of Object.entries(entries)){
  assert.equal(fileIconKind(name),kind,name);
  assert.match(glyph,new RegExp("case '"+kind+"':return"),name);
  assert.ok(css.includes('.v4-glyph-'+kind+'{--fg-color:'),name);
  assert.ok(fileIconLabel(name),name);
 }
 assert.match(glyph,/function LetterMark\(\{label\}\)/);
 assert.match(glyph,/viewBox="0 0 40 40"/);
 assert.doesNotMatch(glyph,/<img|https?:\/\//);
 assert.equal(fileIconKind('nested.svg',true),'folder');
});
