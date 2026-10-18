import assert from 'node:assert/strict';
import test from 'node:test';
import {fileIconKind,fileIconLabel} from './fileIconKind.js';

test('remote file icons identify common office, media and developer extensions',()=>{
 const cases={
  '合同.PDF':'pdf','预算.xlsx':'excel','sales.CSV':'spreadsheet',
  '演示.pptx':'powerpoint','report.docx':'word',
  'cover.avif':'image','thumbnail.SVG':'vector','poster.psd':'design','asset.glb':'model3d',
  'clip.mkv':'video','movie.srt':'subtitle','song.flac':'audio',
  'backup.tar.gz':'tar','bundle.7z':'sevenzip','boot.iso':'disk',
  'book.epub':'ebook','main.rs':'rust','widget.jsx':'javascript',
  'index.tsx':'typescript','script.py':'python','service.go':'golang',
  'experiment.ipynb':'notebook','page.html':'web','theme.scss':'stylesheet',
  'dockerfile':'code','query.sql':'database','archive.sqlite3':'database',
  'analytics.parquet':'data','response.json':'json','config.yaml':'config',
  '.env.local':'config','readme.md':'markdown','cert.pem':'certificate',
  'font.woff2':'font','release.apk':'android',
  '合同.doc':'word','讲义.docm':'word','报表.xls':'excel','自动化.xlsm':'excel',
  '展示.ppt':'powerpoint','放映.ppsx':'powerpoint',
  '会议.odt':'document','data.ods':'spreadsheet','slides.odp':'presentation',
  'photos.zip':'zip','backup.zipx':'zip','pack.rar':'rar',
  'backup.tar':'tar','rootfs.tar.xz':'tar','data.tar.zst':'tar',
  'raw.gz':'compressed','raw.xz':'compressed','backup.tgz':'tar',
  'old.cab':'archive','installer.exe':'windows','setup.msix':'windows',
  'client.aab':'android','app.ipa':'apple','update.deb':'package',
  'plugin.dll':'application','cert.p12':'certificate',
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
 assert.equal(fileIconLabel('合同.docx'),'Word 文档');
 assert.equal(fileIconLabel('工作簿.xlsx'),'Excel 工作簿');
 assert.equal(fileIconLabel('演示.pptx'),'PowerPoint 演示文稿');
 assert.equal(fileIconLabel('压缩包.zip'),'ZIP 压缩包');
 assert.equal(fileIconLabel('压缩包.rar'),'RAR 压缩包');
 assert.equal(fileIconLabel('压缩包.7z'),'7Z 压缩包');
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
