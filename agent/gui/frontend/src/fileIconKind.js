// Fast, deterministic metadata classification. Never reads or opens remote
// contents, so 5000-entry directories require no additional Agent requests.
const groups={
 // Office formats use distinct app-style marks, not generic document icons.
 word:['doc','docx','docm','dot','dotx','dotm'],
 excel:['xls','xlsx','xlsm','xlsb','xlt','xltx','xltm'],
 powerpoint:['ppt','pptx','pptm','pot','potx','potm','pps','ppsx','ppsm'],
 document:['odt','rtf','pages','wpd'],
 spreadsheet:['csv','ods','numbers','tsv','dif'],
 presentation:['odp','keynote','key'],
 pdf:['pdf'],
 image:['png','jpg','jpeg','gif','webp','avif','heic','heif','bmp','tif','tiff','ico','jfif','apng','cr2','nef','arw','dng','raf'],
 vector:['svg','ai','eps'],
 design:['psd','psb','fig','sketch','xd','afdesign','afphoto','indd'],
 model3d:['blend','fbx','obj','stl','gltf','glb','step','stp','3mf','usd','usdz'],
 video:['mp4','m4v','mov','mkv','avi','webm','wmv','flv','mpeg','mpg','3gp','ts','mts','m2ts'],
 audio:['mp3','m4a','wav','flac','aac','ogg','opus','wma','aiff','mid','midi','ape','alac'],
 subtitle:['srt','vtt','ass','ssa','sub'],
 zip:['zip','zipx'],
 rar:['rar'],
 sevenzip:['7z'],
 tar:['tar','tgz','tbz','tbz2','txz','tzst'],
 compressed:['gz','gzip','bz2','xz','zst','zstd','br','lz','lz4'],
 archive:['cab','arj','lzh','ace','cpio'],
 disk:['iso','dmg','img','vhd','vhdx','vmdk','qcow2'],
 ebook:['epub','mobi','azw','azw3','fb2'],
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
 android:['apk','aab'],
 windows:['exe','msi','msix','msixbundle'],
 apple:['ipa','app'],
 package:['deb','rpm','pkg'],
 application:['dll','so','dylib','bin','wasm']
};
const byExtension=Object.freeze(Object.fromEntries(
 Object.entries(groups).flatMap(([kind,extensions])=>extensions.map(ext=>[ext,kind]))
));
const labels=Object.freeze({
 folder:'文件夹',
 word:'Word 文档',excel:'Excel 工作簿',powerpoint:'PowerPoint 演示文稿',
 document:'文档',spreadsheet:'表格',presentation:'演示文稿',pdf:'PDF 文档',
 image:'图片',vector:'矢量图',design:'设计文件',model3d:'三维模型',
 video:'视频',audio:'音频',subtitle:'字幕',
 zip:'ZIP 压缩包',rar:'RAR 压缩包',sevenzip:'7Z 压缩包',
 tar:'TAR 归档',compressed:'压缩文件',archive:'归档文件',disk:'磁盘镜像',ebook:'电子书',
 javascript:'JavaScript',typescript:'TypeScript',python:'Python',golang:'Go',rust:'Rust',notebook:'Notebook',
 web:'网页',stylesheet:'样式表',code:'代码',database:'数据库',data:'数据文件',
 json:'JSON 数据',config:'配置文件',markdown:'Markdown',text:'文本',certificate:'证书与密钥',
 font:'字体',android:'Android 安装包',windows:'Windows 程序',apple:'Apple 应用',
 package:'软件安装包',application:'程序文件',file:'文件'
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
 // Multi-part TAR archives keep their actual container icon. A bare .gz
 // or .xz remains a compressed stream instead of being labeled TAR.
 if(/\\.tar\\.(?:gz|bz2|xz|zst|zstd)$/.test(lower))return 'tar';
 const dot=lower.lastIndexOf('.');
 if(dot<0||dot===lower.length-1)return 'file';
 return byExtension[lower.slice(dot+1)]||'file';
}
export function fileIconLabel(name,isDir=false){
 return labels[fileIconKind(name,isDir)];
}
