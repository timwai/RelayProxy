// Pending browser-device approval is polled by Chrome's existing 1-minute
// MV3 alarm. Registration starts ONLY after an explicit Settings user click;
// a pending registration never grants send/receive or WSS privileges.
import {getOrCreateIdentity} from './device-identity.js';
import {getSettings} from './rules.js';
import {registerBrowser} from './server-api.js';

export const PENDING_ENROLLMENT_KEY='browserSyncPendingEnrollment';
export const ENROLLMENT_POLL_MS=60*1000;

export function enrollmentPollDue(entry,now){
  return !!entry && Number.isFinite(now) &&
    (!Number.isFinite(entry.lastCheckedAt) ||
      entry.lastCheckedAt>now ||
      now-entry.lastCheckedAt>=ENROLLMENT_POLL_MS);
}

export function validPendingEnrollment(entry,settings,deviceId){
  return !!entry && typeof entry==='object' &&
    !!settings?.serverOrigin && entry.serverOrigin===settings.serverOrigin &&
    !!settings.requestedIdentityId &&
    entry.requestedIdentityId===settings.requestedIdentityId &&
    !!settings.deviceName && entry.deviceName===settings.deviceName &&
    entry.deviceId===deviceId;
}

export async function recordEnrollment(result){
  const state=result?.state;
  if(state!=='pending'){
    await chrome.storage.local.remove(PENDING_ENROLLMENT_KEY);
    return;
  }
  const [settings,identity]=await Promise.all([getSettings(),getOrCreateIdentity()]);
  if(!settings.serverOrigin||!settings.requestedIdentityId||!settings.deviceName)
    throw new Error('身份 ID 和设备名称必须填写');
  await chrome.storage.local.set({[PENDING_ENROLLMENT_KEY]:{
    serverOrigin:settings.serverOrigin,deviceId:identity.deviceId,
    requestedIdentityId:settings.requestedIdentityId,deviceName:settings.deviceName,
    lastCheckedAt:Date.now()
  }});
}

let pendingCheck=null;
export function pollPendingEnrollment({force=false}={}){
  if(pendingCheck)return pendingCheck;
  const task=(async()=>{
    const entry=(await chrome.storage.local.get(PENDING_ENROLLMENT_KEY))[PENDING_ENROLLMENT_KEY];
    if(!entry)return {state:'none'};
    const [settings,identity]=await Promise.all([getSettings(),getOrCreateIdentity()]);
    if(!validPendingEnrollment(entry,settings,identity.deviceId)){
      // Never poll/register a different Server, Device, or requested identity
      // based on an old outstanding enrollment. Require a fresh user click.
      await chrome.storage.local.remove(PENDING_ENROLLMENT_KEY);
      return {state:'invalid'};
    }
    const now=Date.now();
    if(!force&&!enrollmentPollDue(entry,now))return {state:'pending'};
    // Persist before fetching: worker suspension, network failure or repeated
    // alarms cannot flood the unprivileged registration endpoint.
    await chrome.storage.local.set({[PENDING_ENROLLMENT_KEY]:{
      ...entry,lastCheckedAt:now
    }});
    try{
      const response=await registerBrowser();
      if(response.state==='approved'){
        await chrome.storage.local.remove(PENDING_ENROLLMENT_KEY);
        return {state:'approved'};
      }
      if(response.state==='pending')return {state:'pending'};
      await chrome.storage.local.remove(PENDING_ENROLLMENT_KEY);
      return {state:response.state||'denied'};
    }catch{
      // Fail closed. Retain the submitted enrollment for the next alarm;
      // never fall through to WSS based on a failed anonymous HTTP probe.
      return {state:'retry'};
    }
  })();
  pendingCheck=task;
  task.finally(()=>{if(pendingCheck===task)pendingCheck=null;}).catch(()=>{});
  return task;
}
