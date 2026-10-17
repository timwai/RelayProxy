import React,{useCallback,useEffect,useRef,useState} from 'react';
import {Icon} from './icons.jsx';
import FileGlyph from './FileGlyph.jsx';
import {fileIconLabel} from './fileIconKind.js';
import {call,callJSON,hasBridge} from './bridge.js';
import {planSameShareMoves,contextSelection} from './fileBrowserV4Ops.js';
import './fileBrowserV4.css';

const join=(dir,name)=>dir==='.'?name:dir+'/'+name;
const parent=dir=>dir==='.'?'.':(dir.includes('/')?dir.slice(0,dir.lastIndexOf('/')):'.');
const fmt=n=>{const bytes=Number(n)||0;return bytes<1024?bytes+' B':bytes<1048576?(bytes/1024).toFixed(1)+' KiB':bytes<1073741824?(bytes/1048576).toFixed(1)+' MiB':(bytes/1073741824).toFixed(2)+' GiB'};
const date=n=>n?new Date(n>1e12?n:n*1000).toLocaleString():'—';
const ext=name=>(name.split('.').pop()||'').toLowerCase();
const focusParam=()=>new URLSearchParams(window.location.search).has('filesWindow');
const within=(x,min,max)=>Math.max(min,Math.min(max,x));

