package rdp

import (
    "path/filepath"
    "testing"
    "time"

    "relayproxy/server/repository"
)

func openSecurityTestDB(t *testing.T) *repository.DB {
    t.Helper()
    t.Setenv("RELAY_ADMIN_PASSWORD","test-password")
    db,err := repository.OpenDB("sqlite",filepath.Join(t.TempDir(),"security.db"))
    if err != nil { t.Fatal(err) }
    t.Cleanup(func(){ _ = db.Close() })
    return db
}

func TestPublicRDPSecurityManualBanAndAllowlist(t *testing.T) {
    db := openSecurityTestDB(t)
    security,err := NewSecurityManager(db)
    if err != nil { t.Fatal(err) }
    defer security.Close()
    ban,err := db.CreateRDPSecurityBan("203.0.113.0/24","","manual","test","admin",nil)
    if err != nil { t.Fatal(err) }
    if err := security.Reload(); err != nil { t.Fatal(err) }
    if ok,_ := security.Admit("rdping_1","203.0.113.8",false); ok { t.Fatal("manual ban did not reject UDP association") }
    if ok,_ := security.Admit("rdping_1","198.51.100.8",false); !ok { t.Fatal("manual ban affected unrelated IP") }
    // An allowlist entry must not override an operator's explicit ban.
    if _,err := db.CreateRDPSecurityBan("203.0.113.8","","allow","trusted","admin",nil); err != nil { t.Fatal(err) }
    if err := security.Reload(); err != nil { t.Fatal(err) }
    if ok,_ := security.Admit("rdping_1","203.0.113.8",true); ok { t.Fatal("allowlist overrode manual ban") }
    if err := db.RevokeRDPSecurityBan(ban.ID,"admin"); err != nil { t.Fatal(err) }
    if err := security.Reload(); err != nil { t.Fatal(err) }
    if ok,_ := security.Admit("rdping_1","203.0.113.8",false); !ok { t.Fatal("revoke did not unblock source") }
}

func TestPublicRDPSecurityAutoBanOnlyCountsTCP(t *testing.T) {
    db := openSecurityTestDB(t)
    if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID:"high_frequency",Enabled:true,Threshold:2,WindowSeconds:60,BanSeconds:60}); err != nil { t.Fatal(err) }
    if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID:"persistent_scan",Enabled:false,Threshold:100,WindowSeconds:300,BanSeconds:3600}); err != nil { t.Fatal(err) }
    security,err := NewSecurityManager(db)
    if err != nil { t.Fatal(err) }
    defer security.Close()
    for i:=0;i<20;i++ {
        if ok,_ := security.Admit("rdping_a","2001:db8::1",false); !ok { t.Fatal("UDP packets triggered TCP auto-ban") }
    }
    for i:=0;i<2;i++ {
        if ok,_ := security.Admit("rdping_a","2001:db8::1",true); !ok { t.Fatal("TCP connection blocked before threshold exceeded") }
    }
    if ok,reason := security.Admit("rdping_a","2001:db8::1",true); ok || reason == "" { t.Fatal("TCP burst did not trigger auto-ban") }
    if ok,_ := security.Admit("rdping_a","2001:db8::1",false); ok { t.Fatal("UDP association not blocked by existing auto-ban") }
    if ok,_ := security.Admit("rdping_b","2001:db8::1",false); !ok { t.Fatal("per-ingress auto-ban leaked to another entrance") }
    if err := security.Reload(); err != nil { t.Fatal(err) }
    if ok,_ := security.Admit("rdping_a","2001:db8::1",false); ok { t.Fatal("auto-ban not recovered from SQLite") }
    bans,err := db.ListActiveRDPSecurityBans()
    if err != nil || len(bans) != 1 || bans[0].Kind != "auto" { t.Fatalf("bad stored ban: %+v %v",bans,err) }
}

func TestPublicRDPSecurityAuditPersistence(t *testing.T) {
    db := openSecurityTestDB(t)
    security,err := NewSecurityManager(db)
    if err != nil { t.Fatal(err) }
    began := time.Now().UTC().Add(-time.Second)
    security.Record(repository.RDPSecurityLog{ID:"session-a",IngressID:"rdping_a",SourceIP:"198.51.100.9",Transport:"tcp",Result:"CONNECTING",StartedAt:began,EndedAt:began})
    security.Record(repository.RDPSecurityLog{ID:"session-a",IngressID:"rdping_a",SourceIP:"198.51.100.9",Transport:"tcp",Result:"FORWARDED",StartedAt:began,EndedAt:time.Now().UTC(),BytesUp:1024})
    security.Close()
    logs,err := db.ListRDPSecurityLogs("198.51.100.9","rdping_a",20)
    if err != nil { t.Fatal(err) }
    if len(logs) != 1 || logs[0].Result != "FORWARDED" || logs[0].BytesUp != 1024 { t.Fatalf("incorrect audited session: %+v",logs) }
}

func TestPublicRDPCIDRNormalization(t *testing.T) {
    cases := map[string]string{"198.51.100.23":"198.51.100.23/32","2001:db8::1":"2001:db8::1/128","198.51.100.29/24":"198.51.100.0/24"}
    for raw,want := range cases {
        actual,err := repository.NormalizeRDPIPCIDR(raw)
        if err != nil || actual != want { t.Fatalf("Normalize(%q)=%q,%v want %q",raw,actual,err,want) }
    }
    if _,err := repository.NormalizeRDPIPCIDR("invalid address"); err == nil { t.Fatal("invalid address accepted") }
}
