package repository

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRDPSourceGroupsDeviceNamesPagingAndClear(t *testing.T) {
	t.Setenv("RELAY_ADMIN_PASSWORD", "test-password")
	db, err := OpenDB("sqlite",filepath.Join(t.TempDir(),"rdp-audit.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	now:=time.Now().UTC().Truncate(time.Second)
	if _,err:=db.Exec(`INSERT INTO devices
		(id,name,public_key_fingerprint,installation_id,approval_state,
		requested_capabilities,approved_capabilities,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,"target-one","工作机,一号","rdp-test-fingerprint","rdp-test-install","approved",
		"[]","[]",now,now);err!=nil { t.Fatal(err) }
	samples:=[]RDPSecurityLog{
		{ID:"1",IngressID:"ingress-one",TargetDeviceID:"target-one",SourceIP:"203.0.113.1",Transport:"tcp",Result:"FORWARDED",BytesUp:1024,StartedAt:now.Add(-time.Minute),EndedAt:now.Add(-time.Minute)},
		{ID:"2",IngressID:"ingress-one",TargetDeviceID:"target-one",SourceIP:"203.0.113.1",Transport:"udp",Result:"REJECTED",Reason:"RATE_LIMIT",StartedAt:now.Add(-30*time.Second),EndedAt:now.Add(-30*time.Second)},
		{ID:"3",IngressID:"ingress-other",TargetDeviceID:"missing-device",SourceIP:"2001:db8::2",Transport:"tcp",Result:"FORWARDED",StartedAt:now.Add(-20*time.Second),EndedAt:now.Add(-20*time.Second)},
	}
	for _,item:=range samples { if err:=db.InsertRDPSecurityLog(item);err!=nil { t.Fatal(err) } }
	groups,err:=db.ListRDPSecuritySourceGroups("",1,1)
	if err!=nil {t.Fatal(err)}
	if groups.Total!=2 || len(groups.Items)!=1 || groups.Items[0].SourceIP!="2001:db8::2" {
		t.Fatalf("bad first page: %+v",groups)
	}
	second,err:=db.ListRDPSecuritySourceGroups("",2,1)
	if err!=nil {t.Fatal(err)}
	if second.Total!=2 || len(second.Items)!=1 {t.Fatalf("bad second page: %+v",second)}
	g:=second.Items[0]
	if g.ConnectionCount!=2 || g.ForwardedCount!=1 || g.RejectedCount!=1 ||
		g.BytesUp!=1024 || len(g.Targets)!=1 || g.Targets[0]!="工作机,一号" {
		t.Fatalf("bad grouped data: %+v",g)
	}
	filtered,err:=db.ListRDPSecuritySourceGroups("203.0.113",1,20)
	if err!=nil || filtered.Total!=1 {t.Fatalf("search failed: %+v %v",filtered,err)}
	details,err:=db.ListRDPSecurityLogPage("203.0.113.1",1,1)
	if err!=nil || details.Total!=2 || len(details.Items)!=1 ||
		details.Items[0].TargetName!="工作机,一号" || details.Items[0].Result!="REJECTED" {
		t.Fatalf("bad source details: %+v %v",details,err)
	}
	fallback,err:=db.ListRDPSecurityLogPage("2001:db8::2",1,10)
	if err!=nil || len(fallback.Items)!=1 || fallback.Items[0].TargetName!="missing-device" {
		t.Fatalf("deleted device fallback missing: %+v %v",fallback,err)
	}
	if _,err:=db.ListRDPSecuritySourceGroups("",0,20);err==nil {t.Fatal("invalid group page accepted")}
	if _,err:=db.ListRDPSecurityLogPage("",1,101);err==nil {t.Fatal("invalid detail page size accepted")}
	if _,err:=db.CreateRDPSecurityBan("192.0.2.11","","manual","test","admin",nil);err!=nil {t.Fatal(err)}
	deleted,err:=db.DeleteRDPSecurityLogs("203.0.113.1")
	if err!=nil || deleted!=2 {t.Fatalf("IP clear: %d %v",deleted,err)}
	groups,err=db.ListRDPSecuritySourceGroups("",1,20)
	if err!=nil || groups.Total!=1 {t.Fatalf("group survived clear: %+v %v",groups,err)}
	deleted,err=db.DeleteRDPSecurityLogs("")
	if err!=nil || deleted!=1 {t.Fatalf("global clear: %d %v",deleted,err)}
	bans,err:=db.ListActiveRDPSecurityBans()
	if err!=nil || len(bans)!=1 {t.Fatalf("audit clear deleted IP security policy: %+v %v",bans,err)}
}

// A Server may have run the old development branch with Windows logon audit.
// Opening the new build must remove those abandoned sensitive event records.
func TestRDPSecurityRetiredWindowsAuditSchemaCleaned(t *testing.T) {
	t.Setenv("RELAY_ADMIN_PASSWORD","test-password")
	dsn:=filepath.Join(t.TempDir(),"cleanup.db")
	db,err:=OpenDB("sqlite",dsn)
	if err!=nil {t.Fatal(err)}
	if _,err:=db.Exec("CREATE TABLE rdp_auth_failures (id TEXT PRIMARY KEY)");err!=nil {t.Fatal(err)}
	if _,err:=db.Exec("INSERT INTO rdp_security_rules (id,name,enabled,threshold,window_seconds,ban_seconds) VALUES ('login_failure','old',1,5,300,1800)");err!=nil {t.Fatal(err)}
	if err:=db.Close();err!=nil {t.Fatal(err)}
	db,err=OpenDB("sqlite",dsn)
	if err!=nil {t.Fatal(err)}
	defer db.Close()
	var tables int
	if err:=db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='rdp_auth_failures' AND type='table'").Scan(&tables);err!=nil {t.Fatal(err)}
	if tables!=0 {t.Fatal("retired Windows audit table still exists")}
	var rules int
	if err:=db.QueryRow("SELECT COUNT(*) FROM rdp_security_rules WHERE id='login_failure'").Scan(&rules);err!=nil {t.Fatal(err)}
	if rules!=0 {t.Fatal("retired Windows audit rule still exists")}
}