export default function FileBrowserV4({
 peers,device,share,path,shares,entries,loading,error,latency,canWrite,warmStatus,treeRevision,
 uploadStaged,uploadTotal,uploading,transfers,onDevice,onDirectory,onDownload,onUpload,onMutate,onCancelUpload,onCancelTransfer,onResumeTransfer,onClearTransfer,onClearInactiveTransfers,onOpenTransfers,onRefresh,toast
}){
 const [query,setQuery]=useState(''),[sort,setSort]=useState('name'),[view,setView]=useState('list');
 const [selected,setSelected]=useState([]),[anchor,setAnchor]=useState(''),[details,setDetails]=useState(false),[visibleLimit,setVisibleLimit]=useState(200);
 const [focus,setFocus]=useState(focusParam),[treeOpen,setTreeOpen]=useState(false),[dockOpen,setDockOpen]=useState(false);
 const [menu,setMenu]=useState(null),[dialog,setDialog]=useState(null),[dropTarget,setDropTarget]=useState('');
 const [treeWidth,setTreeWidth]=useState(248),[detailWidth,setDetailWidth]=useState(286);
 const [expanded,setExpanded]=useState(new Set()),[treeFolders,setTreeFolders]=useState({});
 const box=useRef(null),input=useRef(null),menuRef=useRef(null),dragInfo=useRef(null),uploadTarget=useRef(null),touchTimer=useRef(null);
 const writable=!!canWrite&&!loading&&!uploading;
 const activeShare=shares.find(x=>x.id===share);
 const filtered=[...entries].filter(x=>x.name.toLocaleLowerCase().includes(query.toLocaleLowerCase())).sort((a,b)=>{
  if(a.isDir!==b.isDir)return a.isDir?-1:1;
  if(sort==='size')return (b.size||0)-(a.size||0)||a.name.localeCompare(b.name);
  if(sort==='time')return (b.modTime||0)-(a.modTime||0)||a.name.localeCompare(b.name);
  return a.name.localeCompare(b.name,undefined,{numeric:true,sensitivity:'base'});
 });
 const chosen=entries.filter(x=>selected.includes(x.name));
 const preview=chosen.length===1?chosen[0]:null;
 const runningTransfers=transfers.filter(item=>item.state==='running');
 const recentTransfers=runningTransfers.length?runningTransfers.slice(-3).reverse():transfers.slice(-3).reverse();
 // Keep the file viewport large while idle; reveal progress when a transfer starts.
 // Users may still collapse the dock during a running transfer.
 useEffect(()=>{if(runningTransfers.length>0)setDockOpen(true)},[runningTransfers.length]);
 const transferLabel={running:'进行中',completed:'已完成',failed:'失败',cancelled:'已取消',paused:'已暂停'};
 const rootName=activeShare?.name||'共享文件夹';
 const deviceName=peers.find(x=>x.deviceId===device)?.name||device||'选择设备';
 useEffect(()=>{setSelected([]);setAnchor('');setQuery('');setMenu(null);setDialog(null);setVisibleLimit(200)},[device,share,path]);
 const treeGeneration=useRef(0);
 useEffect(()=>{treeGeneration.current+=1;setExpanded(new Set());setTreeFolders({})},[treeRevision]);
 useEffect(()=>setVisibleLimit(200),[query,sort,view]);
 useEffect(()=>{setSelected([]);setAnchor('')},[query]);
 useEffect(()=>{setSelected([]);setAnchor('');setMenu(null)},[entries]);
 useEffect(()=>{document.documentElement.classList.toggle('file-browser-focus',focus);return()=>document.documentElement.classList.remove('file-browser-focus')},[focus]);
 useEffect(()=>{const keyHandler=e=>{
   if(!box.current?.contains(e.target)||menu||dialog)return;
   if(e.target instanceof Element&&e.target.closest('input,textarea,select,[contenteditable="true"]'))return;
   if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='a'){e.preventDefault();setSelected(filtered.map(x=>x.name))}
   if(e.key==='F2'&&chosen.length===1&&canWrite){e.preventDefault();requestRename(chosen[0])}
   if((e.key==='Delete'||e.key==='Backspace')&&chosen.length&&canWrite){e.preventDefault();requestRemove(chosen)}
  };document.addEventListener('keydown',keyHandler);return()=>document.removeEventListener('keydown',keyHandler)
 },[chosen.map(x=>x.name).join('|'),canWrite,filtered.map(x=>x.name).join('|'),menu,dialog]);
 useEffect(()=>{const close=e=>{if(e.key==='Escape'){if(dialog)setDialog(null);else if(menu)setMenu(null);else if(focus)setFocus(false);else setSelected([])}};
  document.addEventListener('keydown',close);return()=>document.removeEventListener('keydown',close)},[dialog,menu,focus]);
 useEffect(()=>{const clear=e=>{if(!e.target.closest('.file-v4-context'))setMenu(null)};document.addEventListener('pointerdown',clear);return()=>document.removeEventListener('pointerdown',clear)},[]);
 useEffect(()=>{if(!menu||!menuRef.current)return;const bounds=menuRef.current.getBoundingClientRect();setMenu(prev=>prev?{...prev,x:Math.max(8,Math.min(prev.x,window.innerWidth-bounds.width-8)),y:Math.max(8,Math.min(prev.y,window.innerHeight-bounds.height-8))}:null)},[menu?.key]);
 const report=message=>toast?.(message);
 const navigate=(dev,id,dir='.')=>{setTreeOpen(false);onDirectory(dev,id,dir)};
 const openDevice=id=>{setTreeOpen(false);onDevice(id)};
 const showMenu=(e,kind,item=null,target=null)=>{
  e.preventDefault();e.stopPropagation();
  const targets=kind==='file'&&item?contextSelection(item,selected,entries):[];
  if(kind==='file'&&item&&!selected.includes(item.name)){setSelected([item.name]);setAnchor(item.name)}
  setMenu({key:Date.now()+'-'+Math.random(),kind,item,target,targets,x:e.clientX||10,y:e.clientY||10});
 };
 const clickFile=(e,item)=>{
  if(e.ctrlKey||e.metaKey){setSelected(prev=>prev.includes(item.name)?prev.filter(x=>x!==item.name):[...prev,item.name]);setAnchor(item.name)}
  else if(e.shiftKey&&anchor){const names=filtered.map(x=>x.name),a=names.indexOf(anchor),b=names.indexOf(item.name);if(a>=0)setSelected(names.slice(Math.min(a,b),Math.max(a,b)+1))}
  else {setSelected([item.name]);setAnchor(item.name)}
 };
 const setAll=e=>setSelected(e.target.checked?filtered.map(x=>x.name):[]);
 const toggleDetail=()=>setDetails(v=>!v);
 const mutate=async(action,oldPath,dest='',scope={device,share,path})=>{
  const permission=peers.find(p=>p.deviceId===scope.device)?.shares?.find(item=>item.id===scope.share)?.permission;
   if(permission!=='read_write'||uploading||(scope.device===device&&scope.share===share&&loading)){
    report('当前目标共享没有可用的写入权限');return false
   }
  try{await onMutate(action,oldPath,dest,scope);setSelected([]);return true}
  catch(e){report(e.message||String(e));return false}
 };
 const requestRemove=items=>setDialog({kind:'remove',items});
 const requestRename=item=>setDialog({kind:'rename',item,value:item.name});
 const requestCreate=(dir,scope={device,share,path})=>setDialog({kind:'mkdir',dir,scope,value:'新建文件夹'});
 const requestMove=items=>setDialog({kind:'move',items,value:'.'});
 const action=async(type,item,scope,targets=null)=>{
  setMenu(null);
  if(type==='refresh'){onRefresh();return}
  if(type==='details'){toggleDetail();return}
  if(type==='upload'){uploadTarget.current=scope||{path,share,device};input.current?.click();return}
  if(type==='mkdir'){requestCreate(scope?.path??path);return}
  if(type==='open'){if(item?.isDir)navigate(device,share,join(path,item.name));else setDetails(true);return}
  if(type==='name'){if(item?.name){try{if(hasBridge('goCopyClipboard'))await call('goCopyClipboard',item.name);else await navigator.clipboard.writeText(item.name);report('已复制文件名')}catch{report('复制失败')}}return}
  if(type==='download'){for(const entry of item?([item]):chosen.filter(x=>!x.isDir))await onDownload(entry);return}
  if(type==='rename'){requestRename(item);return}
  if(type==='move'){requestMove(targets||chosen);return}
  if(type==='remove'){requestRemove(targets||chosen);return}
 };
 const itemsForMenu=()=>{
  if(!menu)return [];
  if(menu.kind==='tree')return [
   ['open','打开文件夹'],['refresh','刷新目录'],
   ...(menu.target?.permission==='read_write'?[['upload','上传到此目录'],['mkdir','新建文件夹']]:[])];
  if(menu.kind==='background')return [
   ['refresh','刷新'],...(canWrite?[['upload','上传文件'],['mkdir','新建文件夹']]:[]),
   ['details',details?'隐藏详情':'显示详情']];
  return [
   ['open',menu.item?.isDir?'打开文件夹':'查看详情'],
   ...(menu.item?.isDir?[]:[['download','下载']]),
   ['name','复制名称'],
   ...(canWrite?[['rename','重命名'],['move','移动到…'],['remove','删除']]:[])
  ];
 };
 const confirmDialog=async()=>{
  if(!dialog)return;
  if(dialog.kind==='mkdir'){
   const name=(dialog.value||'').trim();
   if(!name||name==='.'||name==='..'||name.includes('/')||name.includes('\\')){report('文件夹名称无效');return}
   if(await mutate('mkdir',join(dialog.dir,name),'',dialog.scope)){
    setDialog(null);
    if(dialog.scope.device!==device||dialog.scope.share!==share||dialog.dir!==path)
     navigate(dialog.scope.device,dialog.scope.share,dialog.dir);
   }
  }else if(dialog.kind==='rename'){
   const name=(dialog.value||'').trim();
   if(!name||name.includes('/')||name.includes('\\')||name==='.'||name==='..'){report('名称无效');return}
   if(await mutate('move',join(path,dialog.item.name),join(path,name)))setDialog(null);
  }else if(dialog.kind==='remove'){
   // This dialog is already an explicit confirmation; do not stack a native confirm.
   for(const item of dialog.items){if(!await mutate('remove',join(path,item.name)))return}
   setDialog(null);
  }else if(dialog.kind==='move'){
   const dest=dialog.value||'.';
   for(const item of dialog.items){
    const src=join(path,item.name),dst=join(dest,item.name);
    if(src===dst){report('目标与当前位置相同');return}
    if(item.isDir&&(dest===src||dest.startsWith(src+'/'))){report('不能移动到自身或子目录');return}
    if(!await mutate('move',src,dst))return;
   }
   setDialog(null);
  }
 };
 const resize=(e,side)=>{
  if(e.button!==0||!box.current)return;e.preventDefault();
  const node=e.currentTarget,nodeBounds=box.current.getBoundingClientRect(),start=e.clientX,initial=side==='tree'?treeWidth:detailWidth;
  node.setPointerCapture(e.pointerId);
  const move=event=>{if(side==='tree')setTreeWidth(within(initial+event.clientX-start,190,Math.min(420,nodeBounds.width*.42)));
   else setDetailWidth(within(initial+start-event.clientX,230,Math.min(420,nodeBounds.width*.42)))};
  const stop=()=>{node.removeEventListener('pointermove',move);node.removeEventListener('pointerup',stop);node.removeEventListener('pointercancel',stop)};
  node.addEventListener('pointermove',move);node.addEventListener('pointerup',stop);node.addEventListener('pointercancel',stop);
 };
 const newWindowURL=()=>{
  const url=new URL(window.location.href);
  url.searchParams.set('page','files');url.searchParams.set('filesWindow','1');
  if(device&&share){
   url.searchParams.set('fileDevice',device);
   url.searchParams.set('fileShare',share);
   url.searchParams.set('filePath',path);
  }
  return url.toString();
 };
 const returnToAppURL=()=>{
  const url=new URL(window.location.href);
  for(const field of ['filesWindow','fileDevice','fileShare','filePath'])url.searchParams.delete(field);
  url.searchParams.set('page','files');
  return url.toString();
 };
 const openNativeWindow=()=>{
  if(typeof window.goOpenFileBrowserWindow!=='function'){
   report('独立窗口接口不可用');return;
  }
  void window.goOpenFileBrowserWindow(device||'',share||'',path)
   .catch(e=>report('打开独立窗口失败：'+(e.message||e)));
 };
 const uploadAt=async(files,dir=path,id=share,dev=device)=>{
  setDropTarget('');
  if(!files?.length)return;
  const target=peers.find(p=>p.deviceId===dev)?.shares?.find(s=>s.id===id);
  if(!target||target.permission!=='read_write'){report('该共享目录没有上传权限');return}
  try{await onUpload(Array.from(files),dir,id,dev)}catch(e){report(e.message||String(e))}
 };
 const handleDrop=async(e,targetPath=path,targetShare=share,targetDevice=device)=>{
  e.preventDefault();e.stopPropagation();setDropTarget('');
  const raw=e.dataTransfer.getData('application/x-relayproxy-files');
  if(raw){
   let payload;try{payload=JSON.parse(raw)}catch{report('拖拽数据无效');return}
   let plan;
   try{
    plan=planSameShareMoves(payload,{device,share,path},
     {device:targetDevice,share:targetShare,path:targetPath},
     !!dragInfo.current&&raw===JSON.stringify(dragInfo.current));
   }catch(err){report(err.message||String(err));return}
   if(!writable){report('当前共享为只读');return}
   for(const step of plan){
    if(!await mutate('move',step.source,step.destination,{device,share,path}))return;
   }
   }else{
   const items=Array.from(e.dataTransfer.items||[]);
   if(items.some(item=>item.kind==='file'&&item.webkitGetAsEntry?.()?.isDirectory)){
    report('当前只支持拖入文件，请使用系统选择器或先压缩文件夹');return;
   }
   await uploadAt(e.dataTransfer.files,targetPath,targetShare,targetDevice);
  }
 };
 const dragStart=(e,item)=>{
  if(!writable){e.preventDefault();return}
  const names=selected.includes(item.name)?selected:[item.name];
  const payload={device,share,path,names};
  dragInfo.current=payload;
  e.dataTransfer.setData('application/x-relayproxy-files',JSON.stringify(payload));
  e.dataTransfer.effectAllowed='move';
 };
 const dragOver=(e,key,permit)=>{e.preventDefault();e.stopPropagation();e.dataTransfer.dropEffect=permit?'move':'none';setDropTarget(permit?key:'')};
 const acceptTarget=(dev,id)=>!!peers.find(p=>p.deviceId===dev)?.shares?.some(s=>s.id===id&&s.permission==='read_write');
 const treeKey=(dev,id,dir)=>JSON.stringify([dev,id,dir]);
 const toggleTree=async(dev,id,dir)=>{
  const key=treeKey(dev,id,dir);
  if(expanded.has(key)){setExpanded(prev=>{const next=new Set(prev);next.delete(key);return next});return}
  setExpanded(prev=>new Set([...prev,key]));
  if(Object.prototype.hasOwnProperty.call(treeFolders,key))return;
  try{
   const generation=treeGeneration.current;
   const records=await callJSON('goGetRemoteFileEntries',[],dev,id,dir);
   if(generation!==treeGeneration.current)return;
   setTreeFolders(prev=>({...prev,[key]:(Array.isArray(records)?records:[]).filter(x=>x.isDir)}));
  }catch(e){setExpanded(prev=>{const next=new Set(prev);next.delete(key);return next});report('读取目录树失败：'+(e.message||e))}
 };
 const renderTreeBranch=(dev,id,dir,depth=0)=>{
  const key=treeKey(dev,id,dir);
  if(!expanded.has(key))return null;
  const rows=treeFolders[key];
  if(!rows)return <div className="v4-tree-loading">读取目录中…</div>;
  if(rows.length===0)return null;
  return <div className="v4-tree-branch">{rows.map(entry=>{
   const next=join(dir,entry.name),opened=expanded.has(treeKey(dev,id,next));
   return <div key={next}>
    <div className="v4-tree-folder">
     <button className="v4-tree-expand" aria-label={'展开 '+entry.name} onClick={()=>void toggleTree(dev,id,next)}>{opened?'⌄':'›'}</button>
     <button className={'v4-nested '+(share===id&&path===next?'active':'')}
      onClick={()=>navigate(dev,id,next)}
      onContextMenu={e=>showMenu(e,'tree',null,{path:next,share:id,device:dev,permission:peers.find(p=>p.deviceId===dev)?.shares.find(x=>x.id===id)?.permission})}
      onDragOver={e=>dragOver(e,'tree-'+next,acceptTarget(dev,id))}
      onDragLeave={()=>setDropTarget('')}
      onDrop={e=>handleDrop(e,next,id,dev)}>
      <FileGlyph name={entry.name} isDir variant="tree"/><span>{entry.name}</span></button>
    </div>
    {depth<16?renderTreeBranch(dev,id,next,depth+1):null}
   </div>
  })}</div>
 };

 return <section className={'file-browser-v4 '+(details?'has-details ':'')+(treeOpen?'tree-open ':'')} ref={box}
  style={{'--file-tree-width':treeWidth+'px','--file-detail-width':detailWidth+'px'}} aria-label="远程文件浏览器">
  <aside className="v4-tree">
   <div className="v4-tree-head"><strong>已授权设备</strong><button className="btn quiet" onClick={onRefresh} title="刷新设备"><Icon name="activity"/></button></div>
   <div className="v4-tree-scroll">{peers.length===0&&<div className="file-browser-aside-empty">暂无在线且授权访问的共享目录</div>}
    {peers.map(d=><div className="v4-device" key={d.deviceId}>
     <button className={'v4-device-row '+(device===d.deviceId?'active':'')} onClick={()=>openDevice(d.deviceId)}>
      <Icon name="devices" size={17}/><span>{d.name||d.deviceId}</span><small>{d.shares.length} 个共享</small></button>
     {device===d.deviceId&&<div className="v4-shares">{d.shares.map(s=><div key={s.id}>
      <div className="v4-tree-folder">
       <button className="v4-tree-expand" aria-label={'展开 '+s.name} onClick={()=>void toggleTree(d.deviceId,s.id,'.')}>
        {expanded.has(treeKey(d.deviceId,s.id,'.'))?'⌄':'›'}</button>
       <button className={'v4-share '+(share===s.id?'active':'')}
        onClick={()=>navigate(d.deviceId,s.id,'.')}
        onContextMenu={e=>showMenu(e,'tree',null,{path:'.',share:s.id,device:d.deviceId,permission:s.permission})}
        onDragOver={e=>dragOver(e,'share-'+s.id,acceptTarget(d.deviceId,s.id))}
        onDragLeave={()=>setDropTarget('')}
        onDrop={e=>handleDrop(e,'.',s.id,d.deviceId)}>
        <FileGlyph name={s.name} isDir variant="tree"/><span>{s.name}</span><small>{s.permission==='read_write'?'读写':'只读'}</small>
       </button></div>
       {renderTreeBranch(d.deviceId,s.id,'.')}
      </div>)}</div>}
    </div>)}
   </div><div className="v4-tree-foot"><Icon name="shield" size={15}/>仅显示共享所有者实际授权的目录</div>
  </aside>
  <div className="v4-splitter v4-tree-split" role="separator" tabIndex={0} aria-label="调整设备树宽度"
   onPointerDown={e=>resize(e,'tree')} onDoubleClick={()=>setTreeWidth(248)}
   onKeyDown={e=>{if(e.key==='ArrowLeft'||e.key==='ArrowRight'){e.preventDefault();setTreeWidth(v=>within(v+(e.key==='ArrowRight'?16:-16),190,420))}}}/>
  <div className="v4-main">
   <div className="v4-head">
    <div className="v4-head-title"><div className="eyebrow">REMOTE FILE EXPLORER</div><h2>{rootName}</h2>
     <small>{deviceName} · {canWrite?'读写':'只读'}{warmStatus?' · '+warmStatus:''}{error?' · '+error:''}</small></div>
    <div className="v4-head-actions">
     <button className="btn v4-show-tree" onClick={()=>setTreeOpen(v=>!v)} title="选择设备"><Icon name="devices"/></button>
     {hasBridge('goOpenFileBrowserWindow')&&
      <button className="btn" onClick={openNativeWindow} title="通过 Wails 打开原生独立文件浏览器"><Icon name="monitor"/> <span className="v4-optional">独立窗口</span></button>}
     <a className="btn v4-window-link" href={focus?returnToAppURL():newWindowURL()}
      title={focus?'返回当前浏览器的管理界面':'在当前页面进入独立文件浏览器，不触发弹窗拦截'}>
      <Icon name="monitor"/> <span className="v4-optional">{focus?'返回主界面':'独立页面'}</span></a>
     <button className="btn" onClick={()=>setFocus(v=>!v)} title={focus?'退出专注模式':'文件浏览器充满整个窗口'}>
      <Icon name="dashboard"/> <span>{focus?'退出专注':'专注模式'}</span></button>
    </div>
   </div>
   <div className="v4-tools">
    <button className="btn quiet" onClick={()=>navigate(device,share,parent(path))} disabled={!share||path==='.'} aria-label="返回上级">↑</button>
    <nav className="v4-crumbs" aria-label="文件夹路径">{share&&<button onClick={()=>navigate(device,share,'.')}>{rootName}</button>}
     {path!=='.'&&path.split('/').map((name,i,a)=><React.Fragment key={i}><span>›</span><button onClick={()=>navigate(device,share,a.slice(0,i+1).join('/'))}>{name}</button></React.Fragment>)}</nav>
    <button className="btn quiet" title="刷新" onClick={onRefresh}><Icon name="activity"/></button>
   </div>
   <div className="v4-toolbar">
    <input type="search" value={query} onChange={e=>setQuery(e.target.value)} placeholder="搜索当前文件夹…" aria-label="搜索文件"/>
    <select value={sort} onChange={e=>setSort(e.target.value)} aria-label="排序">
     <option value="name">按名称</option><option value="time">按时间</option><option value="size">按大小</option></select>
    <div className="v4-view"><button className={view==='list'?'active':''} onClick={()=>setView('list')} title="列表">☰</button>
     <button className={view==='grid'?'active':''} onClick={()=>setView('grid')} title="网格">▦</button></div>
    <button className={'btn '+(details?'primary':'')} onClick={toggleDetail} title="文件详情"><Icon name="file"/><span className="v4-optional">详情</span></button>
    {canWrite&&<><button className="btn" onClick={()=>requestCreate(path)} disabled={loading}>＋ 文件夹</button>
     <button className="btn primary" onClick={()=>input.current?.click()} disabled={loading||uploading}>↑ 上传</button></>}
    <input type="file" multiple ref={input} hidden onChange={e=>{const files=Array.from(e.target.files||[]);const t=uploadTarget.current;uploadTarget.current=null;void uploadAt(files,t?.path??path,t?.share??share,t?.device??device);e.target.value=''}}/>
   </div>
   {uploading&&<div className="v4-upload-progress">上传准备：{fmt(uploadStaged)} / {fmt(uploadTotal)} <button className="btn danger" onClick={onCancelUpload}>取消上传准备</button></div>}
   <div className={'v4-file-scroll '+(dropTarget==='background'?'drop-active':'')}
    onContextMenu={e=>showMenu(e,'background')}
    onDragOverCapture={e=>{
      if(writable&&Array.from(e.dataTransfer.types||[]).includes('Files')){
       e.preventDefault();e.dataTransfer.dropEffect='copy';
      }
     }}
     onDragOver={e=>{if(!e.target.closest('[data-file-name]'))dragOver(e,'background',writable)}}
    onDragLeave={()=>setDropTarget('')}
    onDrop={e=>handleDrop(e)}>
   {selected.length>0&&<div className="v4-selection"><b>已选 {selected.length} 项</b>
    <button onClick={()=>setSelected([])}>取消选择</button>
    {chosen.some(x=>!x.isDir)&&<button onClick={async()=>{
     const files=chosen.filter(x=>!x.isDir);
     if(files.length>20&&!window.confirm('依次下载 '+files.length+' 个文件，需要逐个选择保存位置。确定继续吗？'))return;
     for(const x of files)await onDownload(x);
    }}>下载文件</button>}
    {canWrite&&<><button onClick={()=>requestMove(chosen)}>移动</button><button onClick={()=>requestRemove(chosen)}>删除</button></>}
   </div>}
    {!share?<div className="v4-placeholder">从左侧选择已授权共享目录</div>:
    <div className={'v4-file-list '+(view==='grid'?'grid':'list')}>
     {view==='list'&&<div className="v4-column-head"><input type="checkbox" checked={filtered.length>0&&selected.length===filtered.length} onChange={setAll} aria-label="全选"/>
      <span>名称</span><span>大小</span><span>修改时间</span><span></span></div>}
     {loading?<div className="v4-placeholder">正在加载目录…</div>:filtered.length===0?<div className="v4-placeholder">{query?'无匹配文件':'当前目录为空，可拖入文件上传'}</div>:
      filtered.slice(0,visibleLimit).map(entry=><div key={entry.name} data-file-name={entry.name}
       className={'v4-entry '+(selected.includes(entry.name)?'selected ':'')+(dropTarget==='file-'+entry.name?'drop-active':'')}
       tabIndex={0} draggable={writable}
       onClick={e=>clickFile(e,entry)}
       onTouchStart={e=>{const r=e.currentTarget.getBoundingClientRect();touchTimer.current=setTimeout(()=>{showMenu({preventDefault:()=>{},stopPropagation:()=>{},clientX:r.left+25,clientY:r.top+25},'file',entry)},650)}}
       onTouchEnd={()=>clearTimeout(touchTimer.current)} onTouchMove={()=>clearTimeout(touchTimer.current)}
       onDoubleClick={()=>entry.isDir?navigate(device,share,join(path,entry.name)):setDetails(true)}
       onContextMenu={e=>showMenu(e,'file',entry)}
       onKeyDown={e=>{if(e.key==='Enter')entry.isDir?navigate(device,share,join(path,entry.name)):setDetails(true);if(e.key==='ContextMenu'||e.key==='F10'&&e.shiftKey){const r=e.currentTarget.getBoundingClientRect();showMenu({preventDefault:()=>e.preventDefault(),stopPropagation:()=>e.stopPropagation(),clientX:r.left+24,clientY:r.top+24},'file',entry)}}}
       onDragStart={e=>dragStart(e,entry)} onDragEnd={()=>{dragInfo.current=null;setDropTarget('')}}
       onDragOver={entry.isDir?e=>dragOver(e,'file-'+entry.name,writable):undefined}
       onDragLeave={()=>setDropTarget('')}
       onDrop={entry.isDir?e=>handleDrop(e,join(path,entry.name)):undefined}>
       <input type="checkbox" checked={selected.includes(entry.name)} aria-label={'选择 '+entry.name}
        onClick={e=>e.stopPropagation()} onChange={e=>clickFile({ctrlKey:true},entry)}/>
       <FileGlyph name={entry.name} isDir={entry.isDir} variant={view}/>
       <span className="v4-entry-name" title={entry.name}>{entry.name}</span>
       <span className="v4-entry-size">{entry.isDir?'—':fmt(entry.size)}</span>
       <span className="v4-entry-date">{date(entry.modTime)}</span>
       <button className="v4-more" title="更多操作" onClick={e=>{e.stopPropagation();showMenu(e,'file',entry)}}>⋯</button>
      </div>)}
    </div>}
    {filtered.length>visibleLimit&&<button className="v4-load-more" onClick={()=>setVisibleLimit(n=>n+200)}>显示更多（剩余 {filtered.length-visibleLimit} 项）</button>}
   </div>
   <section className="v4-transfer-dock" aria-label="文件传输任务">
    <div className="v4-transfer-bar">
     <button type="button" className="v4-dock-toggle" aria-expanded={dockOpen} onClick={()=>setDockOpen(v=>!v)}>
      <Icon name="activity" size={15}/> 传输任务
      <span>{runningTransfers.length?runningTransfers.length+' 项进行中':transfers.length+' 项记录'}</span>
      <span aria-hidden="true">{dockOpen?'⌄':'⌃'}</span>
     </button>
     <div className="v4-dock-actions">
      {transfers.some(t=>t.state!=='running')&&<button type="button" className="btn quiet" onClick={onClearInactiveTransfers}>清除已结束</button>}
      <button type="button" className="btn quiet" onClick={onOpenTransfers}>查看全部</button>
     </div>
    </div>
    {dockOpen&&<div className="v4-transfer-list">{recentTransfers.length?recentTransfers.map(task=>{
     const total=Math.max(0,Number(task.total)||0),done=Math.max(0,Number(task.done)||0);
     const progress=total?Math.min(100,done/total*100):0;
     return <div key={task.id} className="v4-transfer-row">
      <span className="v4-transfer-kind">{task.direction==='upload'?'↑':'↓'}</span>
      <div className="v4-transfer-data">
       <div className="v4-transfer-name"><strong title={task.fileName}>{task.fileName||'文件传输'}</strong>
        <span>{transferLabel[task.state]||task.state} · {fmt(done)} / {total?fmt(total):'未知大小'}</span></div>
       <div className="v4-transfer-track"><div style={{width:progress+'%'}}/></div>
      </div>
      <span className="v4-transfer-speed">{task.state==='running'?fmt(task.speed||0)+'/s':(progress?Math.round(progress)+'%':'—')}</span>
      {task.state==='running'&&<button type="button" className="btn quiet" onClick={()=>onCancelTransfer?.(task.id)}>暂停</button>}
      {task.direction==='download'&&(task.state==='paused'||task.state==='failed')&&
       <button type="button" className="btn quiet" onClick={()=>onResumeTransfer?.(task.id)}>继续</button>}
      {task.state!=='running'&&<button type="button" className="btn quiet" title="只清除任务记录，不删除已下载文件" onClick={()=>onClearTransfer?.(task.id)}>清除</button>}
     </div>
    }):<div className="v4-transfer-empty">暂无传输任务，上传和下载将在这里显示进度。</div>}</div>}
   </section>
   <div className="v4-status"><span>{filtered.length} 个项目 · {canWrite?'可读写':'只读访问'}</span>
    <span>{latency?'目录读取 '+latency+' · ':''}传输中 {transfers.filter(x=>x.state==='running').length} 项</span></div>
  </div>
  {details&&<><div className="v4-splitter v4-detail-split" tabIndex={0} role="separator" aria-label="调整详情栏宽度"
    onPointerDown={e=>resize(e,'detail')} onDoubleClick={()=>setDetailWidth(286)}
    onKeyDown={e=>{if(e.key==='ArrowLeft'||e.key==='ArrowRight'){e.preventDefault();setDetailWidth(v=>within(v+(e.key==='ArrowLeft'?16:-16),230,420))}}}/>
   <aside className="v4-details"><div className="v4-detail-head"><strong>文件详情</strong><button className="btn quiet" onClick={()=>setDetails(false)}>关闭</button></div>
    {preview?<div className="v4-detail-content"><span className="v4-detail-icon"><FileGlyph name={preview.name} isDir={preview.isDir} variant="detail"/></span><h3>{preview.name}</h3>
     <div className="v4-detail-line"><span>类型</span><b>{fileIconLabel(preview.name,preview.isDir)}{!preview.isDir&&ext(preview.name)?' · '+ext(preview.name).toUpperCase():''}</b></div>
     <div className="v4-detail-line"><span>大小</span><b>{preview.isDir?'—':fmt(preview.size)}</b></div>
     <div className="v4-detail-line"><span>修改时间</span><b>{date(preview.modTime)}</b></div>
     <div className="v4-detail-line"><span>访问权限</span><b>{canWrite?'读写':'只读'}</b></div>
     <div className="v4-detail-actions">{!preview.isDir&&<button className="btn primary" onClick={()=>onDownload(preview)}>下载文件</button>}
      {canWrite&&<button className="btn" onClick={()=>requestRename(preview)}>重命名</button>}</div>
    </div>:<div className="v4-placeholder">选择文件查看详情</div>}</aside></>}
  {treeOpen&&<button className="v4-tree-shade" onClick={()=>setTreeOpen(false)} aria-label="关闭设备列表"/>}
  {menu&&<div className="file-v4-context" ref={menuRef} role="menu"
    style={{left:menu.x,top:menu.y}}>{itemsForMenu().map(([key,title])=>
    <button key={key} className={key==='remove'?'danger':''} role="menuitem" onClick={()=>{
      if(menu.kind==='tree'){const target=menu.target;setMenu(null);
       if(key==='open')navigate(target.device,target.share,target.path);
       else if(key==='refresh')onDirectory(target.device,target.share,target.path,{force:true});
       else if(key==='upload'){uploadTarget.current=target;input.current?.click()}
       else if(key==='mkdir'){requestCreate(target.path,target)}
      }else void action(key,menu.item,menu.target,menu.targets);
    }}>{title}</button>)}</div>}
  {dialog&&<div className="v4-dialog-backdrop" onMouseDown={e=>e.target===e.currentTarget&&setDialog(null)}>
   <div className="v4-dialog" role="dialog" aria-modal="true">
    <h3>{({mkdir:'新建文件夹',rename:'重命名',move:'移动文件',remove:'确认删除'})[dialog.kind]}</h3>
    {dialog.kind==='remove'?<p>将删除所选 {dialog.items.length} 项。非空文件夹无法删除，删除操作不可撤销。</p>:
     <><p>{dialog.kind==='move'?'请输入同一共享下的目标相对目录（根目录使用 .）':'请输入新名称'}</p>
      <input autoFocus value={dialog.value||''} onChange={e=>setDialog(d=>({...d,value:e.target.value}))} onKeyDown={e=>e.key==='Enter'&&void confirmDialog()}/></>}
    <div className="v4-dialog-actions"><button className="btn" onClick={()=>setDialog(null)}>取消</button>
     <button className={'btn '+(dialog.kind==='remove'?'danger':'primary')} onClick={()=>void confirmDialog()}>{dialog.kind==='remove'?'确认删除':'确定'}</button></div>
   </div></div>}
 </section>;
}
