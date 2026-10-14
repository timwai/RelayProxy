import React, {useCallback,useEffect,useState} from 'react';
import {callJSON} from './bridge.js';
import './fileSharing.css';

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
 function edit(share){
  setEditor(share?'edit':'new');
  setDraft(share?{...share,permissions:{...share.permissions}}:blank());
  setDevice('');setPermission('read');
 }
 function addPermission(){
  const id=device.trim();
  if(!id)return;
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
  {tab==='remote'&&<section className="card file-share-unavailable"><h2>其他设备</h2><p>这一阶段正在接入 Server 的已授权设备发现，以及 Agent 之间的目录浏览协议。本地共享配置不会直接对外暴露；在服务端审批与对端身份校验接通前，这里不显示虚构的远程文件。</p></section>}
  {tab==='transfers'&&<section className="card file-share-unavailable"><h2>传输任务</h2><p>传输通道、下载速率与进度上报尚在后续实现阶段。任务列表将在真正建立传输后才开始显示，并区分 P2P、公网直连和 Relay 路径。</p></section>}
  {editor&&<div className="overlay show" onMouseDown={e=>{if(e.target===e.currentTarget&&!busy)setEditor(null)}}>
   <div className="modal wide" role="dialog" aria-modal="true" aria-label={editor==='new'?'添加共享文件夹':'编辑共享文件夹'}>
    <div className="modal-head"><h2>{editor==='new'?'添加共享文件夹':'编辑共享与设备权限'}</h2><button className="close" aria-label="关闭" onClick={()=>setEditor(null)} disabled={busy}>×</button></div>
    <div className="modal-body">
     <div className="form-grid">
      <label className="field"><span>共享名称</span><input className="control" value={draft.name} maxLength={100} onChange={e=>setDraft(s=>({...s,name:e.target.value}))} placeholder="例如：工作资料"/></label>
      <label className="field"><span>本机绝对路径</span><input className="control" value={draft.rootPath} onChange={e=>setDraft(s=>({...s,rootPath:e.target.value}))} placeholder="例如：/Users/me/Documents"/></label>
     </div>
     <div className="file-share-permissions"><h3>设备权限</h3><p className="mini">默认不授权。当前先按设备 ID 添加，后续接入 Server 授权设备选择器。</p>
      {Object.entries(draft.permissions||{}).map(([id,perm])=><div className="file-share-permission" key={id}><span className="mono grow">{id}</span><select className="control" value={perm} onChange={e=>setDraft(s=>({...s,permissions:{...s.permissions,[id]:e.target.value}}))}><option value="read">只读</option><option value="read_write">读写</option></select><button className="btn danger" onClick={()=>setDraft(s=>{const permissions={...s.permissions};delete permissions[id];return {...s,permissions}})}>移除</button></div>)}
      <div className="file-share-permission"><input className="control" placeholder="设备 ID" value={device} onChange={e=>setDevice(e.target.value)}/><select className="control" value={permission} onChange={e=>setPermission(e.target.value)}><option value="read">只读</option><option value="read_write">读写</option></select><button className="btn" onClick={addPermission} disabled={!device.trim()}>添加权限</button></div>
     </div>
    </div>
    <div className="modal-foot"><button className="btn" onClick={()=>setEditor(null)} disabled={busy}>取消</button><button className="btn primary" disabled={busy} onClick={saveEditor}>{busy?'保存中…':'保存共享'}</button></div>
   </div>
  </div>}
 </>;
}
