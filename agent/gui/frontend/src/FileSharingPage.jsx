import React, {useCallback,useEffect,useRef,useState} from 'react';
import {createPortal} from 'react-dom';
import {call,callJSON} from './bridge.js';
import {Icon} from './icons.jsx';
import {createDirectoryCache} from './fileBrowserCache.js';
import {onlyVerifiedFilePeers} from './fileShareVisibility.js';
import './fileSharing.css';
import FileBrowserV4 from './FileBrowserV4.jsx';
import FileGlyph from './FileGlyph.jsx';
import useDialogFocus from './useDialogFocus.js';

const formatFileBytes=n=>{const num=Math.max(0,Number(n)||0);if(num<1024)return num.toFixed(0)+' B';const units=['KiB','MiB','GiB','TiB'];const power=Math.min(4,Math.floor(Math.log(num)/Math.log(1024)));return (num/1024**power).toFixed(1)+' '+units[power-1]};
const blank=()=>({id:'',name:'',rootPath:'',enabled:true,permissions:{}});
const empty=()=>({revision:0,enabled:false,directUpnpEnabled:false,shares:[]});
const upnpStateLabel={disabled:'已关闭',not_approved:'等待 Server 审批',awaiting_request:'等待文件直连请求',discovering:'正在发现路由器',mapped:'已取得公网候选地址',private_wan:'网关 WAN 为内网或 CGNAT',unavailable:'映射不可用'};
const countPermissions=share=>Object.values(share.permissions||{}).filter(v=>v==='read'||v==='read_write').length;

