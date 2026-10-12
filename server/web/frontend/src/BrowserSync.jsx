import React,{useCallback,useEffect,useMemo,useState} from 'react';
import {api,json,fmtDate,list} from './api.js';

// This admin page displays metadata only. Website origins, policies, secrets,
// encrypted payloads and browser public/private keys are never rendered here.
export function BrowserSyncAdmin({identities=[]}){
  const [devices,setDevices]=useState([]);
  const [extensions,setExtensions]=useState([]);
  const [rules,setRules]=useState([]);
  const [ruleNextOffset,setRuleNextOffset]=useState(-1);
  const [presence,setPresence]=useState({online:{},stats:[]});
  const [audit,setAudit]=useState({items:[],nextCursor:0});
  const [loading,setLoading]=useState(true);
  const [unavailable,setUnavailable]=useState(false);
  const [error,setError]=useState('');
  const [notice,setNotice]=useState('');
  const [busy,setBusy]=useState('');
  const [capabilities,setCapabilities]=useState({});
  const [view,setView]=useState('devices');
  const [filter,setFilter]=useState('');
  const [stateFilter,setStateFilter]=useState('all');
  const [ruleFilter,setRuleFilter]=useState('all');
  const [ruleSearch,setRuleSearch]=useState('');
  const [ruleSearchDebounced,setRuleSearchDebounced]=useState('');
  const [ruleIdentity,setRuleIdentity]=useState('');
  const [auditFilter,setAuditFilter]=useState('all');
  const [auditDevice,setAuditDevice]=useState('');
  const activeIdentities=useMemo(()=>list(identities).filter(x=>{
    const state=x.status??x.state;
    return !state||state==='active';
  }),[identities]);

  useEffect(()=>{
    const id=setTimeout(()=>setRuleSearchDebounced(ruleSearch.trim()),300);
    return ()=>clearTimeout(id);
  },[ruleSearch]);
  const rulePagePath=useCallback(offset=>'/browser-sync/admin/rules?limit=50&offset='+offset+
    (ruleFilter==='all'?'':'&status='+encodeURIComponent(ruleFilter))+
    (ruleIdentity?'&identityId='+encodeURIComponent(ruleIdentity):'')+
    (ruleSearchDebounced?'&q='+encodeURIComponent(ruleSearchDebounced):''),
    [ruleFilter,ruleIdentity,ruleSearchDebounced]);
  const refresh=useCallback(async(silent=false)=>{
    if(!silent){setLoading(true);setError('');}
    try{
      const [deviceData,extensionData,ruleData,statusData,auditData]=await Promise.all([
        api('/browser-sync/admin/devices'),
        api('/browser-sync/admin/extensions'),
        api(rulePagePath(0)),
        api('/browser-sync/admin/status'),
        api('/browser-sync/admin/audit?limit=50'+(auditFilter==='all'?'':'&type='+encodeURIComponent(auditFilter))+
          (auditDevice?'&device='+encodeURIComponent(auditDevice):''))
      ]);
      setDevices(list(deviceData));
      setExtensions(list(extensionData));
      setRules(list(ruleData?.items));
      setRuleNextOffset(Number.isInteger(ruleData?.nextOffset)?ruleData.nextOffset:-1);
      setPresence({online:statusData?.online||{},stats:list(statusData?.stats)});
      setAudit({items:list(auditData?.items),nextCursor:auditData?.nextCursor||0});
      setUnavailable(false);
    }catch(err){
      if(err.status===404){setUnavailable(true);if(!silent){setDevices([]);setRules([]);}}
      else setError(err.message);
    }finally{if(!silent)setLoading(false);}
  },[auditFilter,auditDevice,rulePagePath]);
  useEffect(()=>{
    refresh();
    // Poll only transient presence. A failed poll must not clear the last
    // successfully loaded authorization and audit tables.
    const id=setInterval(async()=>{
      try{
        const result=await api('/browser-sync/admin/status');
        setPresence({online:result?.online||{},stats:list(result?.stats)});
      }catch{ /* The manual refresh path reports administrative API errors. */ }
    },15000);
    return ()=>clearInterval(id);
  },[refresh]);

  // The requested identity is immutable once the browser enrolls.
  // Administrators review/reject that binding; they cannot silently assign
  // the pending device to a different identity from this screen.
  const requestedIdentity=id=>devices.find(d=>d.id===id)?.requestedIdentityId||'';
  const validIdentity=id=>activeIdentities.some(item=>item.id===requestedIdentity(id));
  const deviceAction=async(id,mode)=>{
    if(busy)return;
    if(mode==='revoke'&&!window.confirm('撤销后该浏览器将不能继续同步，相关配对规则也会失效。确定撤销？'))return;
    if(mode==='delete'&&!window.confirm('永久删除已撤销设备及其配对记录？此操作不可恢复，重新注册仍须管理员审批。'))return;
    if(mode==='approve'&&!validIdentity(id)){setError('设备提交的身份 ID 不存在或未启用；请检查设备的身份配置。');return;}
    setBusy(id);setError('');setNotice('');
    try{
      if(mode==='approve'){
        const caps=capabilities[id]||{send:true,receive:false};
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/approve',
          json('POST',{identityId:requestedIdentity(id),send:!!caps.send,receive:!!caps.receive}));
        setNotice('设备已批准，可以连接中继。');
      }else if(mode==='delete'){
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id),{method:'DELETE'});
        setNotice('已删除旧设备及其配对记录。');
      }else{
        await api('/browser-sync/admin/devices/'+encodeURIComponent(id)+'/revoke',json('POST',{}));
        setNotice('设备及相关配对已撤销。');
      }
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const ruleAction=async(rule,mode)=>{
    if(busy)return;
    if(mode==='revoke'&&!window.confirm('撤销后无法恢复这条配对规则，双方需要重新配对。确定撤销？'))return;
    if(mode==='pause'&&!window.confirm('暂停后将阻止该规则的后续会话同步，确定暂停？'))return;
    setBusy(rule.id);setError('');setNotice('');
    try{
      await api('/browser-sync/admin/rules/'+encodeURIComponent(rule.id)+'/'+mode,json('POST',{}));
      setNotice(mode==='pause'?'规则已暂停。':mode==='resume'?'规则已恢复。':'规则已撤销。');
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const removeLegacyBlock=async id=>{
    if(busy||!window.confirm('清除此历史扩展禁用记录？需先撤销并删除关联设备。新设备仍需管理员审批。'))return;
    setBusy(id);setError('');setNotice('');
    try{
      await api('/browser-sync/admin/extensions/'+encodeURIComponent(id),{method:'DELETE'});
      setNotice('历史禁用来源已清理。');
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const moreRules=async()=>{
    if(busy||ruleNextOffset<0)return;
    setBusy('rules');setError('');
    try{
      const result=await api(rulePagePath(ruleNextOffset));
      setRules(old=>[...old,...list(result?.items).filter(item=>!old.some(r=>r.id===item.id))]);
      setRuleNextOffset(Number.isInteger(result?.nextOffset)?result.nextOffset:-1);
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const moreAudit=async()=>{
    if(busy||!audit.nextCursor)return;
    setBusy('audit');setError('');
    try{
      const result=await api('/browser-sync/admin/audit?limit=50&before='+encodeURIComponent(audit.nextCursor)+
        (auditFilter==='all'?'':'&type='+encodeURIComponent(auditFilter))+
        (auditDevice?'&device='+encodeURIComponent(auditDevice):''));
      setAudit(old=>({
        items:[...old.items,...list(result?.items).filter(e=>!old.items.some(p=>p.id===e.id))],
        nextCursor:result?.nextCursor||0
      }));
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };

  const clearAudit=async()=>{
    if(busy||!window.confirm('将永久清除浏览器同步的历史审计记录，仅保留本次清理操作。确定继续？'))return;
    setBusy('audit-clear');setError('');setNotice('');
    try{
      const result=await api('/browser-sync/admin/audit',{method:'DELETE'});
      setNotice('已清理 '+(result?.deleted||0)+' 条审计记录，保留了本次操作留痕。');
      await refresh();
    }catch(err){setError(err.message);}
    finally{setBusy('');}
  };
  const auditExportURL='/api/v1/browser-sync/admin/audit/export'+
    ((auditFilter==='all'&&!auditDevice)?'':
      '?'+[auditFilter==='all'?'':'type='+encodeURIComponent(auditFilter),
        auditDevice?'device='+encodeURIComponent(auditDevice):''].filter(Boolean).join('&'));
  const online=presence.online||{};
  const stats=new Map(list(presence.stats).map(s=>[s.deviceId,s]));
  const activeCount=devices.filter(d=>d.state==='approved'&&online[d.id]).length;
  const visible=devices.filter(d=>
    (stateFilter==='all'||d.state===stateFilter||(stateFilter==='online'&&d.state==='approved'&&!!online[d.id]))&&
    [d.name,d.id,d.identityId,d.requestedIdentityId,d.state,d.extensionId].join(' ').toLowerCase()
      .includes(filter.trim().toLowerCase()))
    .sort((a,b)=>({pending:0,approved:1,revoked:2}[a.state]??3)-({pending:0,approved:1,revoked:2}[b.state]??3));
  const visibleRules=rules;
  const visibleAudit=list(audit.items);
  const linkedExtensions=new Set(devices.filter(d=>d.extensionId).map(d=>d.extensionId));
  const count=state=>devices.filter(d=>d.state===state).length;
  const states=[['all','全部',devices.length],['pending','待审批',count('pending')],
    ['approved','已批准',count('approved')],['revoked','已撤销',count('revoked')]];
  const ruleStates=[['all','全部'],['offered','待确认'],['active','已生效'],['paused','已暂停'],['revoked','已撤销']];
  const ruleStatus={offered:'待确认',active:'已生效',paused:'已暂停',revoked:'已撤销'};
  const diagnosticText={
    UNSPECIFIED:'其他失败',CONFLICT:'会话冲突',PARTIAL_ROLLBACK:'部分回滚',
    NETWORK_UNAVAILABLE:'网络或出口不可用',REPLAY_REJECTED:'重复或过期快照',
    DECRYPT_FAILED:'解密或签名失败',STORAGE_FAILURE:'Storage 恢复失败',
    INDEXEDDB_FAILURE:'IndexedDB 恢复失败',COOKIE_FAILURE:'Cookie 恢复失败'
  };
  const eventText={device_approved:'设备批准',device_revoked:'设备撤销',device_deleted:'设备删除',
    rule_paused:'规则暂停',rule_resumed:'规则恢复',rule_revoked:'规则撤销',delivery_result:'同步结果',audit_cleared:'审计清理'};
  return <section className="browser-sync-admin browser-sync-refined">
    <div className="page-head">
      <div><h1>浏览器同步</h1><p>设备授权、配对规则与同步运行记录</p></div>
      <div className="form-actions"><button className="btn" type="button"
        onClick={()=>refresh()} disabled={loading||!!busy}>刷新</button></div>
    </div>
    {notice&&<div className="config-help" role="status">{notice}</div>}
    {error&&<div className="err-notice" role="alert">{error}</div>}
    {unavailable?<div className="panel browser-sync-empty">
      <h3>浏览器同步暂不可用</h3>
      <p>请确认 Server 已更新，并在配置中启用 browser_sync.enabled。</p>
    </div>:<>
      <div className="browser-sync-summary">
        {states.slice(1).map(([id,label,total])=><button key={id} type="button"
          className={'browser-sync-stat '+(view==='devices'&&stateFilter===id?'selected':'')}
          onClick={()=>{setView('devices');setStateFilter(id);}}>
          <span>{label}</span><strong>{total}</strong></button>)}
        <button type="button" className={'browser-sync-stat '+(view==='devices'&&stateFilter==='online'?'selected':'')}
          onClick={()=>{setView('devices');setStateFilter('online');}}>
          <span>在线设备</span><strong>{activeCount}</strong></button>
      </div>
      <div className="browser-sync-mode-switch" role="group" aria-label="浏览器同步管理视图">
        {[['devices','浏览器设备'],['rules','配对规则'],['audit','同步记录']].map(([id,label])=>
          <button key={id} className={'btn '+(view===id?'primary':'')} type="button"
            aria-pressed={view===id} onClick={()=>setView(id)}>{label}</button>)}
      </div>
      {view==='devices'?<div className="panel">
        <div className="browser-sync-toolbar">
          <input className="input browser-sync-search" aria-label="搜索浏览器设备"
            placeholder="设备名称或 ID" value={filter} onChange={e=>setFilter(e.target.value)}/>
          <span className="muted">在线状态约每 15 秒更新</span>
        </div>
        <div className="browser-sync-filters" aria-label="按授权状态筛选">
          {states.map(([id,label,total])=><button key={id} type="button"
            className={'btn '+(stateFilter===id?'active':'')}
            aria-pressed={stateFilter===id}
            onClick={()=>setStateFilter(id)}>{label} <span>{total}</span></button>)}
        </div>
        <div className="table-wrap"><table>
          <thead><tr><th>浏览器设备</th><th>状态 / 最近活动</th><th>所属身份 / 权限</th><th>操作</th></tr></thead>
          <tbody>{visible.map(d=>{
            const caps=capabilities[d.id]||{send:true,receive:false};
            const stat=stats.get(d.id)||{};
            return <tr key={d.id}>
              <td><div className="browser-sync-device-name">{d.name||'Chrome 浏览器'}</div>
                <small className="browser-sync-device-id" title={d.id}>{d.id}</small>
                <small className="browser-sync-device-extra" title={d.extensionId||''}>
                  {d.extensionId?'扩展 · '+d.extensionId.slice(0,8)+'…':'旧版扩展'}</small></td>
              <td><span className={'badge '+(d.state==='approved'?'ok':d.state==='revoked'?'danger':'')}>
                {d.state==='pending'?'待审批':d.state==='approved'?'已批准':'已撤销'}</span>
                {d.state==='approved'&&<span className={'browser-sync-live '+(online[d.id]?'is-online':'')}>
                  {online[d.id]?'在线':'离线'}</span>}
                <small className="browser-sync-device-extra">最近应用：{fmtDate(stat.lastSyncAt)}</small>
                <small className="browser-sync-device-extra">
                  成功 {stat.applied||0} · 失败 {stat.failed||0} · 冲突 {stat.conflicted||0}</small>
                {d.extensionState==='revoked'&&<small className="browser-sync-device-extra">历史扩展来源已禁用</small>}</td>
              <td>{d.state==='pending'?<div className="browser-sync-approve-controls">
                <div className="browser-sync-requested-identity" title={d.requestedIdentityId||''}>
                  {d.requestedIdentityId
                    ? (activeIdentities.find(i=>i.id===d.requestedIdentityId)?.name||d.requestedIdentityId)
                    : '未提交身份 ID'}
                  {d.requestedIdentityId&&activeIdentities.some(i=>i.id===d.requestedIdentityId)
                    ? <small className="browser-sync-device-extra">申请身份 ID：{d.requestedIdentityId}</small>
                    : <small className="browser-sync-device-extra">此身份未启用，暂不可批准</small>}
                </div>
                <div className="browser-sync-permissions">
                  <label><input type="checkbox" checked={caps.send}
                    onChange={e=>setCapabilities(v=>({...v,[d.id]:{...caps,send:e.target.checked}}))}/>发送</label>
                  <label><input type="checkbox" checked={caps.receive}
                    onChange={e=>setCapabilities(v=>({...v,[d.id]:{...caps,receive:e.target.checked}}))}/>接收</label>
                </div>
              </div>:<><div>{activeIdentities.find(i=>i.id===d.identityId)?.name||d.identityId||'—'}</div>
                <small className="browser-sync-device-extra">{[d.send?'发送':null,d.receive?'接收':null].filter(Boolean).join(' / ')||'无同步权限'}</small></>}</td>
              <td className="browser-sync-actions">{d.state==='pending'?
                <button className="btn primary" type="button"
                  disabled={!!busy||!validIdentity(d.id)||(!caps.send&&!caps.receive)}
                  onClick={()=>deviceAction(d.id,'approve')}>批准</button>:
                d.state==='approved'?<button className="btn" type="button" disabled={!!busy}
                  onClick={()=>deviceAction(d.id,'revoke')}>撤销授权</button>:
                <button className="btn danger" type="button" disabled={!!busy}
                  onClick={()=>deviceAction(d.id,'delete')}>删除设备</button>}</td>
            </tr>;
          })}
          {!visible.length&&<tr><td colSpan={4} className="browser-sync-no-data">{loading?'读取中…':'暂无匹配设备'}</td></tr>}
          </tbody></table></div>
        {extensions.some(ext=>ext.state==='revoked')&&<section className="browser-sync-legacy-blocks">
          <div className="browser-sync-panel-title"><h3>历史禁用来源</h3></div>
          {extensions.filter(ext=>ext.state==='revoked').map(ext=><div key={ext.id} className="browser-sync-legacy-row">
            <span className="browser-sync-extension-id">{ext.id}</span>
            {linkedExtensions.has(ext.id)?<span>请先撤销并删除关联设备</span>:
              <button className="btn danger" type="button" disabled={!!busy}
                onClick={()=>removeLegacyBlock(ext.id)}>清除旧禁用记录</button>}
          </div>)}
        </section>}
      </div>:view==='rules'?<div className="panel">
        <div className="browser-sync-toolbar">
          <input className="input browser-sync-search" aria-label="搜索配对规则" placeholder="搜索设备或规则 ID"
            value={ruleSearch} onChange={e=>setRuleSearch(e.target.value)}/>
          <select className="input browser-sync-search" aria-label="按所属身份筛选配对规则"
            value={ruleIdentity} onChange={e=>setRuleIdentity(e.target.value)}>
            <option value="">全部身份</option>
            {activeIdentities.map(item=><option key={item.id} value={item.id}>{item.name||item.id}</option>)}
          </select>
          <div className="browser-sync-filters">
            {ruleStates.map(([id,label])=><button type="button" key={id}
              className={'btn '+(ruleFilter===id?'active':'')}
              aria-pressed={ruleFilter===id} onClick={()=>setRuleFilter(id)}>{label}</button>)}
          </div>
        </div>
        <div className="table-wrap"><table>
          <thead><tr><th>配对双方</th><th>状态</th><th>确认 / 最近修改</th><th>操作</th></tr></thead>
          <tbody>{visibleRules.map(r=><tr key={r.id}>
            <td><div className="browser-sync-device-name">{r.sourceName||r.sourceId} → {r.targetName||r.targetId}</div>
              <small className="browser-sync-device-id" title={r.id}>{r.id}</small>
              <small className="browser-sync-device-extra">身份：{r.identityId||'—'}</small></td>
            <td><span className={'badge '+(r.status==='active'?'ok':r.status==='revoked'?'danger':'')}>
              {ruleStatus[r.status]||r.status}</span></td>
            <td><div>来源批准：{fmtDate(r.sourceApprovedAt)}</div>
              <small className="browser-sync-device-extra">接收端批准：{fmtDate(r.targetApprovedAt)}</small>
              <small className="browser-sync-device-extra">密钥确认：{fmtDate(r.activatedAt)}</small>
              <small className="browser-sync-device-extra">最后修改：{fmtDate(r.updatedAt)}</small></td>
            <td className="browser-sync-actions">
              {r.status==='active'&&<button className="btn" type="button" disabled={!!busy}
                onClick={()=>ruleAction(r,'pause')}>暂停</button>}
              {r.status==='paused'&&<button className="btn" type="button" disabled={!!busy}
                onClick={()=>ruleAction(r,'resume')}>恢复</button>}
              {r.status!=='revoked'&&<button className="btn danger" type="button" disabled={!!busy}
                onClick={()=>ruleAction(r,'revoke')}>撤销</button>}
              {r.status==='revoked'&&<span className="muted">—</span>}
            </td></tr>)}
            {!visibleRules.length&&<tr><td colSpan={4} className="browser-sync-no-data">暂无匹配规则</td></tr>}
          </tbody>
        </table></div>
        <div className="browser-sync-audit-footer">
          <span className="muted">仅展示配对元数据，Server 无法读取站点策略和会话内容。</span>
          {ruleNextOffset>=0&&<button className="btn" type="button" disabled={!!busy}
            onClick={moreRules}>加载更多规则</button>}
        </div>
      </div>:<div className="panel">
        <div className="browser-sync-toolbar">
          <div className="browser-sync-filters">
            {[['all','全部'],['device_approved','授权'],['device_revoked','撤销设备'],
              ['delivery_result','同步结果'],['rule_revoked','撤销规则']].map(([id,label])=>
              <button type="button" key={id} className={'btn '+(auditFilter===id?'active':'')}
                aria-pressed={auditFilter===id} onClick={()=>setAuditFilter(id)}>{label}</button>)}
          </div>
          <select className="input browser-sync-search" aria-label="筛选浏览器设备"
            value={auditDevice} onChange={e=>setAuditDevice(e.target.value)}>
            <option value="">所有浏览器设备</option>
            {devices.map(d=><option key={d.id} value={d.id}>{d.name||d.id}</option>)}
          </select>
        </div>
        <div className="table-wrap"><table>
          <thead><tr><th>时间</th><th>事件</th><th>操作者</th><th>设备 / 配对</th><th>结果</th></tr></thead>
          <tbody>{visibleAudit.map(e=><tr key={e.id}>
            <td>{fmtDate(e.createdAt)}</td>
            <td>{eventText[e.type]||e.type}</td>
            <td><span className="browser-sync-audit-id">{e.actorId||'—'}</span></td>
            <td><div className="browser-sync-audit-id">{e.deviceId||'—'}{e.peerId?' → '+e.peerId:''}</div>
              {e.ruleId&&<small className="browser-sync-device-extra">规则：{e.ruleId}</small>}</td>
            <td><span className={'badge '+(e.outcome==='APPLIED'?'ok':e.outcome==='FAILED'||e.outcome==='CONFLICT'?'danger':'')}>
              {e.outcome||'—'}</span>
              {e.errorCode&&<small className="browser-sync-device-extra">{diagnosticText[e.errorCode]||'未分类失败'}</small>}</td>
          </tr>)}
          {!visibleAudit.length&&<tr><td colSpan={5} className="browser-sync-no-data">{loading?'读取中…':'暂无同步记录'}</td></tr>}
          </tbody></table></div>
        <div className="browser-sync-audit-footer">
          <span className="muted">只保存脱敏事件；最多保留 30 天或 5000 条，不含 Cookie 和同步密文。</span>
          <div className="browser-sync-audit-actions">
            <a className="btn" href={auditExportURL}>导出 CSV</a>
            {audit.nextCursor>0&&<button className="btn" type="button" disabled={!!busy} onClick={moreAudit}>加载更多</button>}
            <button className="btn danger" type="button" disabled={!!busy} onClick={clearAudit}>清理审计</button>
          </div>
        </div>
      </div>}
    </>}
  </section>;
}
