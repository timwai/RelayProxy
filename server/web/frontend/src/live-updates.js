// Shared realtime invalidation entry point for the Server React console.
// The SSE stream contains only a monotonic revision (no credentials, device
// metadata, or session payloads). Canonical data always comes from the
// existing identity-filtered API endpoints.
const subscribers=new Set();
let stream=null;
let fallback=null;
let refreshTimer=null;
let connectedOnce=false;

function fire(){
  if(refreshTimer!==null)return;
  refreshTimer=setTimeout(()=>{
    refreshTimer=null;
    for(const fn of [...subscribers]){
      try{fn()}catch{ /* Each subscriber owns its async errors. */ }
    }
  },180);
}

function visible(){
  return typeof document==='undefined'||document.visibilityState==='visible';
}

function onVisibility(){
  if(visible())fire();
}

function start(){
  if(typeof window==='undefined'||subscribers.size===0)return;
  if(typeof EventSource!=='undefined'){
    stream=new EventSource('/api/v1/live/events',{withCredentials:true});
    stream.addEventListener('invalidate',fire);
    stream.addEventListener('open',()=>{
      if(connectedOnce)fire(); // Lost events during a disconnect: re-read.
      connectedOnce=true;
    });
  }
  // A single fallback for browsers without SSE, proxy disconnects and
  // process restarts. Never clear last good data just because SSE is down.
  fallback=setInterval(()=>{if(visible())fire();},45000);
  window.addEventListener('focus',onVisibility);
  document.addEventListener('visibilitychange',onVisibility);
  window.addEventListener('online',onVisibility);
}

function stop(){
  if(stream){stream.close();stream=null}
  if(fallback!==null){clearInterval(fallback);fallback=null}
  if(refreshTimer!==null){clearTimeout(refreshTimer);refreshTimer=null}
  if(typeof window!=='undefined'){
    window.removeEventListener('focus',onVisibility);
    document.removeEventListener('visibilitychange',onVisibility);
    window.removeEventListener('online',onVisibility);
  }
  connectedOnce=false;
}

export function subscribeLiveUpdates(listener){
  if(typeof listener!=='function')throw new TypeError('listener required');
  const first=subscribers.size===0;
  subscribers.add(listener);
  if(first)start();
  return ()=>{
    subscribers.delete(listener);
    if(subscribers.size===0)stop();
  };
}
