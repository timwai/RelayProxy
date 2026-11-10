// Persist only fixed categories. Chrome/page exception text may contain tokens,
// URLs or Cookie values, so never retain it in a transfer status or send it out.
const stages={context:'检查配对与权限',source_tab:'选择发送端页面',
  capture_cookies:'采集 Cookie',capture_storage:'采集 Local / Session Storage',
  capture_indexeddb:'采集 IndexedDB',encrypt:'加密快照',send:'发送快照',
  decrypt:'解密快照',network:'准备网络',preflight_storage:'检查目标 Storage',
  preflight_indexeddb:'检查目标 IndexedDB',apply_indexeddb:'写入 IndexedDB',
  apply_storage:'写入 Local / Session Storage',apply_cookies:'写入 Cookie',
  commit_storage:'核验 Storage',commit_indexeddb:'核验 IndexedDB',
  verify_site:'核验目标页面',remote_restore:'接收端恢复'};
const reasons={STORAGE_ACCESS_DENIED:'Chrome 拒绝访问页面 Storage',
  RATE_LIMIT:'同步请求过于频繁，请稍后重试',
  STORAGE_QUOTA:'目标页面 Storage 空间不足',STORAGE_READ_FAILED:'页面 Storage 读取失败',
  STORAGE_WRITE_FAILED:'页面 Storage 写入失败',STORAGE_CHANGED:'页面脚本在同步期间修改了 Storage',
  DOCUMENT_CHANGED:'目标页面已刷新、跳转或关闭',TAB_NOT_READY:'网站标签页尚未加载完成',
  TAB_AMBIGUOUS:'同站点有多个标签页，请先激活要同步的页面',
  HOST_PERMISSION:'站点或 Cookie 权限不完整，请在站点授权中补全权限',
  COOKIE_REJECTED:'Chrome 拒绝写入 Cookie',COOKIE_MISMATCH:'Cookie 写入后的属性或值与快照不一致',
  COOKIE_DELETE_FAILED:'Cookie 删除失败',RESTORE_PAUSED:'接收端因上次恢复未完成而暂停，请在接收端检查并恢复规则',
  STORAGE_LIMIT:'完整 Storage 超过同步上限，未截断任何键',
  STORAGE_UNSTABLE:'来源页面持续修改 Storage，请等待页面稳定后重试'};
const codes={PARTIAL_ROLLBACK:'恢复或回滚不完整，接收端已暂停',CONFLICT:'目标已有不同登录状态',
  REPLAY_REJECTED:'旧快照已拒绝',INDEXEDDB_FAILURE:'IndexedDB 恢复失败',
  COOKIE_FAILURE:'Cookie 恢复失败',STORAGE_FAILURE:'Storage 读写失败',
  DECRYPT_FAILED:'快照解密或签名验证失败',NETWORK_UNAVAILABLE:'同步网络不可用',UNSPECIFIED:'未分类错误'};
export function failureCode(error){
  const message=String(error?.message||'');
  if(message.startsWith('PARTIAL_ROLLBACK:'))return 'PARTIAL_ROLLBACK';
  if(message.startsWith('CONFLICT:'))return 'CONFLICT';
  if(/rate limit/i.test(message))return 'NETWORK_UNAVAILABLE';
  if(/旧会话快照|replay|sequence/i.test(message))return 'REPLAY_REJECTED';
  if(/IndexedDB|indexeddb/i.test(message))return 'INDEXEDDB_FAILURE';
  if(/cookie.{0,60}(?:failed|失败|rejected|denied|回滚|写入)|(?:failed|失败).{0,60}cookie/i.test(message))return 'COOKIE_FAILURE';
  if(/storage/i.test(message))return 'STORAGE_FAILURE';
  if(/解密|decrypt|signature/i.test(message))return 'DECRYPT_FAILED';
  if(/网络|proxy|PAC|出口|connect|network/i.test(message))return 'NETWORK_UNAVAILABLE';
  return 'UNSPECIFIED';
}
export function syncFailure(message,reason,cause){
  const error=new Error(message,{cause});
  if(Object.hasOwn(reasons,reason))error.syncReason=reason;
  return error;
}
export function safeDiagnostic(value){
  if(!value||typeof value!=='object')return {};
  return {...(Object.hasOwn(codes,value.errorCode)?{errorCode:value.errorCode}:{}),
    ...(Object.hasOwn(stages,value.stage)?{stage:value.stage}:{}),
    ...(Object.hasOwn(reasons,value.reason)?{reason:value.reason}:{})};
}
export function failureDiagnostic(error,stage){
  let reason;
  for(let current=error,depth=0;current&&depth<6;current=current.cause,depth++){
    if(Object.hasOwn(reasons,current.syncReason)){reason=current.syncReason;break;}
    const message=String(current.message||'');
    if(/rate limit/i.test(message))reason='RATE_LIMIT';
    else if(/上次.*(?:中断|未完成)|待人工检查/.test(message))reason='RESTORE_PAUSED';
    else if(/权限/.test(message))reason='HOST_PERMISSION';
    else if(/多个 Storage 标签页/.test(message))reason='TAB_AMBIGUOUS';
    else if(/尚未就绪|尚未加载|打开目标站点超时/.test(message))reason='TAB_NOT_READY';
    else if(/刷新|跳转|导航已变化|标签页已关闭/.test(message))reason='DOCUMENT_CHANGED';
    else if(/采集期间持续变化/.test(message))reason='STORAGE_UNSTABLE';
    else if(/Storage.*(?:已被网站修改|被目标网站脚本修改|键值不一致)/.test(message))reason='STORAGE_CHANGED';
    else if(/Storage.*(?:上限|超限|符合限制)/.test(message))reason='STORAGE_LIMIT';
    if(reason)break;
  }
  return safeDiagnostic({errorCode:failureCode(error),stage,reason});
}
export function diagnosticText(value){
  const safe=safeDiagnostic(value);
  const parts=[stages[safe.stage],reasons[safe.reason]||codes[safe.errorCode]].filter(Boolean);
  return parts.join('：');
}
export const TRANSFER_STATUS_KEYS=['browserSyncTransferStatus','browserSyncRestorePaused',
  'browserSyncRestoreJournal','browserSyncStorageRestoreJournal','browserSyncIndexedDBRestoreJournal'];
export function ruleTransferStatus(ruleId,stored){
  const previous=stored.browserSyncTransferStatus?.[ruleId];
  const paused=TRANSFER_STATUS_KEYS.slice(1).some(key=>stored[key]?.[ruleId]);
  if(!previous&&!paused)return null;
  return {...(previous||{time:Date.now(),reason:'RESTORE_PAUSED'}),
    ...(paused?{state:'PARTIAL'}:{})};
}
