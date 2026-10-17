// Client-side drag checks are UX safeguards, never a substitute for the
// authenticated shared-root ACL checks on the hosting Agent.
export function validRemotePath(path,{root=false}={}){
 if(root&&path==='.')return true;
 return typeof path==='string'&&path.length>0&&!path.startsWith('/')&&
  !path.includes('\\')&&path.split('/').every(part=>part!==''&&part!=='.'&&part!=='..'&&!part.startsWith('.relayproxy-upload-'));
}
export function planSameShareMoves(source,current,target,locallyInitiated){
 if(!locallyInitiated||!source||!current||!target||
    source.device!==current.device||source.share!==current.share||
    source.device!==target.device||source.share!==target.share)
  throw new Error('不支持跨设备、跨共享或来源不明的拖拽移动');
 if(!validRemotePath(source.path,{root:true})||!validRemotePath(target.path,{root:true})||
    !Array.isArray(source.names)||source.names.length===0||source.names.length>5000)
  throw new Error('无效的移动来源或目标');
 const seen=new Set();
 return source.names.map(name=>{
  if(!validRemotePath(name)||name.includes('/')||seen.has(name))throw new Error('无效或重复的文件名称');
  seen.add(name);
  const from=source.path==='.'?name:source.path+'/'+name;
  const to=target.path==='.'?name:target.path+'/'+name;
  if(from===to||target.path===from||target.path.startsWith(from+'/'))
   throw new Error('不能将文件移动到当前位置、自身或子目录');
  return {source:from,destination:to};
 });
}

// A context menu targets the clicked entry. Existing multi-selection is
// retained only if the entry already belongs to that selection.
export function contextSelection(clicked,selectedNames,entries){
 if(!clicked||typeof clicked.name!=='string')return [];
 const names=Array.isArray(selectedNames)&&selectedNames.includes(clicked.name)
  ?new Set(selectedNames):new Set([clicked.name]);
 return (Array.isArray(entries)?entries:[]).filter(x=>names.has(x.name));
}
