import React, {useCallback,useEffect,useState} from 'react';
import {callJSON} from './bridge.js';
import './fileSharing.css';

const formatFileBytes=n=>{const num=Math.max(0,Number(n)||0);if(num<1024)return num.toFixed(0)+' B';const units=['KiB','MiB','GiB','TiB'];const power=Math.min(4,Math.floor(Math.log(num)/Math.log(1024)));return (num/1024**power).toFixed(1)+' '+units[power-1]};
const blank=()=>({id:'',name:'',rootPath:'',enabled:true,permissions:{}});
const empty=()=>({revision:0,enabled:false,shares:[]});
const countPermissions=share=>Object.values(share.permissions||{}).filter(v=>v==='read'||v==='read_write').length;

export default function FileSharingPage({toast}) {
 const [state,setState]=useState(null);
 const [tab,setTab]=useState('local');
 const [busy,setBusy]=useState(false);
 const [error,setError]=useState('');
 const [editor,setEditor]=useState(null);
 const [draft,setDraft]=useState(blank);
 const [device,setDevice]=useState('');
 const [permission,setPermission]=useState('read');
 const [candidates,setCandidates]=useState([]);
 const [candidateError,setCandidateError]=useState('');
 const [candidatesLoading,setCandidatesLoading]=useState(false);
 const [peers,setPeers]=useState([]);
 const [remoteDevice,setRemoteDevice]=useState('');
 const [remoteShares,setRemoteShares]=useState([]);
 const [remoteShare,setRemoteShare]=useState('');
 const [remotePath,setRemotePath]=useState('.');
 const [remoteEntries,setRemoteEntries]=useState([]);
 const [remoteLoading,setRemoteLoading]=useState(false);
 const [remoteError,setRemoteError]=useState('');
 const [transfers,setTransfers]=useState([]);
 const [transferError,setTransferError]=useState('');
 const [uploading,setUploading]=useState(false);
 const [uploadStaged,setUploadStaged]=useState(0);
 const [uploadTotal,setUploadTotal]=useState(0);
 const canUpload=remoteShares.some(s=>s.id===remoteShare&&s.permission==='read_write');
 async function uploadSelectedFile(event){
  const file=event.target.files?.[0];
  event.target.value='';
  if(!file||!remoteDevice||!remoteShare||uploading)return;
  if(!canUpload){setRemoteError('共享目录没有写入权限');return}
  if(file.size>20*1024**3){setRemoteError('单文件上传不能超过 20 GiB');return}
  setUploading(true);setUploadStaged(0);setUploadTotal(file.size);setRemoteError('');
  let sessionId='';
  try{
   const destination=remotePath==='.'?file.name:remotePath+'/'+file.name;
   const stage=await callJSON('goBeginFileUpload',null,remoteDevice,remoteShare,destination,file.size);
   if(!stage?.id)throw new Error('未能建立文件上传任务');
   sessionId=stage.id;
   const chunkSize=128*1024;
   for(let offset=0;offset<file.size;offset+=chunkSize){
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
   }
   const task=await callJSON('goCommitFileUpload',null,sessionId);
   if(!task?.id)throw new Error('未能开始远程上传');
   sessionId='';
   toast?.('上传任务已提交，正在将文件传输到远程设备');
   setTab('transfers');
  }catch(e){
   setRemoteError(e.message||String(e));
   if(sessionId)try{await callJSON('goDiscardFileUpload',null,sessionId)}catch{}
  }finally{setUploading(false)}
 }

 const refreshPeers=useCallback(async()=>{
  try{
   const result=await callJSON('goGetFileSharePeers',[]);
   if(!Array.isArray(result))throw new Error('设备发现返回了无效数据');
   setPeers(result);setRemoteError('');
  }catch(e){setRemoteError(e.message||String(e))}
 },[]);
 const refreshTransfers=useCallback(async()=>{
  try{
   const result=await callJSON('goGetFileTransfers',[]);
   setTransfers(Array.isArray(result)?result:[]);
   setTransferError('');
  }catch(e){setTransferError(e.message||String(e))}
 },[]);
 useEffect(()=>{if(tab==='remote')void refreshPeers()},[tab,refreshPeers]);
 useEffect(()=>{
  if(tab!=='transfers')return;
  void refreshTransfers();
  const timer=setInterval(()=>void refreshTransfers(),750);
  return ()=>clearInterval(timer);
 },[tab,refreshTransfers]);
 async function openRemoteDevice(id){
  setRemoteDevice(id);setRemoteShare('');setRemotePath('.');setRemoteEntries([]);
  setRemoteLoading(true);setRemoteError('');
  try{
   const result=await callJSON('goGetRemoteFileShares',[],id);
   if(!Array.isArray(result))throw new Error('未能读取设备共享目录');
   setRemoteShares(result);
  }catch(e){setRemoteError(e.message||String(e));setRemoteShares([])}
  finally{setRemoteLoading(false)}
 }
 async function openRemoteDir(deviceID,shareID,dir){
  setRemoteLoading(true);setRemoteError('');
  try{
   const result=await callJSON('goGetRemoteFileEntries',[],deviceID,shareID,dir);
   if(!Array.isArray(result))throw new Error('未能读取文件列表');
   setRemoteDevice(deviceID);setRemoteShare(shareID);setRemotePath(dir);setRemoteEntries(result);
  }catch(e){setRemoteError(e.message||String(e))}
  finally{setRemoteLoading(false)}
 }
 async function beginDownload(entry){
  const relativePath=remotePath==='.'?entry.name:remotePath+'/'+entry.name;
  try{
   await callJSON('goStartFileDownload',null,remoteDevice,remoteShare,relativePath);
   toast?.('下载任务已创建');
   setTab('transfers');
  }catch(e){setRemoteError(e.message||String(e))}
 }
 async function cancelTransfer(id){
  try{await callJSON('goCancelFileDownload',null,id);await refreshTransfers()}
  catch(e){setTransferError(e.message||String(e))}
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
   <section className="card">
    <div className="card-head"><div><div className="eyebrow">SERVER APPROVED</div><h2>已授权文件共享设备</h2></div><button className="btn" onClick={refreshPeers}>刷新设备</button></div>
    {peers.length===0?<div className="empty">暂无可用的共享设备。请在 Server 审批访问端 files.client 和共享端 files.host，并确认双方属于相同身份且已连接。</div>:
     <div className="file-share-peer-list">{peers.map(peer=><button type="button" key={peer.deviceId} className={'file-share-peer '+(remoteDevice===peer.deviceId?'selected':'')} onClick={()=>openRemoteDevice(peer.deviceId)}><b>{peer.name||peer.deviceId}</b><small className="mono">{peer.deviceId}</small><span className="badge ok">在线</span></button>)}</div>}
   </section>
   {remoteDevice&&<section className="card"><div className="card-head"><div><div className="eyebrow">SHARED FOLDERS</div><h2>远程共享文件夹</h2></div><span className="mini">{remoteLoading?'读取中…':''}</span></div>
    {remoteShares.length===0?<div className="empty">{remoteLoading?'读取中…':'此设备尚未向本机开放共享文件夹。请在共享端开启共享，并对本设备授予只读或读写权限。'}</div>:
     <div className="file-share-peer-list">{remoteShares.map(share=><button type="button" key={share.id} className={'file-share-peer '+(remoteShare===share.id?'selected':'')} onClick={()=>openRemoteDir(remoteDevice,share.id,'.')}><b>▱ {share.name}</b><span className="badge">{share.permission==='read_write'?'读写':'只读'}</span></button>)}</div>}
   </section>}
   {remoteShare&&<section className="card">
    <div className="card-head"><div><div className="eyebrow">REMOTE FILES · RELAY</div><h2>远程文件</h2></div><div className="file-share-upload-actions">{canUpload&&<label className={'btn primary '+(uploading?'disabled':'')} title="上传文件到当前目录">＋ 上传文件<input aria-label="选择要上传的文件" type="file" disabled={uploading||remoteLoading} onChange={uploadSelectedFile} style={{display:'none'}}/></label>}<button className="btn" onClick={()=>openRemoteDir(remoteDevice,remoteShare,remotePath)} disabled={remoteLoading||uploading}>刷新目录</button></div></div>
    {uploading&&<div className="file-share-upload-staging" role="status">正在准备本地文件：{formatFileBytes(uploadStaged)} / {formatFileBytes(uploadTotal)} · {uploadTotal?Math.floor(100*uploadStaged/uploadTotal):100}%<div className="file-share-task-track"><div style={{width:(uploadTotal?Math.min(100,100*uploadStaged/uploadTotal):100)+'%'}}/></div></div>}
    <div className="file-share-breadcrumb"><button className="btn" onClick={()=>openRemoteDir(remoteDevice,remoteShare,'.')} disabled={remoteLoading}>根目录</button><span className="mono">{remotePath==='.'?'/':'/'+remotePath}</span>{remotePath!=='.'&&<button className="btn" onClick={()=>openRemoteDir(remoteDevice,remoteShare,remotePath.split('/').slice(0,-1).join('/')||'.')} disabled={remoteLoading}>返回上级</button>}</div>
    {remoteEntries.length===0?<div className="empty">{remoteLoading?'读取中…':'文件夹为空'}</div>:
     remoteEntries.map(entry=><div className="file-share-entry" key={entry.name}><div className="grow"><b>{entry.isDir?'▱':'▤'} {entry.name}</b><small>{entry.isDir?'文件夹':formatFileBytes(entry.size)}</small></div>{entry.isDir?<button className="btn" disabled={remoteLoading} onClick={()=>openRemoteDir(remoteDevice,remoteShare,remotePath==='.'?entry.name:remotePath+'/'+entry.name)}>打开</button>:<button className="btn primary" disabled={remoteLoading} onClick={()=>beginDownload(entry)}>下载</button>}</div>)}
   </section>}
  </>}
  {tab==='transfers'&&<section className="card">
   <div className="card-head"><div><div className="eyebrow">TRANSFERS</div><h2>传输任务</h2></div><button className="btn" onClick={refreshTransfers}>刷新</button></div>
   {transferError&&<div className="notice danger" role="alert">{transferError}</div>}
   <div className="mini">下载保存至本机 Downloads/RelayProxy；上传到共享端当前目录。传输使用 Relay 加密通道，文件专用直连尚未接入。</div>
   {transfers.length===0?<div className="empty">暂无传输任务。在“其他设备”中选择文件下载，或在读写目录内上传文件。</div>:
    transfers.map(item=><div className="file-share-task" key={item.id}>
     <div className="file-share-task-title"><div className="grow"><b>{item.direction==='upload'?'上传 · ':'下载 · '}{item.fileName}</b><div className="mini mono">{item.deviceId} · {item.direction==='upload'?'远程共享文件夹':item.destination||'下载中'}</div></div><span className={'badge '+(item.state==='completed'?'ok':item.state==='failed'?'danger':'neutral')}>{({'running':item.direction==='upload'?'上传中':'下载中','completed':'已完成','failed':'失败','cancelled':'已取消'})[item.state]||item.state}</span></div>
     <div className="file-share-task-track"><div style={{width:(item.total>0?Math.min(100,item.done/item.total*100):0)+'%'}}/></div>
     <div className="file-share-task-meta"><span>{formatFileBytes(item.done)} / {item.total>0?formatFileBytes(item.total):'计算中'} · {item.total>0?Math.floor(item.done/item.total*100):0}%</span><span>{item.state==='running'?formatFileBytes(item.speed)+'/s':'Relay QUIC / TLS'}</span></div>
     {item.error&&<div className="notice danger">{item.error}</div>}
     {item.state==='running'&&<button className="btn danger" onClick={()=>cancelTransfer(item.id)}>取消传输</button>}
    </div>)}
  </section>}
  {editor&&<div className="overlay show" onMouseDown={e=>{if(e.target===e.currentTarget&&!busy)setEditor(null)}}>
   <div className="modal wide" role="dialog" aria-modal="true" aria-label={editor==='new'?'添加共享文件夹':'编辑共享文件夹'}>
    <div className="modal-head"><h2>{editor==='new'?'添加共享文件夹':'编辑共享与设备权限'}</h2><button className="close" aria-label="关闭" onClick={()=>setEditor(null)} disabled={busy}>×</button></div>
    <div className="modal-body">
     <div className="form-grid">
      <label className="field"><span>共享名称</span><input className="control" value={draft.name} maxLength={100} onChange={e=>setDraft(s=>({...s,name:e.target.value}))} placeholder="例如：工作资料"/></label>
      <label className="field"><span>本机绝对路径</span><input className="control" value={draft.rootPath} onChange={e=>setDraft(s=>({...s,rootPath:e.target.value}))} placeholder="例如：/Users/me/Documents"/></label>
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
