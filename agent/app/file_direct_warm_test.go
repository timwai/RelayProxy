package app

import (
 "context"
 "testing"
 "time"
)

func TestPrewarmRequiresAuthenticatedFileSharingSession(t *testing.T) {
 agent:=&Agent{}
 if _,err:=agent.PrewarmRemoteFileDirect(context.Background(),"peer","share");err==nil {
  t.Fatal("warmup without files.client capability or Relay session must fail")
 }
 for _,target:=range [][2]string{{"","share"},{"peer",""},{"peer","    "}} {
  if _,err:=agent.PrewarmRemoteFileDirect(context.Background(),target[0],target[1]);err==nil {
   t.Fatalf("invalid warmup target accepted: %q %q",target[0],target[1])
  }
 }
}

func TestFileWarmHintsAreScopedToDeviceShareAndSessionEpoch(t *testing.T) {
 agent:=&Agent{epoch:17}
 agent.fileWarmHints=map[string]fileDirectWarmHint{
  "host\x00share":{state:"relay",epoch:17,until:time.Now().Add(time.Second)},
  "host\x00other":{state:"ready",epoch:17,until:time.Now().Add(time.Second)},
  "expired\x00share":{state:"relay",epoch:17,until:time.Now().Add(-time.Second)},
  "prior\x00share":{state:"relay",epoch:16,until:time.Now().Add(time.Minute)},
 }
 if !agent.fileDirectRecentlyUnreachable("host","share"){t.Fatal("failed direct probe not respected")}
 for _,scope:=range [][2]string{{"host","other"},{"expired","share"},{"prior","share"},{"stranger","share"}} {
  if agent.fileDirectRecentlyUnreachable(scope[0],scope[1]){
   t.Fatalf("unrelated, expired or prior-session hint suppressed direct: %v",scope)
  }
 }
 agent.mu.Lock()
 agent.epoch=18
 agent.mu.Unlock()
 if agent.fileDirectRecentlyUnreachable("host","share"){t.Fatal("stale epoch suppressed a new Relay session")}
}
