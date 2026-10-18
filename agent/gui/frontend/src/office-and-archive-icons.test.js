import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {fileIconKind,fileIconLabel} from './fileIconKind.js';

const load=file=>readFileSync(new URL(file,import.meta.url),'utf8');
const glyph=load('./FileGlyph.jsx'),css=load('./fileBrowserV4.css'),explorer=load('./FileBrowserV4.jsx');

test('Microsoft Word, Excel and PowerPoint each have a distinct SVG and color',()=>{
 const examples=[
  ['合同.docx','word','Word 文档'],['预算.xlsm','excel','Excel 工作簿'],
  ['年度汇报.pptx','powerpoint','PowerPoint 演示文稿']
 ];
 for(const [name,kind,label] of examples){
  assert.equal(fileIconKind(name),kind);
  assert.equal(fileIconLabel(name),label);
  assert.match(glyph,new RegExp("case '"+kind+"':return <OfficeMark"));
  assert.match(css,new RegExp('\\.v4-glyph-'+kind+'\\{--fg-color:'));
 }
 assert.match(glyph,/function OfficeMark\(\{letter,type\}\)/);
 assert.match(glyph,/type==='word'/);
 assert.match(glyph,/type==='excel'/);
 assert.match(glyph,/type==='powerpoint'/);
});

test('compressed archives are distinguishable and TAR multi-extensions are preserved',()=>{
 const examples=[
  ['backup.zip','zip'],['x.zipx','zip'],['backup.rar','rar'],
  ['media.7z','sevenzip'],['db.tar','tar'],['src.tar.gz','tar'],
  ['images.tar.xz','tar'],['images.tar.zst','tar'],
  ['backup.tgz','tar'],['log.gz','compressed'],
  ['backup.bz2','compressed'],['resources.jar','archive']
 ];
 for(const [name,kind] of examples){
  assert.equal(fileIconKind(name),kind,name);
  assert.ok(fileIconLabel(name).length>0,name);
  assert.match(css,new RegExp('\\.v4-glyph-'+kind+'\\{--fg-color:'),name);
 }
 for(const [kind,mark] of [['zip','ZIP'],['rar','RAR'],['sevenzip','7Z'],['tar','TAR'],['compressed','GZ']]){
  assert.ok(glyph.includes("case '"+kind+"':return <ArchiveMark label=\""+mark+"\""),kind);
 }
 assert.match(glyph,/function ArchiveMark\(\{label\}\)/);
});

test('platform-specific installers have vector symbols, with fallback on unknown extensions',()=>{
 for(const [name,kind] of [['setup.exe','windows'],['app.msix','windows'],['release.apk','android'],
  ['client.ipa','apple'],['module.deb','package'],['unknown.zzunknown','file']]){
  assert.equal(fileIconKind(name),kind);
  if(kind!=='file'){
   assert.ok(glyph.includes("case '"+kind+"':return"),kind);
   assert.match(css,new RegExp('\\.v4-glyph-'+kind+'\\{--fg-color:'));
  }
 }
 assert.match(explorer,/variant=\{view\}/);
 assert.match(explorer,/variant="detail"/);
 assert.doesNotMatch(glyph,/<img|https?:\/\//);
});

test('folders take precedence over suffix, common extensions ignore case',()=>{
 assert.equal(fileIconKind('Documents.docx',true),'folder');
 assert.equal(fileIconKind('REPORT.DOCX'),'word');
 assert.equal(fileIconKind('预算.XLSX'),'excel');
 assert.equal(fileIconKind('幻灯片.PPTX'),'powerpoint');
 assert.equal(fileIconKind('Archive.RAR'),'rar');
 assert.equal(fileIconKind('App.TS'),'typescript');
});
