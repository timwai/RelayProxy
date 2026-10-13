// Session Storage is scoped to one browsing context. After restoring a
// website session, activate that same tab rather than creating another tab
// with an empty sessionStorage. Never navigate to an unrelated origin; HTTP and HTTPS remain distinct.
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
