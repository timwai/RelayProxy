import assert from 'node:assert/strict';
import test from 'node:test';
import {fileIconKind,fileIconLabel} from './fileIconKind.js';

test('remote file icons identify common office, media and developer extensions',()=>{
 const cases={
  '合同.PDF':'pdf','预算.xlsx':'spreadsheet','sales.CSV':'spreadsheet',
  '演示.pptx':'presentation','report.docx':'document',
  'cover.avif':'image','thumbnail.SVG':'vector','poster.psd':'design','asset.glb':'model3d',
  'clip.mkv':'video','movie.srt':'subtitle','song.flac':'audio',
  'backup.tar.gz':'archive','bundle.7z':'archive','boot.iso':'disk',
  'book.epub':'ebook','main.rs':'rust','widget.jsx':'javascript',
  'index.tsx':'typescript','script.py':'python','service.go':'golang',
  'experiment.ipynb':'notebook','page.html':'web','theme.scss':'stylesheet',
  'dockerfile':'code','query.sql':'database','archive.sqlite3':'database',
  'analytics.parquet':'data','response.json':'json','config.yaml':'config',
  '.env.local':'config','readme.md':'markdown','cert.pem':'certificate',
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
 assert.equal(fileIconLabel('readme.md'),'Markdown');
 assert.equal(fileIconLabel('asset.glb'),'三维模型');
 assert.equal(fileIconLabel('keys.p12'),'证书与密钥');
 assert.equal(fileIconLabel('something.unknown'),'文件');
 assert.equal(fileIconKind(null),'file');
 assert.equal(fileIconKind('empty.'),'file');
});

test('all V4 explorer surfaces reuse the same SVG illustration component',async()=>{
 const {readFileSync}=await import('node:fs');
 const explorer=readFileSync(new URL('./FileBrowserV4.jsx',import.meta.url),'utf8');
 const shares=readFileSync(new URL('./FileSharingPage.jsx',import.meta.url),'utf8');
 const glyph=readFileSync(new URL('./FileGlyph.jsx',import.meta.url),'utf8');
 assert.match(explorer,/variant="tree"/);
 assert.match(explorer,/variant={view}/);
 assert.match(explorer,/variant="detail"/);
 assert.match(shares,/<FileGlyph name={share.name} isDir/);
 assert.match(glyph,/viewBox="0 0 40 40"/);
 assert.match(glyph,/aria-hidden="true"/);
 assert.doesNotMatch(glyph,/<img|https?:\/\//);
});
