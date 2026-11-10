// Session Storage is scoped to one browsing context. After restoring a
// website session, activate that same tab rather than creating another tab
// with an empty sessionStorage. Never navigate to an unrelated origin; HTTP and HTTPS remain distinct.
export async function reloadRestoredWebsiteTab(tabId,siteOrigin){
  const matches=tab=>{
    try{return tab&&!tab.incognito&&new URL(tab.pendingUrl||tab.url).origin===siteOrigin;}
    catch{return false;}
  };
  if(!matches(await chrome.tabs.get(tabId)))throw new Error('Storage 目标标签页已经跳转');
  await new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>finish(new Error('Storage 目标页面刷新超时')),15000);
    let ended=false,reloading=true;
    function finish(error){
      if(ended)return;
      ended=true;clearTimeout(timer);
      chrome.tabs.onUpdated.removeListener(updated);chrome.tabs.onRemoved.removeListener(removed);
      error?reject(error):resolve();
    }
    async function check(){
      try{
        const tab=await chrome.tabs.get(tabId);
        if(!matches(tab))return finish(new Error('Storage 目标页面刷新后已跳转'));
        if(tab.status==='complete')finish();
      }catch{finish(new Error('Storage 目标标签页已关闭'));}
    }
    const updated=(id,info)=>{if(id===tabId&&info.status==='complete'&&!reloading)void check();};
    const removed=id=>{if(id===tabId)finish(new Error('Storage 目标标签页已关闭'));};
    chrome.tabs.onUpdated.addListener(updated);chrome.tabs.onRemoved.addListener(removed);
    Promise.resolve().then(()=>chrome.tabs.reload(tabId))
      .then(()=>{reloading=false;void check();},finish);
  });
}

export async function showRestoredWebsiteTab(tabId, siteOrigin) {
  if (!Number.isSafeInteger(tabId) || tabId < 0) {
    throw new Error('Restored tab identifier is invalid');
  }
  const tab = await chrome.tabs.get(tabId);
  if (!tab || tab.incognito || new URL(tab.url).origin !== siteOrigin) {
    throw new Error('Restored tab is missing or navigated away from the approved website');
  }
  await chrome.tabs.update(tabId, {active: true});
  if (Number.isSafeInteger(tab.windowId)) {
    await chrome.windows.update(tab.windowId, {focused: true});
  }
  return tabId;
}
