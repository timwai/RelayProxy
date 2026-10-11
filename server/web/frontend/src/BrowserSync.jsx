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
  const extensionAction=async(id,action)=>{
    setBusy(id);setError('');
    try{
      await api('/browser-sync/admin/extensions/'+encodeURIComponent(id)+'/'+action,json('POST',{}));
      setNotice(action==='approve'?'Chrome 扩展已批准。现在可注册浏览器设备。':'扩展已撤销，新会话将被拒绝。');
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
      <div className="form-actions"><button className="btn" onClick={onOpenSettings}>编辑浏览器服务配置</button>
      <button className="btn" onClick={refresh} disabled={loading||!!busy}>
        {loading?'正在读取…':'刷新设备'}</button></div></div>
    {notice&&<div className="config-help" role="status">{notice}</div>}
    {error&&<div className="err-notice" role="alert">{error}</div>}
    {unavailable?<div className="panel"><h3>浏览器同步未启用</h3>
      <p>Server 返回了 HTTP 404，当前管理 API 中没有浏览器同步设备接口。</p>
      <p>在 Linux Server 上确认运行的是包含 Browser Sync 的新版 relay-server，
        并在实际加载的 Server YAML 中启用 browser_sync.enabled: true、
        开启 server.admin.tls_enabled: true，然后重启 Server。扩展 ID 由插件上报、管理员审核，无需手动填写。</p>
      <p>如果经 Nginx/Caddy 访问，还要检查 /api/v1/browser-sync/ 路径是否转发到
        <strong> Admin HTTPS 监听端口</strong>，而不是 Agent TCP/QUIC 端口。
        直接访问 Admin HTTPS 的此接口，未登录时应为 HTTP 401，而不是 404。</p></div>:
    <>
      <div className="panel">
        <h3>Chrome 扩展 ID 自动上报审批</h3>
        <p>Chrome 扩展首次点击“上报扩展 ID / 注册浏览器设备”将登记待审批的 ID，无需人工复制；
          批准扩展不等于批准设备或 Cookie 读取，设备仍需独立身份授权。</p>
        <div className="table-wrap"><table><thead><tr><th>扩展 ID</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>{extensions.map(ext=><tr key={ext.id}>
          <td style={{wordBreak:'break-all'}}>{ext.id}</td><td>{ext.state}</td>
          <td>{ext.state==='pending'?<button className="btn primary" disabled={!!busy}
            onClick={()=>extensionAction(ext.id,'approve')}>批准扩展</button>:
            ext.state==='approved'?<button className="btn" disabled={!!busy}
              onClick={()=>extensionAction(ext.id,'revoke')}>撤销扩展</button>:'已撤销'}</td>
        </tr>)}
        {!extensions.length&&<tr><td colSpan={3}>暂无已上报的扩展</td></tr>}</tbody></table></div>
      </div>
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
