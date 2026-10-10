import React,{useCallback,useEffect,useMemo,useState} from 'react';
import {api,json,fmtDate,list} from './api.js';

// Browser identity is separate from Agent enrollment. This screen uses the
// existing authenticated, admin-only API; it never displays Cookie values.
export function BrowserSyncAdmin({identities=[]}){
  const [devices,setDevices]=useState([]);
  const [loading,setLoading]=useState(true);
  const [unavailable,setUnavailable]=useState(false);
  const [error,setError]=useState('');
  const [notice,setNotice]=useState('');
  const [busy,setBusy]=useState('');
  const [identity,setIdentity]=useState({});
  const [capabilities,setCapabilities]=useState({});
  const [filter,setFilter]=useState('');
  const activeIdentities=useMemo(()=>list(identities).filter(x=>
    x.status==='active'||x.state==='active'||!x.status),[identities]);
  const refresh=useCallback(async()=>{
    setError('');setLoading(true);
    try{
      const items=await api('/browser-sync/admin/devices');
      setDevices(list(items));
      setUnavailable(false);
    }catch(err){
      if(err.status===404){setUnavailable(true);setDevices([]);}
      else setError(err.message);
    }finally{setLoading(false);}
  },[]);
  useEffect(()=>{refresh();},[refresh]);
  const action=async (id,mode)=>{
    if(busy)return;
    if(mode==='revoke'&&!window.confirm('撤销后该浏览器将不能继续同步，相关配对规则也会失效。确定撤销？'))return;
    if(mode==='approve'&&!identity[id]){
      setError('请为待审批浏览器选择已启用的身份。');return;
    }
    setBusy(id);setError('');setNotice('');
    try{
      if(mode==='approve'){
        const caps=capabilities[id]||{send:true,receive:false};
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/approve',
          json('POST',{identityId:identity[id],send:!!caps.send,receive:!!caps.receive}));
        setNotice('浏览器设备已审批。');
      }else{
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/revoke',
          json('POST',{}));
        setNotice('浏览器设备及配对已撤销。');
      }
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const visible=devices.filter(d=>[d.name,d.id,d.identityId,d.state].join(' ').toLowerCase()
    .includes(filter.toLowerCase()));
  const count=state=>devices.filter(d=>d.state===state).length;
  return <section className="browser-sync-admin">
    <div className="page-head"><div><h1>浏览器同步</h1>
      <p>独立浏览器设备审批、授权与撤销。会话通过端到端加密中继，Server 不保存 Cookie。</p></div>
      <button className="btn" onClick={refresh} disabled={loading||!!busy}>
        {loading?'正在读取…':'刷新设备'}</button></div>
    {notice&&<div className="config-help" role="status">{notice}</div>}
    {error&&<div className="err-notice" role="alert">{error}</div>}
    {unavailable?<div className="panel"><h3>浏览器同步未启用</h3>
      <p>需要在 Server Admin HTTPS 监听器启用 browser_sync.enabled，并配置允许的 Chrome 扩展 ID。</p>
      <p>旧版本 Server 或尚未启用此功能时，这里不会显示任何浏览器设备。</p></div>:
    <>
      <div className="grid-cards">
        {[['待审批',count('pending')],['已批准',count('approved')],['已撤销',count('revoked')]]
          .map(([label,count])=><div className="panel" key={label}><strong>{label}</strong>
            <div style={{fontSize:26,fontWeight:700,marginTop:8}}>{count}</div></div>)}
      </div>
      <div className="panel">
        <div className="form-actions" style={{justifyContent:'space-between',alignItems:'center'}}>
          <div><h3>浏览器设备</h3><p>设备与传统 Agent 完全隔离；仅赋予 browser.sync.send / receive。</p></div>
          <input className="input" aria-label="搜索浏览器设备" placeholder="搜索名称、ID 或状态"
            value={filter} onChange={e=>setFilter(e.target.value)}/>
        </div>
        <div className="table-wrap"><table><thead><tr>
          <th>设备</th><th>状态</th><th>身份 / 权限</th><th>创建时间</th><th>操作</th>
        </tr></thead><tbody>
          {visible.length?visible.map(d=>{
            const caps=capabilities[d.id]||{send:true,receive:false};
            return <tr key={d.id}>
              <td><strong>{d.name||'Chrome'}</strong><small style={{display:'block',wordBreak:'break-all'}}>
                {d.id}</small></td>
              <td><span className="badge">{d.state==='pending'?'待审批':
                d.state==='approved'?'已批准':'已撤销'}</span></td>
              <td>{d.state==='pending'?<div style={{minWidth:170}}>
                  <select className="input" aria-label="所属身份"
                    value={identity[d.id]||''} onChange={e=>setIdentity(p=>({...p,[d.id]:e.target.value}))}>
                    <option value="">选择身份</option>{activeIdentities.map(x=>
                      <option key={x.id} value={x.id}>{x.name||x.id}</option>)}
                  </select>
                  <label style={{display:'block',marginTop:8}}><input type="checkbox" checked={caps.send}
                    onChange={e=>setCapabilities(p=>({...p,[d.id]:{...caps,send:e.target.checked}}))}/> 允许发送</label>
                  <label style={{display:'block',marginTop:4}}><input type="checkbox" checked={caps.receive}
                    onChange={e=>setCapabilities(p=>({...p,[d.id]:{...caps,receive:e.target.checked}}))}/> 允许接收</label>
                </div>:<><small>{d.identityId||'—'}</small>
                  <div>{d.send?'发送':''}{d.send&&d.receive?' / ':''}{d.receive?'接收':''}</div></>}</td>
              <td>{fmtDate(d.createdAt)}</td>
              <td>{d.state==='pending'?<button className="btn primary" disabled={!!busy||!identity[d.id]||
                  (!caps.send&&!caps.receive)} onClick={()=>action(d.id,'approve')}>批准</button>:
                d.state==='approved'?<button className="btn" disabled={!!busy}
                  onClick={()=>action(d.id,'revoke')}>撤销授权</button>:
                  <span>已失效</span>}</td>
            </tr>;
          }):<tr><td colSpan={5}>{loading?'正在加载…':'暂无匹配的浏览器设备'}</td></tr>}
        </tbody></table></div>
      </div>
      <div className="config-help">撤销只会阻止 RelayProxy 后续同步，
        不会撤销网站服务器已经签发的会话。需要彻底退出时，应使用网站自身的“退出所有设备”。</div>
    </>}
  </section>;
}
