package bridge

import (
 "encoding/json"
 "strings"
 "testing"

 "relayproxy/internal/config"
)

func strictConfigUpdate(t *testing.T, raw string) ConfigUpdate {
 t.Helper()
 var update ConfigUpdate
 decoder:=json.NewDecoder(strings.NewReader(raw))
 decoder.DisallowUnknownFields()
 if err:=decoder.Decode(&update);err!=nil{t.Fatalf("legacy config payload rejected: %v",err)}
 return update
}

func TestLegacyUpstreamPasswordPresenceCannotEraseSecret(t *testing.T) {
 b:=newTestBridge(t)
 initial:=ConfigUpdate{}
 initial.Exit.Upstream.Mode=ptr("socks5")
 initial.Exit.Upstream.Address=ptr("proxy.example.test:1080")
 initial.Exit.Upstream.Username=ptr("alice")
 initial.Exit.Upstream.Password=ptr("existing-secret")
 if _,err:=b.SaveConfig(initial);err!=nil{t.Fatal(err)}

 // Previous Agent Web passed the read-only hasPassword together with its
 // redacted password input when saving unrelated exit settings.
 old:=strictConfigUpdate(t,`{"exit":{"upstream":{"mode":"socks5","address":"proxy.example.test:1080","username":"alice","hasPassword":true,"password":""}}}`)
 if _,err:=b.SaveConfig(old);err!=nil{t.Fatal(err)}
 saved,err:=config.LoadAgentConfig(b.configPath)
 if err!=nil{t.Fatal(err)}
 if saved.Exit.Upstream.Password!="existing-secret" {
  t.Fatal("redacted upstream flag cleared a stored password")
 }

 // A new client may still explicitly clear a password by omitting the
 // read-only hasPassword flag and sending a genuine empty string.
 clear:=strictConfigUpdate(t,`{"exit":{"upstream":{"password":""}}}`)
 if _,err:=b.SaveConfig(clear);err!=nil{t.Fatal(err)}
 saved,err=config.LoadAgentConfig(b.configPath)
 if err!=nil{t.Fatal(err)}
 if saved.Exit.Upstream.Password!=""{t.Fatal("explicit password clearing was ignored")}
}

func TestLegacyCustomExitPasswordFlagsAreAcceptedButNeverTrusted(t *testing.T) {
 b:=newTestBridge(t)
 item:=localExitUpdate("local:one")
 item.Username="alice"
 item.Password=ptr("saved-secret")
 initial:=ConfigUpdate{}
 initial.Proxy.CustomExits=&[]CustomExitUpdate{item}
 if _,err:=b.SaveConfig(initial);err!=nil{t.Fatal(err)}

 old:=strictConfigUpdate(t,`{"proxy":{"customExits":[{"id":"local:one","name":"Local","enabled":false,"protocol":"socks5","address":"proxy.example.test:1080","username":"alice","hasPassword":true}]}}`)
 if _,err:=b.SaveConfig(old);err!=nil{t.Fatal(err)}
 saved,err:=config.LoadAgentConfig(b.configPath)
 if err!=nil{t.Fatal(err)}
 if len(saved.Proxy.CustomExits)!=1 || saved.Proxy.CustomExits[0].Password!="saved-secret"{
  t.Fatalf("legacy save lost secret; entries=%d",len(saved.Proxy.CustomExits))
 }

 // Keep strict unknown-field rejection for unrelated, unrecognized data.
 decoder:=json.NewDecoder(strings.NewReader(`{"exit":{"upstream":{"hasPassword":true,"dangerousUnknown":true}}}`))
 decoder.DisallowUnknownFields()
 var update ConfigUpdate
 if err:=decoder.Decode(&update);err==nil{
  t.Fatal("configuration decoder accepted a genuinely unknown field")
 }
}
