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
  const [view,setView]=useState('devices');
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
    if(mode==='approve'&&!identity[id]){
      setError('请为待审批浏览器选择已启用的身份。');return;
    }
    setBusy(id);setError('');setNotice('');
    try{
      if(mode==='approve'){
        const caps=capabilities[id]||{send:true,receive:false};
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/approve',
          json('POST',{identityId:identity[id]||devices.find(d=>d.id===id)?.requestedIdentityId,send:!!caps.send,receive:!!caps.receive}));
        setNotice(devices.some(d=>d.id===id&&d.extensionId)
          ?'扩展与该浏览器设备已一次批准。'
          :'旧版浏览器设备已批准。');
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
  const extensionAction=async(id,action)=>{
    if(action==='delete'&&!window.confirm('永久删除已撤销扩展的授权记录？需先删除其关联的旧浏览器设备。重新申请仍需管理员审批。'))return;
    setBusy(id);setError('');
    try{
      if(action==='delete'){
        await api('/browser-sync/admin/extensions/'+encodeURIComponent(id),{method:'DELETE'});
        setNotice('旧扩展授权记录已删除；请在 Chrome 重新提交申请。');
      }else{
        await api('/browser-sync/admin/extensions/'+encodeURIComponent(id)+'/'+action,json('POST',{}));
        setNotice(action==='approve'?'已批准旧版单独上报的扩展；关联设备仍需独立审批。':'扩展已撤销，新会话将被拒绝。');
      }
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
        <button className="btn" type="button" onClick={()=>{setView('devices');refresh();}}
          disabled={loading||!!busy}>刷新</button>
        <button className="btn" type="button" onClick={onOpenSettings}>服务配置</button>
      </div>
    </div>
    {notice&&<div className="config-help" role="status">{notice}</div>}
    {error&&<div className="err-notice" role="alert">{error}</div>}
    {unavailable?<div className="panel browser-sync-empty">
      <h3>浏览器同步暂不可用</h3>
      <p>请检查 Server 版本，并在服务配置中启用浏览器同步。</p>
      <button className="btn primary" type="button" onClick={onOpenSettings}>打开服务配置</button>
    </div>:<>
      <div className="browser-sync-summary">
        {states.slice(1).map(([id,label,total])=><button key={id} type="button"
          className={'browser-sync-stat '+(view==='devices'&&stateFilter===id?'selected':'')}
          onClick={()=>{setView('devices');setStateFilter(id);}}>
          <span>{label}</span><strong>{total}</strong>
        </button>)}
      </div>
      <div className="panel">
        <div className="browser-sync-toolbar">
          <div className="browser-sync-views" role="tablist" aria-label="浏览器同步管理">
            <button type="button" role="tab" aria-selected={view==='devices'}
              className={'btn '+(view==='devices'?'primary':'')}
              onClick={()=>setView('devices')}>浏览器设备</button>
            <button type="button" role="tab" aria-selected={view==='extensions'}
              className={'btn '+(view==='extensions'?'primary':'')}
              onClick={()=>setView('extensions')}>扩展来源</button>
          </div>
          {view==='devices'&&<input className="input browser-sync-search" aria-label="搜索浏览器设备"
            placeholder="搜索设备名称或 ID" value={filter} onChange={e=>setFilter(e.target.value)}/>}
        </div>
        {view==='devices'?<>
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
        </>:<>
          <div className="browser-sync-panel-title"><h3>扩展来源</h3>
            <span className="muted">仅在扩展失效或需要重新申请时管理</span></div>
          <div className="table-wrap"><table>
            <thead><tr><th>扩展 ID</th><th>授权状态</th><th>操作</th></tr></thead>
            <tbody>{extensions.map(ext=><tr key={ext.id}>
              <td className="browser-sync-extension-id">{ext.id}</td>
              <td><span className={'badge '+(ext.state==='approved'?'ok':ext.state==='revoked'?'danger':'')}>
                {ext.state==='pending'?'待审批':ext.state==='approved'?'已批准':'已撤销'}</span></td>
              <td className="browser-sync-actions">
                {ext.state==='pending'&&linkedExtensions.has(ext.id)?
                  <button type="button" className="btn" onClick={()=>{setView('devices');setStateFilter('pending');}}>
                    审批关联设备</button>:
                 ext.state==='pending'?
                  <button type="button" className="btn primary" disabled={!!busy}
                    onClick={()=>extensionAction(ext.id,'approve')}>批准扩展</button>:
                 ext.state==='approved'?
                  <button type="button" className="btn" disabled={!!busy}
                    onClick={()=>extensionAction(ext.id,'revoke')}>撤销</button>:
                 linkedExtensions.has(ext.id)?
                  <button type="button" className="btn" onClick={()=>{setView('devices');setStateFilter('all');}}>
                    先删除关联设备</button>:
                  <button type="button" className="btn danger" disabled={!!busy}
                    onClick={()=>extensionAction(ext.id,'delete')}>删除扩展</button>}
              </td>
            </tr>)}
            {!extensions.length&&<tr><td colSpan={3} className="browser-sync-no-data">
              {loading?'读取中…':'暂无扩展记录'}</td></tr>}</tbody>
          </table></div>
        </>}
      </div>
    </>}
  </section>;
}
