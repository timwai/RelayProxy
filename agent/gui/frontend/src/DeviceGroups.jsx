import React,{useState} from 'react';
import {Badge,Button,Field,Input,Modal,Select} from './App.jsx';

const arr=v=>Array.isArray(v)?v:[];
const newGroupID=()=>'group:'+(globalThis.crypto?.randomUUID?.()||Date.now().toString(36)+'-'+Math.random().toString(36).slice(2));

export default function DeviceGroups({config,exits,setv,save,dirty,toast,selectedExit,onSelectExit}){
 const groups=arr(config.routing?.device_groups);
 const [editing,setEditing]=useState(null);
 const idOf=e=>e.deviceId||e.id;
 const refs=id=>[(selectedExit??config.defaultExitId)===id&&'默认出口',config.upstreamExitId===id&&'共享上游',config.gateway?.exit_id===id&&'网关默认出口',config.routing?.dns_exit_id===id&&'DNS 出口',...arr(config.routing?.rules).filter(r=>r.exit_id===id).map(r=>'规则 '+r.name),...arr(config.routing?.subscriptions).filter(r=>r.exit_id===id).map(r=>'订阅 '+r.name)].filter(Boolean);
 const name=id=>exits.find(e=>idOf(e)===id)?.name||id;
 const online=id=>exits.some(e=>idOf(e)===id&&e.online!==false);
 const update=next=>setv('routing.device_groups',next);
 const commit=()=>{
  const g={...editing,name:editing.name.trim()};
  if(!g.name||!g.members.length){toast('请填写分组名称并至少选择一个设备','warn');return}
  if(groups.some(x=>x.id!==g.id&&x.name.toLowerCase()===g.name.toLowerCase())){toast('分组名称已存在','warn');return}
  update(groups.some(x=>x.id===g.id)?groups.map(x=>x.id===g.id?g:x):[...groups,g]);
  setEditing(null);
 };
 const move=(i,delta)=>{const a=[...editing.members];[a[i],a[i+delta]]=[a[i+delta],a[i]];setEditing({...editing,members:a})};
 // The heading intentionally avoids `.card-head`: PageHead portals the Device
 // Center's page actions into the first card heading it finds.
 return <section className="card device-groups">
  <div className="device-groups-head"><div><div className="eyebrow">DEVICE GROUPS</div><h2>设备分组</h2></div><Button onClick={()=>setEditing({id:newGroupID(),name:'',members:[]})}>新增分组</Button></div>
  <div className="mini">按成员顺序选择首个在线且已授权的出口，连接失败时尝试下一成员。已有 TCP 连接与 UDP 会话保持原出口；主设备恢复后，新连接优先使用主设备。</div>
  {groups.map(g=>{const used=refs(g.id),ready=g.members.some(online),isDefault=(selectedExit??config.defaultExitId)===g.id;return <div className="setting" key={g.id}>
   <div className="grow"><b>{g.name}</b>{isDefault&&<Badge tone="blue">默认出口</Badge>}<div className="mini">{g.members.map(name).join(' → ')}</div><div className="mini">{used.join(' · ')||'尚未引用'}</div></div>
   <Badge tone={ready?'ok':'neutral'}>{ready?'可用':'无在线成员'}</Badge>
   <div className="actions">{onSelectExit&&!isDefault&&<Button quiet disabled={!ready||dirty} title={dirty?'请先保存或放弃当前修改':'将此分组设为默认出口，立即生效'} onClick={()=>onSelectExit(g.id)}>设为默认</Button>}<Button quiet onClick={()=>setEditing({...g,members:[...g.members]})}>编辑</Button><Button quiet danger disabled={used.length>0} title={used.length?'请先解除引用':''} onClick={()=>update(groups.filter(x=>x.id!==g.id))}>删除</Button></div>
  </div>})}
  {!groups.length&&<div className="empty">尚无设备分组。创建后可在默认出口、分流规则、订阅、DNS 出口和局域网网关中选择。</div>}
  {dirty&&<div className="actions end top-gap"><Button primary onClick={()=>save({routing:{device_groups:groups}})}>保存设备分组</Button></div>}
  <Modal open={!!editing} title={editing&&groups.some(x=>x.id===editing.id)?'编辑设备分组':'新增设备分组'} onClose={()=>setEditing(null)} footer={<><Button onClick={()=>setEditing(null)}>取消</Button><Button primary onClick={commit}>确定</Button></>}>
   {editing&&<>
    <Field label="分组名称" help="同名分组不区分大小写，最多 64 个字符"><Input maxLength={64} value={editing.name} onChange={e=>setEditing({...editing,name:e.target.value})}/></Field>
    {/* A div, not a <label>: a label would forward clicks to the first button inside. */}
    <div className="field" role="group" aria-label="成员与优先级"><span>成员与优先级</span>
     <div className="device-group-members">
      {editing.members.map((id,i)=><div className="setting" key={id}>
       <span className="grow">{i+1}. {name(id)}{online(id)?'':' · 不可用'}</span>
       <div className="actions"><Button quiet disabled={i===0} aria-label={'上移 '+name(id)} onClick={()=>move(i,-1)}>↑</Button><Button quiet disabled={i===editing.members.length-1} aria-label={'下移 '+name(id)} onClick={()=>move(i,1)}>↓</Button><Button quiet danger onClick={()=>setEditing({...editing,members:editing.members.filter(x=>x!==id)})}>移除</Button></div>
      </div>)}
      {!editing.members.length&&<div className="mini">尚未添加成员。</div>}
     </div>
     <small>从上到下为优先级；一台设备可加入多个分组，仅可选择 Server 已授权给本机的出口。</small>
    </div>
    <Field label="添加设备"><Select value="" onChange={e=>{if(e.target.value)setEditing({...editing,members:[...editing.members,e.target.value]})}}>
     <option value="">选择设备…</option>
     {exits.filter(e=>!editing.members.includes(idOf(e))).map(e=><option key={idOf(e)} value={idOf(e)}>{e.name||idOf(e)}{e.online===false?' · 离线':''}</option>)}
    </Select></Field>
   </>}
  </Modal>
 </section>;
}
