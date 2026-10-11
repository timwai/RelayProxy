import React,{useCallback,useEffect,useMemo,useState} from 'react';
import {api,json,fmtDate,list} from './api.js';
import {validateBrowserNetworkDraft,applyBrowserNetworkDraft} from './browserNetworkConfig.js';

// Uses the existing admin-only revisioned Server config API. Network
// listeners and allowlists are intentionally restart-gated (no hot reload).
function BrowserNetworkSettings({onOpenSettings}) {
  const [record,setRecord]=useState(null);
  const [edit,setEdit]=useState(null);
  const [exits,setExits]=useState([]);
  const [loading,setLoading]=useState(true);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState('');
  const [notice,setNotice]=useState('');
  const load=useCallback(async()=>{
    setLoading(true);setError('');
    try{
      const [state,agents]=await Promise.all([api('/server/config'),api('/exits')]);
      setRecord(state);
      setEdit({
        listen:state.config.browserSync?.networkProxyListen||'',
        advertise:state.config.browserSync?.networkProxyAdvertise||'',
        allowed:list(state.config.browserSync?.allowedExitIds)
      });
      setExits(list(agents).filter(x=>x.deviceId));
    }catch(e){setError(e.message);}
    finally{setLoading(false);}
  },[]);
  useEffect(()=>{load()},[load]);
  const toggle=id=>setEdit(v=>({...v,allowed:v.allowed.includes(id)?
    v.allowed.filter(x=>x!==id):[...v.allowed,id]}));
  const onlineIDs=new Set(exits.map(x=>x.deviceId));
  const extras=(edit?.allowed||[]).filter(id=>!onlineIDs.has(id));
  const changed=record&&edit&&(
    edit.listen!== (record.config.browserSync?.networkProxyListen||'') ||
    edit.advertise!== (record.config.browserSync?.networkProxyAdvertise||'') ||
    JSON.stringify([...edit.allowed].sort())!==
      JSON.stringify([...list(record.config.browserSync?.allowedExitIds)].sort()));
  const save=async()=>{
    if(!record||!edit||busy||!changed)return;
    const validation=validateBrowserNetworkDraft(edit);
    if(validation){setError(validation);return;}
    setBusy(true);setError('');setNotice('');
    try{
      const config=applyBrowserNetworkDraft(record.config,edit);
      const result=await api('/server/config',json('PUT',{revision:record.revision,config}));
      setRecord(result);
      setEdit({listen:result.config.browserSync?.networkProxyListen||'',
        advertise:result.config.browserSync?.networkProxyAdvertise||'',
        allowed:list(result.config.browserSync?.allowedExitIds)});
      setNotice(result.restartRequired?
        '配置已保存到 Server。网络代理监听与 Agent 白名单必须重启 Server 才会生效。':
        '配置已保存；请核实运行状态后再创建网络绑定邀请。');
    }catch(e){
      setError(e.status===409?'配置已被其他管理页面修改，请重新读取后保存，避免覆盖其他设置。':e.message);
    }finally{setBusy(false);}
  };
  return <div className="panel">
    <div className="form-actions" style={{justifyContent:'space-between',alignItems:'center'}}>
      <div><h3>Agent 网络出口 · 浏览器配置</h3>
        <p>管理员直接配置 Server 的网络代理和允许共享的 Agent；关闭时不会影响 Cookie/Storage 同步。</p></div>
      <div className="form-actions">
        <button className="btn" onClick={load} disabled={busy||loading}>重新读取配置</button>
        <button className="btn" onClick={onOpenSettings}>高级设置</button>
      </div>
    </div>
    {error&&<div className="err-notice" role="alert">{error}</div>}
    {notice&&<div className="config-help" role="status">{notice}</div>}
    {loading?<p>正在加载浏览器网络配置…</p>:!edit?
      <p>未取得配置，请通过“高级设置”确认 Server 是否启用配置管理。</p>:
      <>
        <div className="form-grid">
          <label>代理 TCP 监听地址
            <input className="input" value={edit.listen} placeholder=":21292"
              onChange={e=>setEdit(v=>({...v,listen:e.target.value}))} disabled={busy}/>
          </label>
          <label>对外代理域名与端口
            <input className="input" value={edit.advertise}
              placeholder="relay.example.com:21292"
              onChange={e=>setEdit(v=>({...v,advertise:e.target.value}))} disabled={busy}/>
          </label>
        </div>
        <p className="muted">两个地址都留空表示停用网络代理。对外域名必须匹配 Server TLS 证书，
          端口需要由管理员在防火墙放行。修改配置不会自动重启服务。</p>
        <h4>允许作为浏览器网络出口的 Agent</h4>
        {exits.length? <div className="grid-cards">{exits.map(agent=>
          <label className="panel" key={agent.deviceId} style={{display:'flex',gap:10,alignItems:'center',cursor:'pointer'}}>
            <input type="checkbox" checked={edit.allowed.includes(agent.deviceId)} disabled={busy}
              onChange={()=>toggle(agent.deviceId)}/>
            <span><strong>{agent.deviceName||agent.deviceId}</strong>
              <small style={{display:'block',wordBreak:'break-all'}}>{agent.deviceId}</small>
              <small>在线 · 需 Agent 自行启用浏览器网络共享，且与浏览器身份相同</small></span>
          </label>)}</div>:<p className="muted">当前没有在线 Agent 出口。可在“高级设置”手动配置离线设备 ID。</p>}
        {extras.length>0&&<div><p>已配置但当前不在在线出口列表中的设备：</p>
          {extras.map(id=><label key={id} style={{display:'flex',gap:8,alignItems:'center',marginBottom:8}}>
            <input type="checkbox" checked disabled={busy} onChange={()=>toggle(id)}/>
            <code>{id}</code><small>（离线或不再具备出口角色）</small>
          </label>)}</div>}
        {record?.restartRequired&&<p className="config-help">有配置与运行中的 Server 不一致，需要重启 Server 才会应用新设置：
          {list(record.restartFields).join('、')||'网络配置'}</p>}
        {!record?.config?.browserSync?.enabled&&<p className="err-notice">
          Browser Sync 服务当前配置为关闭。请先在高级设置中启用，保存并重启 Server。</p>}
        <div className="form-actions">
          <button className="btn primary" onClick={save} disabled={!changed||busy||!record?.config?.browserSync?.enabled}>
            {busy?'正在保存…':'保存网络出口配置'}</button>
          {changed&&<button className="btn" onClick={load} disabled={busy}>取消修改</button>}
        </div>
        <p className="muted">还需在 Agent Web / GUI → 本机出口共享 开启“允许作为浏览器网络出口”，
          并重启或重连 Agent。只有 Server、Agent 和设备身份授权同时满足后，Chrome 扩展才能发送绑定邀请。</p>
      </>}
  </div>;
}

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
        setNotice(devices.some(d=>d.id===id&&d.extensionId)
          ?'扩展与该浏览器设备已一次批准。'
          :'旧版浏览器设备已批准。');
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
      setNotice(action==='approve'?'已批准旧版单独上报的扩展；关联设备仍需独立审批。':'扩展已撤销，新会话将被拒绝。');
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const visible=devices.filter(d=>[d.name,d.id,d.identityId,d.state,d.extensionId].join(' ').toLowerCase()
    .includes(filter.toLowerCase()));
  const linkedExtensions=new Set(devices.filter(d=>d.extensionId).map(d=>d.extensionId));
  const legacyExtensions=extensions.filter(e=>e.state!=='pending'||!linkedExtensions.has(e.id));
  const count=state=>devices.filter(d=>d.state===state).length;
  return <section className="browser-sync-admin">
    <div className="page-head"><div><h1>浏览器同步</h1>
      <p>首次只需一次审批：同时批准扩展身份及当前浏览器设备。会话采用端到端加密，Server 不保存 Cookie。</p></div>
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
      <BrowserNetworkSettings onOpenSettings={onOpenSettings}/>
      <div className="panel">
        <h3>扩展来源管理</h3>
        <p>新版扩展首次申请时会自动上报扩展 ID 和设备公钥；关联的待审批扩展直接在下方设备列表一次批准。
          这里保留旧版本只上报扩展 ID 的独立审批，以及已批准扩展的撤销功能。</p>
        <div className="table-wrap"><table><thead><tr><th>扩展 ID</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>{legacyExtensions.map(ext=><tr key={ext.id}>
          <td style={{wordBreak:'break-all'}}>{ext.id}</td><td>{ext.state}</td>
          <td>{ext.state==='pending'?<button className="btn primary" disabled={!!busy}
            onClick={()=>extensionAction(ext.id,'approve')}>批准扩展</button>:
            ext.state==='approved'?<button className="btn" disabled={!!busy}
              onClick={()=>extensionAction(ext.id,'revoke')}>撤销扩展</button>:'已撤销'}</td>
        </tr>)}
        {!legacyExtensions.length&&<tr><td colSpan={3}>暂无需要单独处理的扩展。关联申请请在下方审批。</td></tr>}</tbody></table></div>
      </div>
      <div className="grid-cards">
        {[['待审批',count('pending')],['已批准',count('approved')],['已撤销',count('revoked')]]
          .map(([label,count])=><div className="panel" key={label}><strong>{label}</strong>
            <div style={{fontSize:26,fontWeight:700,marginTop:8}}>{count}</div></div>)}
      </div>
      <div className="panel">
        <div className="form-actions" style={{justifyContent:'space-between',alignItems:'center'}}>
          <div><h3>浏览器设备与统一审批</h3><p>选择身份和收发权限后，一次批准该设备及其关联扩展；不授予 Agent 权限。</p></div>
          <input className="input" aria-label="搜索浏览器设备" placeholder="搜索名称、ID 或状态"
            value={filter} onChange={e=>setFilter(e.target.value)}/>
        </div>
        <div className="table-wrap"><table><thead><tr>
          <th>设备</th><th>扩展来源</th><th>状态</th><th>身份 / 权限</th><th>创建时间</th><th>操作</th>
        </tr></thead><tbody>
          {visible.length?visible.map(d=>{
            const caps=capabilities[d.id]||{send:true,receive:false};
            return <tr key={d.id}>
              <td><strong>{d.name||'Chrome'}</strong><small style={{display:'block',wordBreak:'break-all'}}>
                {d.id}</small></td>
              <td><small style={{display:'block',wordBreak:'break-all'}}>
                {d.extensionId||'旧版设备'}</small>
                <span className="badge">{d.extensionState==='pending'?'待统一审批':
                  d.extensionState==='approved'?'扩展已批准':
                  d.extensionState==='revoked'?'扩展已撤销':'—'}</span></td>
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
                  (!caps.send&&!caps.receive)} onClick={()=>action(d.id,'approve')}>{d.extensionId?'批准扩展与设备':'批准设备'}</button>:
                d.state==='approved'?<button className="btn" disabled={!!busy}
                  onClick={()=>action(d.id,'revoke')}>撤销授权</button>:
                  <span>已失效</span>}</td>
            </tr>;
          }):<tr><td colSpan={6}>{loading?'正在加载…':'暂无匹配的浏览器设备'}</td></tr>}
        </tbody></table></div>
      </div>
      <div className="config-help">撤销只会阻止 RelayProxy 后续同步，
        不会撤销网站服务器已经签发的会话。需要彻底退出时，应使用网站自身的“退出所有设备”。</div>
    </>}
  </section>;
}
