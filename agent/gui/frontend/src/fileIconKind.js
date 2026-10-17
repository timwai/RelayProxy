// Fast, deterministic metadata classification. Never reads or opens remote
// contents, so 5000-entry directories require no additional Agent requests.
const groups={
 document:['doc','docx','odt','rtf','pages'],
 spreadsheet:['xls','xlsx','csv','ods','numbers','tsv'],
 presentation:['ppt','pptx','odp','key'],
 pdf:['pdf'],
 image:['png','jpg','jpeg','gif','webp','avif','heic','heif','bmp','tif','tiff','svg','ico'],
 video:['mp4','m4v','mov','mkv','avi','webm','wmv','flv','mpeg','mpg','3gp'],
 audio:['mp3','m4a','wav','flac','aac','ogg','opus','wma','aiff','mid','midi'],
 archive:['zip','7z','rar','tar','gz','tgz','bz2','xz','zst','zstd','cab','iso','dmg'],
 code:['js','jsx','ts','tsx','mjs','cjs','py','go','rs','java','kt','swift','c','h','cpp','hpp','cs','rb','php','sh','bat','ps1','html','css','scss','sql','vue','svelte'],
 config:['json','jsonc','yaml','yml','toml','xml','ini','conf','cfg','properties','lock','env'],
 text:['txt','md','markdown','log','rst','tex'],
 font:['ttf','otf','woff','woff2','eot'],
 application:['exe','msi','app','apk','aab','deb','rpm','pkg','dll','so','dylib','bin']
};
const byExtension=Object.freeze(Object.fromEntries(
 Object.entries(groups).flatMap(([kind,extensions])=>extensions.map(ext=>[ext,kind]))
));
const labels=Object.freeze({
 folder:'文件夹',document:'文档',spreadsheet:'表格',presentation:'演示文稿',
 pdf:'PDF 文档',image:'图片',video:'视频',audio:'音频',archive:'压缩文件',
 code:'代码',config:'配置文件',text:'文本',font:'字体',application:'应用程序',file:'文件'
});
const specialNames=Object.freeze({
 dockerfile:'code',makefile:'code',gnumakefile:'code',
 '.gitignore':'config','.gitattributes':'config','.editorconfig':'config',
 '.env':'config','.env.example':'config','.npmrc':'config',
 license:'text',notice:'text',readme:'text'
});
export function fileIconKind(name,isDir=false){
 if(isDir)return 'folder';
 if(typeof name!=='string'||!name.trim())return 'file';
 const lower=name.trim().toLowerCase();
 if(specialNames[lower])return specialNames[lower];
 if(lower.startsWith('.env.'))return 'config';
 const dot=lower.lastIndexOf('.');
 if(dot<0||dot===lower.length-1)return 'file';
 return byExtension[lower.slice(dot+1)]||'file';
}
export function fileIconLabel(name,isDir=false){
 return labels[fileIconKind(name,isDir)];
}
