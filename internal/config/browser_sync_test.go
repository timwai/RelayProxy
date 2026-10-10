package config

import "testing"

func TestBrowserSyncDisabledByDefault(t *testing.T) {
 cfg,err:=parseServerConfig([]byte("server:\n  tls_enabled: true\n"))
 if err!=nil{t.Fatal(err)}
 if cfg.BrowserSync.Enabled{t.Fatal("browser sync must default to disabled")}
}
func TestBrowserSyncRequiresAdminTLSAndExplicitExtensionAllowlist(t *testing.T){
 const valid="abcdefghijklmnopabcdefghijklmnop"
 cfg:=&ServerConfig{}
 cfg.BrowserSync.Enabled=true
 cfg.BrowserSync.ExtensionIDs=[]string{valid}
 cfg.Server.Admin.TLSEnabled=BoolPtr(false)
 if err:=NormalizeServerConfig(cfg);err==nil{t.Fatal("plaintext admin port accepted")}
 cfg.Server.Admin.TLSEnabled=BoolPtr(true)
 cfg.BrowserSync.ExtensionIDs=[]string{"not-a-chrome-extension"}
 if err:=NormalizeServerConfig(cfg);err==nil{t.Fatal("bad Chrome extension ID accepted")}
 cfg.BrowserSync.ExtensionIDs=[]string{valid}
 if err:=NormalizeServerConfig(cfg);err!=nil{t.Fatalf("valid Browser Sync config rejected: %v",err)}
}