export default function FileSharingPage({toast,initialDevice='',active=true,navigationTarget=null,transferOpen=false,onTransferOpenChange,onTransferSummary}) {
 const [state,setState]=useState(null);
 const [tab,setTab]=useState(initialDevice||new URLSearchParams(window.location.search).has('filesWindow')?'remote':'local');
 const [directStatus,setDirectStatus]=useState(null);
 const [warmStatus,setWarmStatus]=useState('');
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
 const [visibleLimit,setVisibleLimit]=useState(200);
 const [selectedFile,setSelectedFile]=useState(null);
 const saveHandles=useRef(new Map());
 const completedSaves=useRef(new Set());
 const completedUploads=useRef(new Set());
 const peersRef=useRef([]);
 const initialDeviceRef=useRef(initialDevice);
 const initialAutoSelected=useRef(false);
 const lastNavigationSerial=useRef(null);
 const drawerRef=useRef(null);
 useDialogFocus(transferOpen,drawerRef,()=>onTransferOpenChange?.(false));
 const peerRefreshSerial=useRef(0);
 const selectedRemote=useRef({device:'',share:''});
 const directoryCache=useRef(createDirectoryCache());
 const directoryInflight=useRef(new Map());
 const navigationSerial=useRef(0);
 const [browseLatency,setBrowseLatency]=useState('');
 const [treeRevision,setTreeRevision]=useState(0);
 const uploadBusyRef=useRef(false);
 const liveFolderRef=useRef({device:'',share:'',path:'.'});
 const refreshActiveDirRef=useRef(null);
 const initialWindowTarget=useRef((()=>{
  const params=new URLSearchParams(window.location.search);
  if(!params.has('filesWindow'))return null;
  const device=params.get('fileDevice'),share=params.get('fileShare'),path=params.get('filePath')||'.';
  if(!device||!share||path.startsWith('/')||path.includes('\\')||path.split('/').includes('..'))return null;
  return {device,share,path};
 })());
 useEffect(()=>{liveFolderRef.current={device:remoteDevice,share:remoteShare,path:remotePath}},[remoteDevice,remoteShare,remotePath]);
 useEffect(()=>{selectedRemote.current={device:remoteDevice,share:remoteShare}},[remoteDevice,remoteShare]);

 const [transfers,setTransfers]=useState([]);
 useEffect(()=>{onTransferSummary?.(transfers.filter(t=>t.state==='running').length,transfers.length)},[transfers,onTransferSummary]);
 const [transferError,setTransferError]=useState('');
 const uploadCancelRef=useRef(false);
 const [uploading,setUploading]=useState(false);
 const [uploadStaged,setUploadStaged]=useState(0);
 const [uploadTotal,setUploadTotal]=useState(0);
 // Preflight starts after the peer and share have been verified. Poll only
 // while a previous short-lived probe is in flight; never start file bytes.
 useEffect(()=>{
  const peer=peersRef.current.find(p=>p.deviceId===remoteDevice);
  if(!remoteDevice||!remoteShare||!peer?.shares?.some(x=>x.id===remoteShare)){
   setWarmStatus('');return;
  }
  let active=true,timer=null,attempts=0;
  setWarmStatus('直连预探测中');
  const probe=async()=>{
   try{
    const result=await callJSON('goPrewarmRemoteFileDirect',null,remoteDevice,remoteShare);
    if(!active)return;
    if(result?.state==='warming'){
     if(++attempts<14)timer=setTimeout(()=>void probe(),650);
     else setWarmStatus('直连预探测超时，可用 Relay');
     return;
    }
    if(result?.state==='ready'){
     const label=({'p2p_quic':'P2P QUIC','direct_quic':'QUIC','direct_tls':'TLS'})[result.path]||'直连';
     setWarmStatus(label+' 路径已预探测');
    }else setWarmStatus('Relay 回退可用');
   }catch(e){if(active)setWarmStatus('Relay 回退可用')}
  };
  void probe();
  return()=>{active=false;if(timer!==null)clearTimeout(timer)};
 },[remoteDevice,remoteShare]);
 const canUpload=remoteShares.some(s=>s.id===remoteShare&&s.permission==='read_write');
 const currentShare=remoteShares.find(s=>s.id===remoteShare);
 const visibleFiles=remoteEntries.filter(e=>e.name.toLowerCase().includes(fileQuery.toLowerCase())).sort((a,b)=>{
  if(a.isDir!==b.isDir)return a.isDir?-1:1;
  if(fileSort==='size')return (b.size||0)-(a.size||0)||a.name.localeCompare(b.name);
  if(fileSort==='time')return (b.modTime||0)-(a.modTime||0)||a.name.localeCompare(b.name);
  return a.name.localeCompare(b.name,undefined,{numeric:true,sensitivity:'base'});
 });
 const activeDownloads=transfers.filter(t=>t.state==='running').length;
 async function uploadOneFile(file,targetDir,targetShare,targetDevice,completed){
  if(file.size>20*1024**3)throw new Error('单文件上传不能超过 20 GiB');
  let sessionId='';
  try{
   const destination=targetDir==='.'?file.name:targetDir+'/'+file.name;
   const stage=await callJSON('goBeginFileUpload',null,targetDevice,targetShare,destination,file.size);
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
    setUploadStaged(completed+result.done);
   }
   if(uploadCancelRef.current)throw new Error('已取消上传准备');
   const task=await callJSON('goCommitFileUpload',null,sessionId);
   if(!task?.id)throw new Error('未能开始远程上传');
   sessionId='';
  }catch(e){
   if(sessionId)try{await callJSON('goDiscardFileUpload',null,sessionId)}catch{}
   throw e;
  }
 }

 async function uploadFiles(files,dir=remotePath,share=remoteShare,device=remoteDevice){
  const chosen=Array.from(files||[]);
  if(!chosen.length)return;
  if(uploadBusyRef.current)throw new Error('已有上传任务正在准备，请完成后重试');
  const authorized=peersRef.current.find(p=>p.deviceId===device)?.shares?.some(s=>s.id===share&&s.permission==='read_write');
  if(!authorized)throw new Error('目标共享没有写入权限');
  uploadBusyRef.current=true;
  uploadCancelRef.current=false;
  setUploading(true);setUploadStaged(0);
  setUploadTotal(chosen.reduce((bytes,file)=>bytes+file.size,0));
  setRemoteError('');
  let completed=0;
  try{
   for(const file of chosen){
    if(uploadCancelRef.current)throw new Error('已取消后续上传');
    await uploadOneFile(file,dir,share,device,completed);
    completed+=file.size;
    setUploadStaged(completed);
   }
   directoryCache.current.clearDevice(device);
   setTreeRevision(n=>n+1);
   toast?.('已提交 '+chosen.length+' 个上传任务');
   const live=liveFolderRef.current;
   if(live.device===device&&live.share===share&&live.path===dir)
    await openRemoteDir(device,share,dir,{force:true});
  }catch(e){setRemoteError(e.message||String(e));throw e}
  finally{
   uploadBusyRef.current=false;
   uploadCancelRef.current=false;
   setUploading(false);
  }
 }

 async function mutateRemote(action,src,dst='',scope=null){
  if(!['mkdir','move','remove'].includes(action))throw new Error('操作不受支持');
  const target=scope||liveFolderRef.current;
  const targetDevice=target.device,targetShare=target.share;
  if(!targetDevice||!targetShare||!peersRef.current.find(p=>p.deviceId===targetDevice)?.shares?.some(s=>s.id===targetShare&&s.permission==='read_write'))
   throw new Error('共享目录未授权写入');
  await callJSON('goMutateRemoteFile',null,targetDevice,targetShare,action,src,dst);
  directoryCache.current.clearDevice(targetDevice);
  setTreeRevision(n=>n+1);
  const live=liveFolderRef.current;
  if(live.device===targetDevice&&live.share===targetShare)
   await openRemoteDir(live.device,live.share,live.path,{force:true});
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
  const seq=++peerRefreshSerial.current;
  try{
   const raw=await callJSON('goGetFileSharePeers',[]);
   if(seq!==peerRefreshSerial.current)return;
   if(!Array.isArray(raw))throw new Error('设备发现返回了无效数据');
   // Never trust a files.host or Server peer list by itself. Only hosts
   // verified by the requesting Agent's live list_shares ACL query appear.
   // Old Agents lacking this proof fail closed.
   const result=onlyVerifiedFilePeers(raw);
   for(const oldPeer of peersRef.current){
    const updated=result.find(peer=>peer.deviceId===oldPeer.deviceId);
    if(!updated||JSON.stringify(updated.shares)!==JSON.stringify(oldPeer.shares)){
     directoryCache.current.clearDevice(oldPeer.deviceId);
     for(const key of directoryInflight.current.keys()){
      try{if(JSON.parse(key)[0]===oldPeer.deviceId)directoryInflight.current.delete(key)}catch{}
     }
    }
   }
   if(JSON.stringify(peersRef.current)!==JSON.stringify(result))setTreeRevision(n=>n+1);
   peersRef.current=result;
   setPeers(result);
   // Device Center deep link: only traverse this device if the current Agent
   // has verified at least one readable share. Unapproved hosts fail closed.
   const initialPeerID=initialDeviceRef.current;
   if(initialPeerID){
    initialDeviceRef.current='';
    const peer=result.find(x=>x.deviceId===initialPeerID);
    if(peer){
     setTab('remote');
     if(peer.shares.length===1){setRemoteShares(peer.shares);void openRemoteDir(peer.deviceId,peer.shares[0].id,'.')}
     else void openRemoteDevice(peer.deviceId);
    }else setRemoteError('该设备当前没有经过授权验证的可访问共享目录。');
   }
   const initial=initialWindowTarget.current;
   const authorizedPeer=initial&&result.find(p=>p.deviceId===initial.device);
   if(authorizedPeer?.shares?.some(x=>x.id===initial.share)){
    initialWindowTarget.current=null;
    setRemoteShares(authorizedPeer.shares);
    void openRemoteDir(initial.device,initial.share,initial.path);
   }
   // On first discovery, reveal a verified device's folder tree immediately.
   // Never silently switch devices after a later ACL revocation.
   if(!initialAutoSelected.current){
    if(initialPeerID||initial){initialAutoSelected.current=true}
    else if(!selectedRemote.current.device&&result.length>0){
     initialAutoSelected.current=true;
     void openRemoteDevice(result[0].deviceId);
    }
   }
   const selected=selectedRemote.current;
   if(selected.device){
    const current=result.find(peer=>peer.deviceId===selected.device);
    if(!current){
     navigationSerial.current+=1;
     selectedRemote.current={device:'',share:''};
     setRemoteDevice('');setRemoteShare('');setRemoteShares([]);
     setRemoteEntries([]);setRemotePath('.');setSelectedFile(null);setRemoteLoading(false);
    }else{
     setRemoteShares(current.shares);
     directoryCache.current.set(current.deviceId,'','',current.shares);
     if(selected.share&&!current.shares.some(share=>share.id===selected.share)){
      navigationSerial.current+=1;
      selectedRemote.current={device:current.deviceId,share:''};
      setRemoteShare('');setRemotePath('.');setRemoteEntries([]);
      setSelectedFile(null);setRemoteLoading(false);
     }
    }
   }
   if(!initialPeerID||result.some(peer=>peer.deviceId===initialPeerID))setRemoteError('');
  }catch(e){
   if(seq!==peerRefreshSerial.current)return;
   navigationSerial.current+=1;
   directoryCache.current.clear();
   directoryInflight.current.clear();
   peersRef.current=[];
   selectedRemote.current={device:'',share:''};
   setPeers([]);setRemoteDevice('');setRemoteShare('');
   setRemoteShares([]);setRemoteEntries([]);setRemotePath('.');setRemoteLoading(false);
   setRemoteError(e.message||String(e));
  }
 },[]);
 const refreshTransfers=useCallback(async()=>{
  try{
   const result=await callJSON('goGetFileTransfers',[]);
   const tasks=Array.isArray(result)?result:[];
   setTransfers(tasks);
   for(const task of tasks){
    if(task.direction==='upload'&&task.state==='completed'&&!completedUploads.current.has(task.id)){
     completedUploads.current.add(task.id);
     directoryCache.current.clearDevice(task.deviceId);
     setTreeRevision(n=>n+1);
     const live=liveFolderRef.current;
     const taskDir=task.relativePath?.includes('/')?task.relativePath.slice(0,task.relativePath.lastIndexOf('/')):'.';
     if(live.device===task.deviceId&&live.share===task.shareId&&live.path===taskDir)
      void refreshActiveDirRef.current?.(live.device,live.share,live.path,{force:true});
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
  if(!active)return;
  const requested=navigationTarget?.device;
  if(requested&&navigationTarget.serial!==lastNavigationSerial.current){
   lastNavigationSerial.current=navigationTarget.serial;
   initialDeviceRef.current=requested;
   initialAutoSelected.current=true;
   setTab('remote');
   void refreshPeers();
  }else if(!requested&&!new URLSearchParams(window.location.search).has('filesWindow')){
   setTab('local');
  }
 },[active,navigationTarget?.serial,refreshPeers]);
 useEffect(()=>{
  if(tab!=='remote'||!active)return;
  void refreshPeers();
  const timer=setInterval(()=>void refreshPeers(),15000);
  return ()=>{
   clearInterval(timer);
   peerRefreshSerial.current+=1; // discard responses after leaving the file browser
  };
 },[tab,active,refreshPeers]);
 useEffect(()=>{
  void refreshTransfers();
  // Keep the transfer owner mounted across all Agent pages: Save As handles
  // and upload-completion directory refresh survive route changes.
  const timer=setInterval(()=>void refreshTransfers(),1200);
  return ()=>clearInterval(timer);
 },[refreshTransfers]);
 async function openRemoteDevice(id){
  const verified=peersRef.current.find(peer=>peer.deviceId===id);
  if(!verified)return;
  selectedRemote.current={device:id,share:''};
  const seq=++navigationSerial.current;
  liveFolderRef.current={device:id,share:'',path:'.'};
  setRemoteDevice(id);setRemoteShare('');setRemoteShares([]);setRemotePath('.');setRemoteEntries([]);setSelectedFile(null);setFileQuery('');setVisibleLimit(200);
  setRemoteError('');setBrowseLatency('');
  const cached=verified.shares||directoryCache.current.get(id,'','');
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
  const verified=peersRef.current.find(peer=>peer.deviceId===deviceID);
  if(!verified||!verified.shares.some(share=>share.id===shareID))return;
  selectedRemote.current={device:deviceID,share:shareID};
  const seq=++navigationSerial.current;
  liveFolderRef.current={device:deviceID,share:shareID,path:dir};
  setRemoteDevice(deviceID);setRemoteShare(shareID);setRemotePath(dir);
  setSelectedFile(null);setFileQuery('');setVisibleLimit(200);setRemoteError('');setBrowseLatency('');
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
 refreshActiveDirRef.current=openRemoteDir;
 useEffect(()=>{
  window.relayOpenFileShare=(deviceID,shareID,dir='.')=>{
   const peer=peersRef.current.find(p=>p.deviceId===deviceID);
   if(!peer?.shares?.some(s=>s.id===shareID))return;
   setRemoteShares(peer.shares);
   setTab('remote');
   void openRemoteDir(deviceID,shareID,dir);
  };
  return()=>{delete window.relayOpenFileShare};
 },[peers]);
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
   toast?.('下载任务已创建，可在下方传输面板查看进度');
   void refreshTransfers();
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
 async function clearTransfer(id){
  try{
   await callJSON('goClearFileTransfer',null,id);
   saveHandles.current.delete(id);
   completedSaves.current.delete(id);
   completedUploads.current.delete(id);
   setTransferError('');
   await refreshTransfers();
  }catch(e){setTransferError(e.message||String(e))}
 }
 async function clearInactiveTransfers(){
  try{
   await callJSON('goClearInactiveFileTransfers',null);
   // Only discard state for records that were removed. Active downloads
   // still need their live Save As handles and upload-completion bookkeeping.
   const active=new Set(transfers.filter(t=>t.state==='running').map(t=>t.id));
   for(const id of saveHandles.current.keys())if(!active.has(id))saveHandles.current.delete(id);
   for(const id of completedSaves.current)if(!active.has(id))completedSaves.current.delete(id);
   for(const id of completedUploads.current)if(!active.has(id))completedUploads.current.delete(id);
   setTransferError('');
   await refreshTransfers();
  }catch(e){setTransferError(e.message||String(e))}
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
 const activeShareCount=shares.filter(s=>s.enabled).length;
 return <><section className={'file-sharing-host '+(tab==='remote'?'is-remote':'')} hidden={!active}>
  {error&&<div className="notice danger file-share-error" role="alert">{error}</div>}
  {tab==='local'&&<>
   <section className="card"><div className="card-head file-share-list-head"><div><div className="eyebrow">{shares.length} DIRECTORIES · {activeShareCount} 已启用</div><h2>共享文件夹</h2></div><div className="file-share-list-actions"><span className={'badge '+(state?.enabled?'ok':'neutral')}>{state?.enabled?'共享已启用':'共享已关闭'}</span><button className="btn quiet" onClick={refresh} disabled={busy}>刷新</button><button className="btn" onClick={()=>{setTab('remote');void refreshPeers()}}><Icon name="devices" size={15}/> 浏览其他设备文件</button><button className="btn primary" onClick={()=>edit(null)} disabled={!state||busy}>＋ 添加共享文件夹</button></div></div>
   {!state&&<div className="empty">正在读取本机文件共享配置…</div>}
   {state&&shares.length===0&&<div className="empty">还没有共享文件夹。添加本机目录后，再为设备设置只读或读写权限。</div>}
   {shares.map(share=><div className="file-share-item" key={share.id}>
    <div className="file-share-folder"><FileGlyph name={share.name} isDir/></div>
    <div className="grow file-share-detail"><b>{share.name}</b><div className="mono mini" title={share.rootPath}>{share.rootPath}</div><div className="mini">{countPermissions(share)} 台设备已授权 · {share.enabled?'目录已启用':'目录已停用'}</div></div>
    <span className={'badge '+(share.enabled?'ok':'neutral')}>{share.enabled?'已启用':'已停用'}</span>
    <button type="button" role="switch" aria-label={'启用 '+share.name} aria-checked={share.enabled} className={'switch '+(share.enabled?'on':'')} disabled={busy} onClick={()=>commit(s=>({...s,shares:s.shares.map(v=>v.id===share.id?{...v,enabled:!v.enabled}:v)}))}><span/></button>
    <div className="file-share-ops"><button className="btn" onClick={()=>edit(share)} disabled={busy}>编辑 / 权限</button><button className="btn danger" onClick={()=>remove(share)} disabled={busy}>移除</button></div>
   </div>)}
   </section>
   <details className="file-share-advanced" id="file-share-options">
    <summary><span><strong>共享服务与直连设置</strong><small>管理共享开关、UPnP 端口映射及授权统计</small></span><Icon name="settings" size={17}/></summary>
    <div className="file-share-advanced-content">   <div className="cards-3">
    <div className="metric"><div className="mini">共享文件夹</div><div className="metric-value">{shares.length}</div><div className="mini">{activeShareCount} 个已启用</div></div>
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
</div>
   </details>
  </>}
  {tab==='remote'&&<FileBrowserV4
    peers={peers} device={remoteDevice} share={remoteShare} path={remotePath} shares={remoteShares}
    entries={remoteEntries} loading={remoteLoading} error={remoteError} latency={browseLatency}
    canWrite={canUpload} warmStatus={warmStatus} uploading={uploading} uploadStaged={uploadStaged} uploadTotal={uploadTotal}
    transfers={transfers} treeRevision={treeRevision} onDevice={openRemoteDevice} onDirectory={openRemoteDir}
    onDownload={beginDownload} onUpload={uploadFiles} onMutate={mutateRemote}
    onCancelTransfer={cancelTransfer} onResumeTransfer={resumeDownload} onClearTransfer={clearTransfer} onClearInactiveTransfers={clearInactiveTransfers} onOpenTransfers={()=>onTransferOpenChange?.(true)} onCancelUpload={()=>{uploadCancelRef.current=true}}
    onRefresh={()=>{void refreshPeers();if(remoteDevice&&remoteShare)void openRemoteDir(remoteDevice,remoteShare,remotePath,{force:true})}}
    onBackToShares={()=>setTab('local')} toast={toast}/>}
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
 </section>
 {transferOpen&&document.getElementById('agent-file-transfer-portal')&&createPortal(
  <div className="global-transfer-shade" onMouseDown={e=>e.target===e.currentTarget&&onTransferOpenChange?.(false)}>
   <section ref={drawerRef} tabIndex={-1} className="global-transfer-panel" role="dialog" aria-modal="true" aria-label="文件传输任务">
    <div className="global-transfer-panel-head"><div><strong>文件传输任务</strong><span className="mini">{transfers.filter(t=>t.state==='running').length} 项进行中 · {transfers.length} 项记录</span></div><div className="actions"><button type="button" className="btn quiet" onClick={refreshTransfers}>刷新</button>{transfers.some(t=>t.state!=='running')&&<button type="button" className="btn" onClick={clearInactiveTransfers}>清除已结束</button>}<button type="button" className="btn" onClick={()=>onTransferOpenChange?.(false)} aria-label="关闭传输任务">关闭</button></div></div>
   
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
      {item.state!=='running'&&<button className="btn" title="只清除传输记录，不删除下载的文件" onClick={()=>clearTransfer(item.id)}>清除记录</button>}
    </div>)}
   </section>

  </div>,document.getElementById('agent-file-transfer-portal'))}
 </>;
}
