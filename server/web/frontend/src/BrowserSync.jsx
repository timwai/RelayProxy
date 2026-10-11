import React,{useCallback,useEffect,useMemo,useState} from 'react';
import {api,json,fmtDate,list} from './api.js';

// Browser identity is separate from Agent enrollment. This screen uses the
// existing authenticated, admin-only API; it never displays Cookie values.
export function BrowserSyncAdmin({identities=[],onOpenSettings}){
  const [devices,setDevices]=useState([]);
  const [extensions,setExtensions]=useState([]);
  const [loading,setLoading]=useState(true);
  const [unavailable,setUnavailable]=useState(false);
  const [error,setError]=useState('');
  const [notice,setNotice]=useState('');
  const [busy,setBusy]=useState('');
  const [identity,setIdentity]=useState({});
  const [capabilities,setCapabilities]=useState({});
  const [filter,setFilter]=useState('');
  const [stateFilter,setStateFilter]=useState('all');
  const activeIdentities=useMemo(()=>list(identities).filter(x=>{
    const state=x.status??x.state;
    return !state||state==='active';
  }),[identities]);
  const refresh=useCallback(async()=>{
    setError('');setLoading(true);
    try{
      const items=await api('/browser-sync/admin/devices');
      setDevices(list(items));
      setExtensions(list(await api('/browser-sync/admin/extensions')));
      setUnavailable(false);
    }catch(err){
      if(err.status===404){setUnavailable(true);setDevices([]);setExtensions([]);}
      else setError(err.message);
    }finally{setLoading(false);}
  },[]);
  useEffect(()=>{refresh();},[refresh]);
  const action=async (id,mode)=>{
    if(busy)return;
    if(mode==='revoke'&&!window.confirm('撤销后该浏览器将不能继续同步，相关配对规则也会失效。确定撤销？'))return;
    if(mode==='delete'&&!window.confirm('永久删除已撤销浏览器设备及其全部配对/同步记录？此操作不可恢复，重新注册仍须管理员审批。'))return;
    if(mode==='approve'&&!(identity[id]||devices.find(d=>d.id===id)?.requestedIdentityId)){
      setError('请为待审批浏览器选择已启用的身份。');return;
    }
    setBusy(id);setError('');setNotice('');
    try{
      if(mode==='approve'){
        const caps=capabilities[id]||{send:true,receive:false};
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/approve',
          json('POST',{identityId:identity[id]||devices.find(d=>d.id===id)?.requestedIdentityId,send:!!caps.send,receive:!!caps.receive}));
        setNotice('浏览器设备已批准，可以连接中继。');
      }else if(mode==='delete'){
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id),{method:'DELETE'});
        setNotice('旧浏览器设备及配对记录已删除。若要重新加入，请在 Chrome 重新提交授权申请。');
      }else{
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/revoke',
          json('POST',{}));
        setNotice('浏览器设备及配对已撤销。');
      }
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const removeLegacyBlock=async id=>{
    if(busy||!window.confirm('清除这条历史扩展禁用记录？关联设备必须先撤销并删除。新设备仍需管理员单独审批。'))return;
    setBusy(id);setError('');setNotice('');
    try{
      await api('/browser-sync/admin/extensions/'+encodeURIComponent(id),{method:'DELETE'});
      setNotice('已清除历史扩展禁用记录，浏览器设备仍需单独申请审批。');
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const visible=devices.filter(d=>
    (stateFilter==='all'||d.state===stateFilter)&&
    [d.name,d.id,d.identityId,d.requestedIdentityId,d.state,d.extensionId].join(' ').toLowerCase()
      .includes(filter.trim().toLowerCase()))
    .sort((a,b)=>({pending:0,approved:1,revoked:2}[a.state]??3)-
      ({pending:0,approved:1,revoked:2}[b.state]??3));
  const linkedExtensions=new Set(devices.filter(d=>d.extensionId).map(d=>d.extensionId));
  const count=state=>devices.filter(d=>d.state===state).length;
  const states=[['all','全部',devices.length],['pending','待审批',count('pending')],
    ['approved','已批准',count('approved')],['revoked','已撤销',count('revoked')]];
  return <section className="browser-sync-admin browser-sync-refined">
    <div className="page-head">
      <div><h1>浏览器同步</h1><p>设备授权与同步访问管理</p></div>
      <div className="form-actions">
        <button className="btn" type="button" onClick={()=>{refresh();}}
          disabled={loading||!!busy}>刷新</button>
      </div>
    </div>
    {notice&&<div className="config-help" role="status">{notice}</div>}
    {error&&<div className="err-notice" role="alert">{error}</div>}
    {unavailable?<div className="panel browser-sync-empty">
      <h3>浏览器同步暂不可用</h3>
      <p>请确认 Server 已部署支持 Browser Sync 的版本，并在服务端 YAML 启用 browser_sync.enabled。</p>
    </div>:<>
      <div className="browser-sync-summary">
        {states.slice(1).map(([id,label,total])=><button key={id} type="button"
          className={'browser-sync-stat '+(stateFilter===id?'selected':'')}
          onClick={()=>setStateFilter(id)}>
          <span>{label}</span><strong>{total}</strong>
        </button>)}
      </div>
      <div className="panel">
        <div className="browser-sync-toolbar">
          <input className="input browser-sync-search" aria-label="搜索浏览器设备"
            placeholder="搜索设备名称或 ID" value={filter} onChange={e=>setFilter(e.target.value)}/>
        </div>
          <div className="browser-sync-filters" aria-label="按授权状态筛选">
            {states.map(([id,label,total])=><button key={id} type="button"
              className={'btn '+(stateFilter===id?'active':'')}
              aria-pressed={stateFilter===id}
              onClick={()=>setStateFilter(id)}>{label} <span>{total}</span></button>)}
          </div>
          <div className="table-wrap"><table>
            <thead><tr><th>浏览器设备</th><th>状态</th><th>所属身份 / 权限</th><th>操作</th></tr></thead>
            <tbody>{visible.map(d=>{
              const caps=capabilities[d.id]||{send:true,receive:false};
              return <tr key={d.id}>
                <td><div className="browser-sync-device-name">{d.name||'Chrome 浏览器'}</div>
                  <small className="browser-sync-device-id" title={d.id}>{d.id}</small>
                  <small className="browser-sync-device-extra" title={d.extensionId||''}>
                    {d.extensionId?'扩展 · '+d.extensionId.slice(0,8)+'…':'旧版扩展'}</small></td>
                <td><span className={'badge '+(d.state==='approved'?'ok':d.state==='revoked'?'danger':'')}>
                  {d.state==='pending'?'待审批':d.state==='approved'?'已批准':'已撤销'}</span>
                  {d.extensionState==='revoked'&&<small className="browser-sync-device-extra">扩展已撤销</small>}</td>
                <td>{d.state==='pending'?<div className="browser-sync-approve-controls">
                  <select className="input" aria-label={'为 '+(d.name||d.id)+' 选择身份'}
                    value={identity[d.id]||d.requestedIdentityId||''} onChange={e=>setIdentity(v=>({...v,[d.id]:e.target.value}))}>
                    <option value="">选择身份</option>{activeIdentities.map(v=>
                      <option value={v.id} key={v.id}>{v.name||v.id}</option>)}
                  </select>
                  <div className="browser-sync-permissions">
                    <label><input type="checkbox" checked={caps.send}
                      onChange={e=>setCapabilities(v=>({...v,[d.id]:{...caps,send:e.target.checked}}))}/> 发送</label>
                    <label><input type="checkbox" checked={caps.receive}
                      onChange={e=>setCapabilities(v=>({...v,[d.id]:{...caps,receive:e.target.checked}}))}/> 接收</label>
                  </div>
                </div>:<><div>{activeIdentities.find(i=>i.id===d.identityId)?.name||d.identityId||'—'}</div>
                  <small className="browser-sync-device-extra">
                    {[d.send?'发送':null,d.receive?'接收':null].filter(Boolean).join(' / ')||'无同步权限'}</small></>}</td>
                <td className="browser-sync-actions">{d.state==='pending'?
                  <button className="btn primary" type="button"
                    disabled={!!busy||!(identity[d.id]||d.requestedIdentityId)||(!caps.send&&!caps.receive)}
                    onClick={()=>action(d.id,'approve')}>批准</button>:
                  d.state==='approved'?<button className="btn" type="button"
                    disabled={!!busy} onClick={()=>action(d.id,'revoke')}>撤销授权</button>:
                  <button className="btn danger" type="button" disabled={!!busy}
                    onClick={()=>action(d.id,'delete')}>删除设备</button>}</td>
              </tr>;
            })}
            {!visible.length&&<tr><td colSpan={4} className="browser-sync-no-data">
              {loading?'读取中…':'暂无匹配设备'}</td></tr>}</tbody>
          </table></div>
        {extensions.some(ext=>ext.state==='revoked')&&<section className="browser-sync-legacy-blocks">
          <h3>历史禁用来源</h3>
          <p className="muted">仅用于清理旧版本留下的扩展级禁用记录；新授权只审批浏览器设备。</p>
          {extensions.filter(ext=>ext.state==='revoked').map(ext=><div key={ext.id} className="browser-sync-legacy-row">
            <span className="browser-sync-extension-id">{ext.id}</span>
            {linkedExtensions.has(ext.id)?<span>请先撤销并删除关联设备</span>:
              <button className="btn danger" type="button" disabled={!!busy}
                onClick={()=>removeLegacyBlock(ext.id)}>清除旧禁用记录</button>}
          </div>)}
        </section>}
      </div>
    </>}
  </section>;
}
