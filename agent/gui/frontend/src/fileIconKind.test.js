import assert from 'node:assert/strict';
import test from 'node:test';
import {fileIconKind,fileIconLabel} from './fileIconKind.js';

test('remote file icons identify common office, media and developer extensions',()=>{
 const cases={
  '合同.PDF':'pdf','预算.xlsx':'spreadsheet','sales.CSV':'spreadsheet',
  '演示.pptx':'presentation','report.docx':'document',
  'cover.avif':'image','thumbnail.SVG':'image','clip.mkv':'video',
  'song.flac':'audio','backup.tar.gz':'archive','bundle.7z':'archive',
  'main.rs':'code','widget.jsx':'code','dockerfile':'code',
  'config.yaml':'config','.env.local':'config','readme.md':'text',
  'font.woff2':'font','release.apk':'application',
  'unknown.xyz':'file','no-extension':'file','.hidden':'file',
 };
 for(const [name,kind] of Object.entries(cases)){
  assert.equal(fileIconKind(name),kind,name);
 }
});
test('folder icon and labels override name suffix with predictable fallback',()=>{
 assert.equal(fileIconKind('photos.zip',true),'folder');
 assert.equal(fileIconLabel('photos.zip',true),'文件夹');
 assert.equal(fileIconLabel('resume.pdf'),'PDF 文档');
 assert.equal(fileIconLabel('something.unknown'),'文件');
 assert.equal(fileIconKind(null),'file');
 assert.equal(fileIconKind('empty.'),'file');
});
