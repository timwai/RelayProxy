import React, {useCallback,useEffect,useRef,useState} from 'react';
import {call,callJSON} from './bridge.js';
import {Icon} from './icons.jsx';
import {createDirectoryCache} from './fileBrowserCache.js';
import './fileSharing.css';

const formatFileBytes=n=>{const num=Math.max(0,Number(n)||0);if(num<1024)return num.toFixed(0)+' B';const units=['KiB','MiB','GiB','TiB'];const power=Math.min(4,Math.floor(Math.log(num)/Math.log(1024)));return (num/1024**power).toFixed(1)+' '+units[power-1]};
const blank=()=>({id:'',name:'',rootPath:'',enabled:true,permissions:{}});
const empty=()=>({revision:0,enabled:false,directUpnpEnabled:false,shares:[]});
const upnpStateLabel={disabled:'已关闭',not_approved:'等待 Server 审批',awaiting_request:'等待文件直连请求',discovering:'正在发现路由器',mapped:'已取得公网候选地址',private_wan:'网关 WAN 为内网或 CGNAT',unavailable:'映射不可用'};
const countPermissions=share=>Object.values(share.permissions||{}).filter(v=>v==='read'||v==='read_write').length;

export default function FileSharingPage({toast}) {
 const [state,setState]=useState(null);
 const [tab,setTab]=useState('local');
 const [directStatus,setDirectStatus]=useState(null);
 const [busy,setBusy]=useState(false);
 const [error,setError]=useState('');
 const [editor,setEditor]=useState(null);
 const [draft,setDraft]=useState(blank);
 const [device,setDevice]=useState('');
 const [permission,setPermission]=useState('read');
 const [candidates,setCandidates]=useState([]);
 const [candidateError,setCandidateError]=useState('');
 const [folderPickerError,setFolderPickerError]=useState('');
 const [candidatesLoading,setCandidatesLoading]=useState(false);
 const [peers,setPeers]=useState([]);
 const [remoteDevice,setRemoteDevice]=useState('');
 const [remoteShares,setRemoteShares]=useState([]);
 const [remoteShare,setRemoteShare]=useState('');
 const [remotePath,setRemotePath]=useState('.');
 const [remoteEntries,setRemoteEntries]=useState([]);
 const [remoteLoading,setRemoteLoading]=useState(false);
 const [remoteError,setRemoteError]=useState('');
 const [fileQuery,setFileQuery]=useState('');
 const [fileSort,setFileSort]=useState('name');
 const [viewMode,setViewMode]=useState('list');
 const [selectedFile,setSelectedFile]=useState(null);
 const saveHandles=useRef(new Map());
 const completedSaves=useRef(new Set());
 const completedUploads=useRef(new Set());
 const peersRef=useRef([]);
 const directoryCache=useRef(createDirectoryCache());
 const directoryInflight=useRef(new Map());
 const navigationSerial=useRef(0);
 const [browseLatency,setBrowseLatency]=useState('');

 const [transfers,setTransfers]=useState([]);
 const [transferError,setTransferError]=useState('');
 const uploadCancelRef=useRef(false);
 const [uploading,setUploading]=useState(false);
 const [uploadStaged,setUploadStaged]=useState(0);
 const [uploadTotal,setUploadTotal]=useState(0);
 const canUpload=remoteShares.some(s=>s.id===remoteShare&&s.permission==='read_write');
 const currentShare=remoteShares.find(s=>s.id===remoteShare);
 const visibleFiles=remoteEntries.filter(e=>e.name.toLowerCase().includes(fileQuery.toLowerCase())).sort((a,b)=>{
  if(a.isDir!==b.isDir)return a.isDir?-1:1;
  if(fileSort==='size')return (b.size||0)-(a.size||0)||a.name.localeCompare(b.name);
  if(fileSort==='time')return (b.modTime||0)-(a.modTime||0)||a.name.localeCompare(b.name);
  return a.name.localeCompare(b.name,undefined,{numeric:true,sensitivity:'base'});
 });
 const activeDownloads=transfers.filter(t=>t.state==='running').length;
 async function uploadSelectedFile(event){
  const file=event.target.files?.[0];
  event.target.value='';
  if(!file||!remoteDevice||!remoteShare||uploading)return;
  if(!canUpload){setRemoteError('共享目录没有写入权限');return}
  if(file.size>20*1024**3){setRemoteError('单文件上传不能超过 20 GiB');return}
  uploadCancelRef.current=false;
  setUploading(true);setUploadStaged(0);setUploadTotal(file.size);setRemoteError('');
  let sessionId='';
  try{
   const destination=remotePath==='.'?file.name:remotePath+'/'+file.name;
   const stage=await callJSON('goBeginFileUpload',null,remoteDevice,remoteShare,destination,file.size);
   if(!stage?.id)throw new Error('未能建立文件上传任务');
   sessionId=stage.id;
   const chunkSize=128*1024;
   for(let offset=0;offset<file.size;offset+=chunkSize){
    if(uploadCancelRef.current)throw new Error('已取消上传准备');
    const slice=file.slice(offset,Math.min(offset+chunkSize,file.size));
    const encoded=await new Promise((resolve,reject)=>{
     const reader=new FileReader();
     reader.onerror=()=>reject(reader.error||new Error('读取本地文件失败'));
     reader.onload=()=>resolve(String(reader.result||'').split(',')[1]||'');
     reader.readAsDataURL(slice);
    });
    const result=await callJSON('goAppendFileUploadChunk',null,sessionId,encoded);
    if(!result||typeof result.done!=='number')throw new Error('上传暂存失败');
    setUploadStaged(result.done);
    if(uploadCancelRef.current)throw new Error('已取消上传准备');
   }
   if(uploadCancelRef.current)throw new Error('已取消上传准备');
   const task=await callJSON('goCommitFileUpload',null,sessionId);
   if(!task?.id)throw new Error('未能开始远程上传');
   sessionId='';
   // Upload may alter any cached listing for this host; the next browse
   // must re-fetch with its current permission and directory contents.
   directoryCache.current.clearDevice(remoteDevice);
   toast?.('上传任务已提交，正在将文件传输到远程设备');
   setTab('transfers');
  }catch(e){
   setRemoteError(e.message||String(e));
   if(sessionId)try{await callJSON('goDiscardFileUpload',null,sessionId)}catch{}
  }finally{setUploading(false);uploadCancelRef.current=false}
 }

 const refreshDirectStatus=useCallback(async()=>{
  try{setDirectStatus(await callJSON('goGetFileDirectStatus',null))}
  catch{setDirectStatus(null)}
 },[]);
 useEffect(()=>{
  if(tab!=='local')return;
  void refreshDirectStatus();
  const timer=setInterval(()=>void refreshDirectStatus(),5000);
  return ()=>clearInterval(timer);
 },[tab,refreshDirectStatus]);
 const refreshPeers=useCallback(async()=>{
  try{
   const result=await callJSON('goGetFileSharePeers',[]);
   if(!Array.isArray(result))throw new Error('设备发现返回了无效数据');
   setRemoteError('');
   // Visibility is based on the host's current folder ACL, not just its
   // Server grant. Clear stale selected content after revocation.
   for(const oldPeer of peersRef.current){
    if(!result.some(peer=>peer.deviceId===oldPeer.deviceId)){
     directoryCache.current.clearDevice(oldPeer.deviceId);
     // Old slow requests for revoked shares are never reused.
     for(const key of directoryInflight.current.keys()){
      try{if(JSON.parse(key)[0]===oldPeer.deviceId)directoryInflight.current.delete(key)}catch{}
     }
    }
   }
   peersRef.current=result;
   setPeers(result);
   if(remoteDevice&&!result.some(peer=>peer.deviceId===remoteDevice)){
    navigationSerial.current+=1;
    setRemoteDevice('');setRemoteShare('');setRemoteShares([]);
    setRemoteEntries([]);setRemotePath('.');setSelectedFile(null);setRemoteLoading(false);
   }
  }catch(e){
   // An unavailable ACL check must not leave previously visible hosts
   // advertised as accessible.
   navigationSerial.current+=1;
   directoryCache.current.clear();
   directoryInflight.current.clear();
   peersRef.current=[];
   setPeers([]);setRemoteDevice('');setRemoteShare('');
   setRemoteShares([]);setRemoteEntries([]);setRemotePath('.');setRemoteLoading(false);
   setRemoteError(e.message||String(e));
  }
 },[remoteDevice]);
 const refreshTransfers=useCallback(async()=>{
  try{
   const result=await callJSON('goGetFileTransfers',[]);
   const tasks=Array.isArray(result)?result:[];
   setTransfers(tasks);
   for(const task of tasks){
    if(task.direction==='upload'&&task.state==='completed'&&!completedUploads.current.has(task.id)){
     completedUploads.current.add(task.id);
     directoryCache.current.clearDevice(task.deviceId);
    }
    if(task.direction!=='download'||task.state!=='completed'||completedSaves.current.has(task.id))continue;
    const entry=saveHandles.current.get(task.id);
    if(!entry)continue;
    completedSaves.current.add(task.id);
    saveHandles.current.delete(task.id);
    void (async()=>{
     try{
      const url='/api/file-transfers/'+encodeURIComponent(task.id)+'/file';
      if(entry==='browser'){
       // Native browser attachment download streams to disk; do not Blob a
       // 20 GiB file into renderer memory just to trigger Save As.
       const anchor=document.createElement('a');anchor.href=url;anchor.download=task.fileName;document.body.append(anchor);anchor.click();anchor.remove();
      }else{
       const response=await fetch(url,{credentials:'same-origin',cache:'no-store'});
       if(!response.ok||!response.body)throw new Error('无法从 Agent 获取已验证文件');
       const writable=await entry.createWritable();
       try{await response.body.pipeTo(writable)}catch(err){try{await writable.abort()}catch{}throw err}
      }
     }catch(err){setTransferError('保存文件失败：'+(err.message||String(err)))}
    })();
   }
  }catch(e){setTransferError(e.message||String(e))}
 },[]);
 useEffect(()=>{
  if(tab!=='remote')return;
  void refreshPeers();
  const timer=setInterval(()=>void refreshPeers(),15000);
  return ()=>clearInterval(timer);
 },[tab,refreshPeers]);
 useEffect(()=>{
  if(tab==='transfers')void refreshTransfers();
  // A browser-selected Save As handle must still be fulfilled when the
  // user switches between the remote explorer and transfers tabs.
  const timer=setInterval(()=>{
   if(tab==='transfers'||saveHandles.current.size>0)void refreshTransfers();
  },900);
  return ()=>clearInterval(timer);
 },[tab,refreshTransfers]);
 async function openRemoteDevice(id){
  const seq=++navigationSerial.current;
  setRemoteDevice(id);setRemoteShare('');setRemotePath('.');setRemoteEntries([]);setSelectedFile(null);setFileQuery('');
  setRemoteError('');setBrowseLatency('');
  const cached=directoryCache.current.get(id,'','');
  if(cached){
   setRemoteShares(cached);setRemoteLoading(false);setBrowseLatency('缓存');
   return;
  }
  setRemoteLoading(true);
  const start=performance.now();
  try{
   const result=await callJSON('goGetRemoteFileShares',[],id);
   if(!Array.isArray(result))throw new Error('未能读取设备共享目录');
   if(seq!==navigationSerial.current)return;
   directoryCache.current.set(id,'','',result);
   setRemoteShares(result);setBrowseLatency(Math.round(performance.now()-start)+' ms');
  }catch(e){
   if(seq!==navigationSerial.current)return;
   setRemoteError(e.message||String(e));setRemoteShares([]);
  }finally{
   if(seq===navigationSerial.current)setRemoteLoading(false);
  }
 }
 async function openRemoteDir(deviceID,shareID,dir,{force=false}={}){
  const seq=++navigationSerial.current;
  setRemoteDevice(deviceID);setRemoteShare(shareID);setRemotePath(dir);
  setSelectedFile(null);setFileQuery('');setRemoteError('');setBrowseLatency('');
  const cached=force?null:directoryCache.current.get(deviceID,shareID,dir);
  if(cached){
   setRemoteEntries(cached);setRemoteLoading(false);setBrowseLatency('缓存');
   return;
  }
  setRemoteEntries([]);setRemoteLoading(true);
  const start=performance.now();
  const key=JSON.stringify([deviceID,shareID,dir]);
  let pending=force?null:directoryInflight.current.get(key);
  if(!pending){
   pending=callJSON('goGetRemoteFileEntries',[],deviceID,shareID,dir);
   directoryInflight.current.set(key,pending);
   void pending.finally(()=>{
    if(directoryInflight.current.get(key)===pending)directoryInflight.current.delete(key);
   }).catch(()=>{});
  }
  try{
   const result=await pending;
   if(!Array.isArray(result))throw new Error('未能读取文件列表');
   if(seq!==navigationSerial.current)return; // Slow prior requests cannot replace the active folder.
   directoryCache.current.set(deviceID,shareID,dir,result);
   setRemoteEntries(result);setBrowseLatency(Math.round(performance.now()-start)+' ms');
  }catch(e){
   if(seq!==navigationSerial.current)return;
   directoryCache.current.delete(deviceID,shareID,dir);
   setRemoteError(e.message||String(e));setRemoteEntries([]);
  }finally{
   if(seq===navigationSerial.current)setRemoteLoading(false);
  }
 }
 async function beginDownload(entry){
  const relativePath=remotePath==='.'?entry.name:remotePath+'/'+entry.name;
  setRemoteError('');
  try{
   let task;
   const nativeWails=!!globalThis.wails?.Call?.ByName;
   const macBrowser=!nativeWails&&/Macintosh|Mac OS X/i.test(navigator.userAgent||'');
   if(nativeWails||macBrowser){
    // The Wails / local macOS bridge opens the host OS Save As dialog and
    // directly creates a scoped transfer task only after user selection.
    try{
     task=await callJSON('goChooseAndStartFileDownload',null,remoteDevice,remoteShare,relativePath);
     if(task?.cancelled||!task?.id)return;
    }catch(e){
     if(nativeWails||!/not implemented|unavailable|HTTP 403|HTTP 501/i.test(e.message||''))throw e;
    }
   }
   if(!task){
    // Browser file-system handles cannot disclose the user's absolute path
    // to Agent. Browser supplies the real OS picker and receives verified
    // bytes only after Agent's existing completion + SHA-256 verification.
    let saveHandle='browser';
    if(typeof window.showSaveFilePicker==='function'){
     try{saveHandle=await window.showSaveFilePicker({suggestedName:entry.name})}
     catch(e){if(e.name==='AbortError')return;throw e}
    }
    task=await callJSON('goStartFileDownload',null,remoteDevice,remoteShare,relativePath);
    if(!task?.id)throw new Error('未能创建下载任务');
    saveHandles.current.set(task.id,saveHandle);
   }
   toast?.('下载任务已创建');
   setTab('transfers');
  }catch(e){setRemoteError(e.message||String(e))}
 }
 async function chooseShareFolder(){
  try{
   const selected=await call('goChooseShareFolder');
   const value=typeof selected==='string'?selected:selected?.path;
   if(!value)return;
   const name=value.replace(/[\\/]+$/,'').split(/[\\/]/).pop();
   setFolderPickerError('');setDraft(s=>({...s,rootPath:value,name:s.name||name||''}));
  }catch(e){setFolderPickerError('系统文件夹选择不可用：'+(e.message||String(e)))}
 }
 async function cancelTransfer(id){
  try{await callJSON('goCancelFileDownload',null,id);await refreshTransfers()}
  catch(e){setTransferError(e.message||String(e))}
 }
 async function resumeDownload(id){
  const item=transfers.find(t=>t.id===id);
  if(!item)return;
  try{
   const nativeWails=!!globalThis.wails?.Call?.ByName;
   const macBrowser=!nativeWails&&/Macintosh|Mac OS X/i.test(navigator.userAgent||'');
   if(item.requiresSaveAs&&(nativeWails||macBrowser)){
    const result=await callJSON('goChooseAndResumeFileDownload',null,id);
    if(!result||result.cancelled||result===false)return;
   }else{
    if(!nativeWails){
     let handle='browser';
     if(typeof window.showSaveFilePicker==='function'){
      try{handle=await window.showSaveFilePicker({suggestedName:item.fileName})}
      catch(e){if(e.name==='AbortError')return;throw e}
     }
     saveHandles.current.set(id,handle);
     completedSaves.current.delete(id);
    }
    await callJSON('goResumeFileDownload',null,id);
   }
   await refreshTransfers();
  }catch(e){setTransferError(e.message||String(e))}
 }

 const refresh=useCallback(async()=>{
  try{
   const next=await callJSON('goGetFileShares',null);
   if(!next||!Array.isArray(next.shares))throw new Error('无效的共享配置响应');
   setState(next);setError('');
  }catch(e){setError(e.message||String(e))}
 },[]);
 useEffect(()=>{void refresh()},[refresh]);

 async function commit(patch) {
  if(!state||busy)return false;
  setBusy(true);
  try{
   const next=typeof patch==='function'?patch(state):patch;
   const saved=await callJSON('goSaveFileShares',null,next);
   if(!saved||!Array.isArray(saved.shares))throw new Error('保存共享配置失败');
   setState(saved);
   setError('');
   toast?.('文件共享设置已保存');
   return true;
  }catch(e){
   setError(e.message||String(e));
   if(/changed|revision|重新载入|冲突/i.test(e.message||''))void refresh();
   return false;
  }finally{setBusy(false)}
 }
 async function loadCandidates(){
  setCandidatesLoading(true);setCandidateError('');
  try{
   const result=await callJSON('goGetFileShareCandidates',[]);
   if(!Array.isArray(result))throw new Error('设备列表响应无效');
   setCandidates(result);
  }catch(e){setCandidates([]);setCandidateError(e.message||String(e))}
  finally{setCandidatesLoading(false)}
 }
 function edit(share){
  void loadCandidates();
  setFolderPickerError('');
  setEditor(share?'edit':'new');
  setDraft(share?{...share,permissions:{...share.permissions}}:blank());
  setDevice('');setPermission('read');
 }
 function addPermission(){
  const id=device.trim();
  if(!id||!candidates.some(item=>item.deviceId===id))return;
  setDraft(s=>({...s,permissions:{...(s.permissions||{}),[id]:permission}}));
  setDevice('');
 }
 async function saveEditor(){
  const item={...draft,name:draft.name.trim(),rootPath:draft.rootPath.trim()};
  if(!item.name||!item.rootPath){setError('共享名称和绝对文件夹路径不能为空');return}
  const ok=await commit(s=>({...s,shares:editor==='new'?[...s.shares,item]:s.shares.map(v=>v.id===item.id?item:v)}));
  if(ok)setEditor(null);
 }
 async function remove(share){
  if(!window.confirm('移除“'+share.name+'”的共享配置？磁盘文件不会被删除。'))return;
  await commit(s=>({...s,shares:s.shares.filter(v=>v.id!==share.id)}));
 }
 const shares=state?.shares||[];
 const active=shares.filter(s=>s.enabled).length;
 return <>
  <div className="page-head"><div><h1>文件共享</h1><p>在授权设备之间共享本地文件夹，按设备分别控制读写权限。</p></div><div className="actions"><button className="btn" onClick={refresh} disabled={busy}>刷新</button>{tab==='local'&&<button className="btn primary" onClick={()=>edit(null)} disabled={!state||busy}>＋ 添加共享文件夹</button>}</div></div>
  {error&&<div className="notice danger file-share-error" role="alert">{error}</div>}
  <div className="file-share-tabs" role="tablist" aria-label="文件共享页面">
   {[[ 'local','我的共享'],['remote','其他设备'],['transfers','传输任务']].map(([id,title])=><button type="button" key={id} role="tab" aria-selected={tab===id} className={tab===id?'selected':''} onClick={()=>setTab(id)}>{title}</button>)}
  </div>
  {tab==='local'&&<>
   <div className="cards-3">
    <div className="metric"><div className="mini">共享文件夹</div><div className="metric-value">{shares.length}</div><div className="mini">{active} 个已启用</div></div>
    <div className="metric"><div className="mini">设备权限</div><div className="metric-value">{shares.reduce((n,s)=>n+countPermissions(s),0)}</div><div className="mini">各共享文件夹独立授权</div></div>
    <div className="metric"><div className="mini">共享服务</div><div className="metric-value" style={{fontSize:19}}>{state?.enabled?'已开启':'已关闭'}</div><div className="mini">初始默认关闭</div></div>
   </div>
   <section className="card">
    <div className="card-head"><div><div className="eyebrow">FILE SHARING</div><h2>本机文件共享</h2></div>
    <button type="button" role="switch" aria-label="启用文件共享" aria-checked={!!state?.enabled} className={'switch '+(state?.enabled?'on':'')} disabled={!state||busy} onClick={()=>commit(s=>({...s,enabled:!s.enabled}))}><span/></button></div>
    <div className="mini">开启后仍需在 Server 审批文件能力，并为每个共享目录明确授权具体设备；未授权设备不能访问。</div>
   </section>
   <section className="card">
    <div className="card-head"><div><div className="eyebrow">OPTIONAL · PUBLIC TCP / QUIC</div><h2>文件直连端口映射</h2></div>
    <button type="button" role="switch" aria-label="允许文件专用 TCP 与 QUIC UPnP 映射" aria-checked={!!state?.directUpnpEnabled} className={'switch '+(state?.directUpnpEnabled?'on':'')} disabled={!state||busy} onClick={()=>commit(s=>({...s,directUpnpEnabled:!s.directUpnpEnabled}))}><span/></button></div>
    <div className="mini">默认关闭。启用后尝试独立映射文件专用 TCP 与 QUIC/UDP 端口；只有公网可路由的 WAN 地址才会公布，仍须 Server 审批、一次性票据与 TLS 指纹校验。关闭后同时撤销两种映射。映射成功不代表跨运营商公网可达，也不代表完成 NAT 打洞。</div>
    {state?.directUpnpEnabled&&<><div className="file-share-upnp-status" role="status"><b>TCP</b><span>{!state?.enabled?'共享服务未开启':upnpStateLabel[directStatus?.mappingState]||'读取映射状态中…'}</span>{directStatus?.publicAddress&&<span className="mono">{directStatus.publicAddress}</span>}{directStatus?.reason&&<span className="mini" title={directStatus.reason}>{directStatus.reason}</span>}</div><div className="file-share-upnp-status" role="status"><b>QUIC / UDP</b><span>{!state?.enabled?'共享服务未开启':upnpStateLabel[directStatus?.quicMappingState]||'读取映射状态中…'}</span>{directStatus?.quicPublicAddress&&<span className="mono">{directStatus.quicPublicAddress}</span>}{directStatus?.quicReason&&<span className="mini" title={directStatus.quicReason}>{directStatus.quicReason}</span>}</div></>}
   </section>
   <section className="card"><div className="card-head"><div><div className="eyebrow">{shares.length} DIRECTORIES</div><h2>共享文件夹</h2></div></div>
   {!state&&<div className="empty">正在读取本机文件共享配置…</div>}
   {state&&shares.length===0&&<div className="empty">还没有共享文件夹。添加本机目录后，再为设备设置只读或读写权限。</div>}
   {shares.map(share=><div className="file-share-item" key={share.id}>
    <div className="file-share-folder">▱</div>
    <div className="grow file-share-detail"><b>{share.name}</b><div className="mono mini" title={share.rootPath}>{share.rootPath}</div><div className="mini">{countPermissions(share)} 台设备已授权 · {share.enabled?'目录已启用':'目录已停用'}</div></div>
    <span className={'badge '+(share.enabled?'ok':'neutral')}>{share.enabled?'已启用':'已停用'}</span>
    <button type="button" role="switch" aria-label={'启用 '+share.name} aria-checked={share.enabled} className={'switch '+(share.enabled?'on':'')} disabled={busy} onClick={()=>commit(s=>({...s,shares:s.shares.map(v=>v.id===share.id?{...v,enabled:!v.enabled}:v)}))}><span/></button>
    <div className="file-share-ops"><button className="btn" onClick={()=>edit(share)} disabled={busy}>编辑 / 权限</button><button className="btn danger" onClick={()=>remove(share)} disabled={busy}>移除</button></div>
   </div>)}
   </section>
  </>}
  {tab==='remote'&&<>
   {remoteError&&<div className="notice danger" role="alert">{remoteError}</div>}
   <section className="file-browser">
    <aside className="file-browser-sidebar">
     <div className="file-browser-aside-title"><span>已授权设备</span><button type="button" className="btn quiet" onClick={refreshPeers} title="刷新设备"><Icon name="activity"/></button></div>
     {peers.length===0&&<div className="file-browser-aside-empty">暂无在线共享设备。请先在 Server 审批文件访问能力。</div>}
     {peers.map(peer=><div key={peer.deviceId} className="file-browser-device-group">
      <button type="button" className={'file-browser-device '+(remoteDevice===peer.deviceId?'active':'')} onClick={()=>openRemoteDevice(peer.deviceId)}>
       <span className="file-browser-device-mark"><Icon name="devices" size={17}/></span>
       <span className="file-browser-device-label"><strong>{peer.name||peer.deviceId}</strong><small>{peer.deviceId}</small></span>
       <span className="file-browser-online-dot" title="在线"/>
      </button>
      {remoteDevice===peer.deviceId&&<div className="file-browser-shares">
       {remoteLoading&&!remoteShares.length&&<span className="mini">正在读取共享目录…</span>}
       {remoteShares.map(share=><button type="button" key={share.id} className={'file-browser-share '+(remoteShare===share.id?'active':'')} onClick={()=>openRemoteDir(remoteDevice,share.id,'.')}>
        <Icon name="folder"/><span>{share.name}</span><small>{share.permission==='read_write'?'读写':'只读'}</small>
       </button>)}
       {!remoteLoading&&remoteShares.length===0&&<div className="mini">未授权共享文件夹</div>}
      </div>}
     </div>)}
     <div className="file-browser-aside-bottom"><Icon name="shield" size={16}/><span>共享目录权限由设备与 Server 双重校验</span></div>
    </aside>
    <div className="file-browser-content">
     <div className="file-browser-header">
      <div><div className="eyebrow">REMOTE FILE EXPLORER</div><h2>{currentShare?.name||'远程文件浏览器'}</h2><div className="mini">{remoteDevice?(peers.find(p=>p.deviceId===remoteDevice)?.name||remoteDevice):'选择设备并打开已授权的共享文件夹'}</div></div>
      <div className="file-share-upload-actions">
       {remoteShare&&canUpload&&<label className={'btn primary '+(uploading?'disabled':'')} title="使用系统文件选择器上传文件"><Icon name="folder"/> 上传文件<input aria-label="选择要上传的文件" type="file" disabled={uploading||remoteLoading} onChange={uploadSelectedFile} style={{display:'none'}}/></label>}
       {remoteShare&&<button className="btn" onClick={()=>openRemoteDir(remoteDevice,remoteShare,remotePath,{force:true})} disabled={remoteLoading||uploading}><Icon name="activity"/> 刷新</button>}
      </div>
     </div>
     {remoteShare&&<div className="file-browser-tools">
      <nav aria-label="文件夹路径" className="file-browser-crumbs">
       <button type="button" onClick={()=>openRemoteDir(remoteDevice,remoteShare,'.')} disabled={remoteLoading}><Icon name="folder"/> {currentShare?.name||'根目录'}</button>
       {remotePath!=='.'&&remotePath.split('/').map((part,i,parts)=><React.Fragment key={i}><span className="file-browser-separator">/</span><button type="button" onClick={()=>openRemoteDir(remoteDevice,remoteShare,parts.slice(0,i+1).join('/'))} disabled={remoteLoading}>{part}</button></React.Fragment>)}
      </nav>
      <div className="file-browser-filters">
       <input aria-label="搜索当前目录" className="control file-browser-search" type="search" placeholder="搜索此文件夹…" value={fileQuery} onChange={e=>setFileQuery(e.target.value)}/>
       <select aria-label="排序方式" className="control file-browser-sort" value={fileSort} onChange={e=>setFileSort(e.target.value)}><option value="name">按名称</option><option value="size">按大小</option><option value="time">按时间</option></select>
       <button className="btn" aria-label="切换文件视图" title="切换列表／网格" onClick={()=>setViewMode(v=>v==='list'?'grid':'list')}>{viewMode==='list'?'▦':'☰'}</button>
      </div>
     </div>}
     {uploading&&<div className="file-share-upload-staging" role="status"><div className="file-share-upload-staging-title"><span>上传准备：{formatFileBytes(uploadStaged)} / {formatFileBytes(uploadTotal)} · {uploadTotal?Math.floor(uploadStaged/uploadTotal*100):100}%</span><button type="button" className="btn danger" onClick={()=>{uploadCancelRef.current=true}}>取消</button></div><div className="file-share-task-track"><div style={{width:(uploadTotal?Math.min(100,uploadStaged/uploadTotal*100):100)+'%'}}/></div></div>}
     {!remoteShare?<div className="file-browser-placeholder"><span className="file-browser-placeholder-icon"><Icon name="folder" size={34}/></span><h3>选择设备与共享文件夹</h3><p>左侧显示 Server 已批准且在线的文件共享设备。</p></div>:
     <>
      <div className="file-browser-table-head"><span>名称</span><span>大小</span><span>修改时间</span><span>操作</span></div>
      {remoteLoading&&<div className="empty">正在加载文件…</div>}
      {!remoteLoading&&visibleFiles.length===0&&<div className="empty">{fileQuery?'没有匹配的文件':'此文件夹为空'}</div>}
      {!remoteLoading&&<div className={'file-browser-files '+(viewMode==='grid'?'grid-view':'list-view')}>
       {visibleFiles.map(entry=><div key={entry.name} className={'file-browser-file '+(selectedFile===entry.name?'selected':'')} onClick={()=>entry.isDir?openRemoteDir(remoteDevice,remoteShare,remotePath==='.'?entry.name:remotePath+'/'+entry.name):setSelectedFile(entry.name)}>
        <span className={'file-browser-file-icon '+(entry.isDir?'directory':'document')}><Icon name={entry.isDir?'folder':'file'} size={19}/></span>
        <span className="file-browser-file-name"><strong title={entry.name}>{entry.name}</strong><small>{entry.isDir?'文件夹':'文件'}</small></span>
        <span className="file-browser-file-size">{entry.isDir?'—':formatFileBytes(entry.size)}</span>
        <span className="file-browser-file-date">{entry.modTime?new Date(entry.modTime>1e12?entry.modTime:entry.modTime*1000).toLocaleDateString():'—'}</span>
        <button type="button" className={'btn '+(entry.isDir?'':'primary')} onClick={e=>{e.stopPropagation();entry.isDir?openRemoteDir(remoteDevice,remoteShare,remotePath==='.'?entry.name:remotePath+'/'+entry.name):beginDownload(entry)}} disabled={remoteLoading}>{entry.isDir?'打开':'下载'}</button>
       </div>)}
      </div>}
      <div className="file-browser-status"><span>{visibleFiles.length} 个项目 · {currentShare?.permission==='read_write'?'可读取和上传':'只读访问'}</span><span>{browseLatency?'目录读取 '+browseLatency+' · ':''}{activeDownloads?'正在传输 '+activeDownloads+' 项 · ':''}传输采用已授权的加密通道</span></div>
     </>}
    </div>
   </section>
  </>}
  {tab==='transfers'&&<section className="card">
   <div className="card-head"><div><div className="eyebrow">TRANSFERS</div><h2>传输任务</h2></div><button className="btn" onClick={refreshTransfers}>刷新</button></div>
   {transferError&&<div className="notice danger" role="alert">{transferError}</div>}
   <div className="mini">下载保存至本机 Downloads/RelayProxy；上传到共享端当前目录。优先尝试文件专用 QUIC 直连，其次 TCP/TLS，均不可用时回退 Relay；不要求授予代理或 RDP 权限。</div>
   {transfers.length===0?<div className="empty">暂无传输任务。在“其他设备”中选择文件下载，或在读写目录内上传文件。</div>:
    transfers.map(item=><div className="file-share-task" key={item.id}>
     <div className="file-share-task-title"><div className="grow"><b>{item.direction==='upload'?'上传 · ':'下载 · '}{item.fileName}</b><div className="mini mono">{item.deviceId} · {item.direction==='upload'?'远程共享文件夹':item.destination||'下载中'}</div></div><span className={'badge '+(item.state==='completed'?'ok':item.state==='failed'?'danger':'neutral')}>{({'running':item.direction==='upload'?'上传中':'下载中','completed':'已完成','failed':'失败','cancelled':'已取消','paused':'已暂停'})[item.state]||item.state}</span></div>
     <div className="file-share-task-track"><div style={{width:(item.total>0?Math.min(100,item.done/item.total*100):0)+'%'}}/></div>
     <div className="file-share-task-meta"><span>{formatFileBytes(item.done)} / {item.total>0?formatFileBytes(item.total):'计算中'} · {item.total>0?Math.floor(item.done/item.total*100):0}%</span><span>{item.transportPath==='p2p_quic'?'P2P QUIC':item.transportPath==='direct_quic'?'直连 QUIC':item.transportPath=== 'direct_tls'?'直连 TLS':item.transportPath==='relay_quic'?'Relay QUIC':item.transportPath==='relay_tls'?'Relay TLS':'协商中'}{item.state==='running'?' · '+formatFileBytes(item.speed)+'/s':''}</span></div>
     {item.error&&<div className="notice danger">{item.error}</div>}
     {item.direction==='download'&&item.state==='completed'&&!globalThis.wails?.Call?.ByName&&<button type="button" className="btn" onClick={()=>{
       if(globalThis.wails?.Call?.ByName){toast?.(item.destination||'文件已保存到选择的位置');return}
       const link=document.createElement('a');
       link.href='/api/file-transfers/'+encodeURIComponent(item.id)+'/file';
       link.download=item.fileName;document.body.append(link);link.click();link.remove();
     }}>保存到浏览器下载目录</button>}
     {item.state==='running'&&<button className="btn danger" onClick={()=>cancelTransfer(item.id)}>{item.direction==='download'?'暂停下载':'取消上传'}</button>}
     {item.direction==='download'&&(item.state==='paused'||item.state==='failed')&&<button className="btn primary" onClick={()=>resumeDownload(item.id)}>继续下载</button>}
    </div>)}
  </section>}
  {editor&&<div className="overlay show" onMouseDown={e=>{if(e.target===e.currentTarget&&!busy)setEditor(null)}}>
   <div className="modal wide" role="dialog" aria-modal="true" aria-label={editor==='new'?'添加共享文件夹':'编辑共享文件夹'}>
    <div className="modal-head"><h2>{editor==='new'?'添加共享文件夹':'编辑共享与设备权限'}</h2><button className="close" aria-label="关闭" onClick={()=>setEditor(null)} disabled={busy}>×</button></div>
    <div className="modal-body">
     <div className="form-grid">
      <label className="field"><span>共享名称</span><input className="control" value={draft.name} maxLength={100} onChange={e=>setDraft(s=>({...s,name:e.target.value}))} placeholder="例如：工作资料"/></label>
      <div className="field"><span>本机共享文件夹</span><div className="file-share-folder-input"><input className="control" value={draft.rootPath} onChange={e=>setDraft(s=>({...s,rootPath:e.target.value}))} placeholder="点击浏览选择文件夹，或输入绝对路径"/><button type="button" className="btn" onClick={chooseShareFolder} disabled={busy}><Icon name="folder"/> 浏览…</button></div><small className="mini">选择的是运行 Agent 的设备上的文件夹。远程访问管理页时须手动输入 Agent 主机路径。</small>{folderPickerError&&<div className="notice danger" role="alert">{folderPickerError}</div>}</div>
     </div>
     <div className="file-share-permissions"><h3>设备权限</h3><p className="mini">默认不授权。只显示 Server 已审批、同身份且具备文件访问能力的设备；离线设备也可预先设置权限。</p>
      {candidateError&&<div className="notice danger" role="alert">{candidateError}</div>}
      {candidatesLoading&&<div className="mini">正在读取 Server 已授权设备…</div>}
      {Object.entries(draft.permissions||{}).map(([id,perm])=><div className="file-share-permission" key={id}><span className="grow file-share-grantee"><b>{candidates.find(item=>item.deviceId===id)?.name||'历史授权设备'}</b><small className="mono">{id}</small></span><select className="control" value={perm} onChange={e=>setDraft(s=>({...s,permissions:{...s.permissions,[id]:e.target.value}}))}><option value="read">只读</option><option value="read_write">读写</option></select><button className="btn danger" onClick={()=>setDraft(s=>{const permissions={...s.permissions};delete permissions[id];return {...s,permissions}})}>移除</button></div>)}
      <div className="file-share-permission"><select aria-label="选择已审批的访问设备" className="control" value={device} disabled={candidatesLoading||!candidates.length} onChange={e=>setDevice(e.target.value)}><option value="">选择已审批的设备…</option>{candidates.filter(item=>!draft.permissions?.[item.deviceId]).map(item=><option key={item.deviceId} value={item.deviceId}>{item.name||item.deviceId} · {item.online?'在线':'离线'} · {item.deviceId}</option>)}</select><select className="control" value={permission} onChange={e=>setPermission(e.target.value)}><option value="read">只读</option><option value="read_write">读写</option></select><button className="btn" onClick={addPermission} disabled={!device||!candidates.some(item=>item.deviceId===device)}>添加权限</button></div>
     </div>
    </div>
    <div className="modal-foot"><button className="btn" onClick={()=>setEditor(null)} disabled={busy}>取消</button><button className="btn primary" disabled={busy} onClick={saveEditor}>{busy?'保存中…':'保存共享'}</button></div>
   </div>
  </div>}
 </>;
}
