// Fast, deterministic metadata classification. Never reads or opens remote
// contents, so 5000-entry directories require no additional Agent requests.
const groups={
 document:['doc','docx','odt','rtf','pages'],
 spreadsheet:['xls','xlsx','csv','ods','numbers','tsv'],
 presentation:['ppt','pptx','odp','keynote','key'],
 pdf:['pdf'],
 image:['png','jpg','jpeg','gif','webp','avif','heic','heif','bmp','tif','tiff','ico'],
 vector:['svg','ai','eps'],
 design:['psd','psb','fig','sketch','xd','afdesign','afphoto','indd'],
 model3d:['blend','fbx','obj','stl','gltf','glb','step','stp','3mf'],
 video:['mp4','m4v','mov','mkv','avi','webm','wmv','flv','mpeg','mpg','3gp'],
 audio:['mp3','m4a','wav','flac','aac','ogg','opus','wma','aiff','mid','midi'],
 subtitle:['srt','vtt','ass','ssa','sub'],
 archive:['zip','7z','rar','tar','gz','tgz','bz2','xz','zst','zstd','cab'],
 disk:['iso','dmg','img','vhd','vhdx','vmdk','qcow2'],
 ebook:['epub','mobi','azw','azw3'],
 javascript:['js','jsx','mjs','cjs'],
 typescript:['ts','tsx','mts','cts'],
 python:['py','pyw','pyi'],
 golang:['go'],
 rust:['rs'],
 notebook:['ipynb'],
 web:['html','htm','xhtml'],
 stylesheet:['css','scss','sass','less'],
 code:['java','kt','kts','swift','c','h','cpp','hpp','cs','rb','php','sh','bat','ps1','vue','svelte','dart','lua','pl','r','scala','ex','exs'],
 database:['db','db3','sqlite','sqlite3','sql','mdb','accdb'],
 data:['parquet','arrow','feather','h5','hdf5','pkl'],
 json:['json','jsonc','json5','ndjson'],
 config:['yaml','yml','toml','xml','ini','conf','cfg','properties','lock','env'],
 markdown:['md','markdown','mdx'],
 text:['txt','log','rst','tex'],
 certificate:['crt','cer','pem','p12','pfx','pub'],
 font:['ttf','otf','woff','woff2','eot'],
 application:['exe','msi','app','apk','aab','deb','rpm','pkg','dll','so','dylib','bin']
};
const byExtension=Object.freeze(Object.fromEntries(
 Object.entries(groups).flatMap(([kind,extensions])=>extensions.map(ext=>[ext,kind]))
));
const labels=Object.freeze({
 folder:'文件夹',document:'文档',spreadsheet:'表格',presentation:'演示文稿',
 pdf:'PDF 文档',image:'图片',vector:'矢量图',design:'设计文件',model3d:'三维模型',
 video:'视频',audio:'音频',subtitle:'字幕',archive:'压缩文件',disk:'磁盘镜像',ebook:'电子书',
 javascript:'JavaScript',typescript:'TypeScript',python:'Python',golang:'Go',rust:'Rust',notebook:'Notebook',
 web:'网页',stylesheet:'样式表',code:'代码',database:'数据库',data:'数据文件',
 json:'JSON 数据',config:'配置文件',markdown:'Markdown',text:'文本',certificate:'证书与密钥',
 font:'字体',application:'应用程序',file:'文件'
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
