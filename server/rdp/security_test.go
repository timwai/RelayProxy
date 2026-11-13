package rdp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"relayproxy/server/repository"
)

func openSecurityTestDB(t *testing.T) *repository.DB {
	t.Helper()
	t.Setenv("RELAY_ADMIN_PASSWORD", "test-password")
	db, err := repository.OpenDB("sqlite", filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPublicRDPSecurityManualBanAndAllowlist(t *testing.T) {
	db := openSecurityTestDB(t)
	security, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer security.Close()
	ban, err := db.CreateRDPSecurityBan("203.0.113.0/24", "", "manual", "test", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := security.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := security.Admit("rdping_1", "203.0.113.8", false); ok {
		t.Fatal("manual ban did not reject UDP association")
	}
	if ok, _ := security.Admit("rdping_1", "198.51.100.8", false); !ok {
		t.Fatal("manual ban affected unrelated IP")
	}
	// An allowlist entry must not override an operator's explicit ban.
	if _, err := db.CreateRDPSecurityBan("203.0.113.8", "", "allow", "trusted", "admin", nil); err != nil {
		t.Fatal(err)
	}
	if err := security.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := security.Admit("rdping_1", "203.0.113.8", true); ok {
		t.Fatal("allowlist overrode manual ban")
	}
	if err := db.RevokeRDPSecurityBan(ban.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := security.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := security.Admit("rdping_1", "203.0.113.8", false); !ok {
		t.Fatal("revoke did not unblock source")
	}
}

func TestPublicRDPSecurityAutoBanOnlyCountsTCP(t *testing.T) {
	db := openSecurityTestDB(t)
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "high_frequency", Enabled: true, Threshold: 2, WindowSeconds: 60, BanSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "persistent_scan", Enabled: false, Threshold: 100, WindowSeconds: 300, BanSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer security.Close()
	for i := 0; i < 20; i++ {
		if ok, _ := security.Admit("rdping_a", "2001:db8::1", false); !ok {
			t.Fatal("UDP packets triggered TCP auto-ban")
		}
	}
	for i := 0; i < 2; i++ {
		if ok, _ := security.Admit("rdping_a", "2001:db8::1", true); !ok {
			t.Fatal("TCP connection blocked before threshold exceeded")
		}
	}
	if ok, reason := security.Admit("rdping_a", "2001:db8::1", true); ok || reason == "" {
		t.Fatal("TCP burst did not trigger auto-ban")
	}
	if ok, _ := security.Admit("rdping_a", "2001:db8::1", false); ok {
		t.Fatal("UDP association not blocked by existing auto-ban")
	}
	if ok, _ := security.Admit("rdping_b", "2001:db8::1", false); !ok {
		t.Fatal("per-ingress auto-ban leaked to another entrance")
	}
	if err := security.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := security.Admit("rdping_a", "2001:db8::1", false); ok {
		t.Fatal("auto-ban not recovered from SQLite")
	}
	bans, err := db.ListActiveRDPSecurityBans()
	if err != nil || len(bans) != 1 || bans[0].Kind != "auto" {
		t.Fatalf("bad stored ban: %+v %v", bans, err)
	}
}

func TestPublicRDPSecurityAuditPersistence(t *testing.T) {
	db := openSecurityTestDB(t)
	security, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now().UTC().Add(-time.Second)
	security.Record(repository.RDPSecurityLog{ID: "session-a", IngressID: "rdping_a", SourceIP: "198.51.100.9", Transport: "tcp", Result: "CONNECTING", StartedAt: began, EndedAt: began})
	security.Record(repository.RDPSecurityLog{ID: "session-a", IngressID: "rdping_a", SourceIP: "198.51.100.9", Transport: "tcp", Result: "FORWARDED", StartedAt: began, EndedAt: time.Now().UTC(), BytesUp: 1024})
	security.Close()
	logs, err := db.ListRDPSecurityLogs("198.51.100.9", "rdping_a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Result != "FORWARDED" || logs[0].BytesUp != 1024 {
		t.Fatalf("incorrect audited session: %+v", logs)
	}
}

func TestPublicRDPCIDRNormalization(t *testing.T) {
	cases := map[string]string{"198.51.100.23": "198.51.100.23/32", "2001:db8::1": "2001:db8::1/128", "198.51.100.29/24": "198.51.100.0/24"}
	for raw, want := range cases {
		actual, err := repository.NormalizeRDPIPCIDR(raw)
		if err != nil || actual != want {
			t.Fatalf("Normalize(%q)=%q,%v want %q", raw, actual, err, want)
		}
	}
	if _, err := repository.NormalizeRDPIPCIDR("invalid address"); err == nil {
		t.Fatal("invalid address accepted")
	}
}

func TestRDPSecurityClearLogsCannotResurrectOldSessions(t *testing.T) {
	db := openSecurityTestDB(t)
	security, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Minute)
	first := repository.RDPSecurityLog{
		ID: "old-session", IngressID: "ingress-a", SourceIP: "203.0.113.10",
		Transport: "tcp", Result: "CONNECTING", StartedAt: old, EndedAt: old,
	}
	security.Record(first)
	deleted, err := security.ClearLogs(context.Background(), "203.0.113.10")
	if err != nil || deleted != 1 {
		t.Fatalf("clear failed: %d %v", deleted, err)
	}
	first.Result = "FORWARDED"
	first.EndedAt = time.Now().UTC()
	security.Record(first) // Completion from the pre-clear connection.
	security.Record(repository.RDPSecurityLog{
		ID: "other-source", IngressID: "ingress-a", SourceIP: "203.0.113.11", Transport: "tcp",
		Result: "FORWARDED", StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	})
	security.Close()
	firstPage, err := db.ListRDPSecurityLogPage("203.0.113.10", 1, 10)
	if err != nil || firstPage.Total != 0 {
		t.Fatalf("old session resurrected: %+v %v", firstPage, err)
	}
	otherPage, err := db.ListRDPSecurityLogPage("203.0.113.11", 1, 10)
	if err != nil || otherPage.Total != 1 {
		t.Fatalf("other IP unexpectedly cleared: %+v %v", otherPage, err)
	}
}

func TestRDPSecurityLowFrequencyGlobalAndRestartRecovery(t *testing.T) {
	db := openSecurityTestDB(t)
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "high_frequency", Enabled: false, Threshold: 30, WindowSeconds: 60, BanSeconds: 900}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "persistent_scan", Enabled: false, Threshold: 100, WindowSeconds: 300, BanSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{
		ID: "low_frequency_scan", Enabled: true, Threshold: 3, WindowSeconds: 86400, BanSeconds: 0,
		Scope: repository.RDPRuleScopeGlobal, ClearLogs: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, ing := range []string{"rdping_a", "rdping_b", "rdping_c"} {
		if err := db.InsertRDPSecurityLog(repository.RDPSecurityLog{
			ID: "slow-" + ing, IngressID: ing, SourceIP: "198.51.100.42", Transport: "tcp",
			Result: "FORWARDED", StartedAt: now.Add(-time.Duration(15-i*3) * time.Minute),
			EndedAt: now.Add(-time.Duration(15-i*3) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a server restart: NewSecurityManager seeds the 24h counter from
	// retained audit rows across all entrances.
	security, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	// The 4th attempt (on any entrance) exceeds threshold=3, triggers a
	// permanent global ban and clears the historical audit rows.
	if ok, reason := security.Admit("rdping_d", "198.51.100.42", true); ok || reason != "AUTO_BAN_low_frequency_scan" {
		t.Fatalf("expected low_frequency_scan permanent ban, got ok=%v reason=%q", ok, reason)
	}
	// Subsequent attempts on any entrance are rejected, and suppressed audit
	// records must not repopulate the table.
	if ok, _ := security.Admit("rdping_a", "198.51.100.42", true); ok {
		t.Fatal("global permanent ban did not block other entrance")
	}
	security.Record(repository.RDPSecurityLog{
		ID: "suppressed-after-ban", IngressID: "rdping_a", SourceIP: "198.51.100.42",
		Transport: "tcp", Result: "REJECTED", Reason: "AUTO_IP_BAN", StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	})
	security.Close()

	bans, err := db.ListActiveRDPSecurityBans()
	if err != nil || len(bans) != 1 || bans[0].ExpiresAt != nil || bans[0].IngressID != "" || !bans[0].ClearLogs {
		t.Fatalf("expected 1 global permanent ban with clearLogs, got %+v (%v)", bans, err)
	}
	page, err := db.ListRDPSecurityLogPage("198.51.100.42", 1, 20)
	if err != nil || page.Total != 0 {
		t.Fatalf("expected logs to be auto-cleared and suppressed, got %+v (%v)", page, err)
	}
}

func TestRDPSecuritySubnetUDPAndRepeatOffenderEscalation(t *testing.T) {
	db := openSecurityTestDB(t)
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "high_frequency", Enabled: true, Threshold: 1, WindowSeconds: 60, BanSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "persistent_scan", Enabled: false, Threshold: 100, WindowSeconds: 300, BanSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{
		ID: "subnet_scan", Enabled: true, Threshold: 2, WindowSeconds: 3600, BanSeconds: 3600,
		Scope: repository.RDPRuleScopeGlobal, PrefixV4: 24, PrefixV6: 64, CountUDP: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{
		ID: "repeat_offender", Enabled: true, Threshold: 1, WindowSeconds: 7 * 86400, BanSeconds: 0,
		Scope: repository.RDPRuleScopeGlobal, Metric: repository.RDPRuleMetricBans,
	}); err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer security.Close()

	// Rotate through 3 distinct IPs in 198.51.100.0/24 using UDP associations:
	// subnet_scan (CountUDP=true, PrefixV4=24, Threshold=2) bans the whole /24.
	if ok, _ := security.Admit("rdping_a", "198.51.100.1", false); !ok {
		t.Fatal("1st subnet UDP attempt rejected early")
	}
	if ok, _ := security.Admit("rdping_b", "198.51.100.2", false); !ok {
		t.Fatal("2nd subnet UDP attempt rejected early")
	}
	if ok, reason := security.Admit("rdping_c", "198.51.100.3", false); ok || reason != "AUTO_BAN_subnet_scan" {
		t.Fatalf("3rd subnet UDP attempt did not trigger subnet ban: ok=%v reason=%q", ok, reason)
	}
	// A 4th unused IP in the same /24 is immediately blocked by the /24 ban.
	if ok, _ := security.Admit("rdping_a", "198.51.100.250", true); ok {
		t.Fatal("/24 subnet auto-ban did not block sibling address")
	}

	// Now disable subnet_scan and trigger high_frequency twice on different
	// entrances for 203.0.113.7: the 2nd auto-ban exceeds repeat_offender
	// (Threshold=1) and escalates to a permanent global ban.
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{ID: "subnet_scan", Enabled: false, Threshold: 2, WindowSeconds: 3600, BanSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := security.Reload(); err != nil {
		t.Fatal(err)
	}
	_, _ = security.Admit("rdping_a", "203.0.113.7", true)
	if ok, reason := security.Admit("rdping_a", "203.0.113.7", true); ok || reason != "AUTO_BAN_high_frequency" {
		t.Fatalf("expected 1st high_frequency ban, got ok=%v reason=%q", ok, reason)
	}
	_, _ = security.Admit("rdping_b", "203.0.113.7", true)
	if ok, reason := security.Admit("rdping_b", "203.0.113.7", true); ok || reason != "AUTO_BAN_repeat_offender" {
		t.Fatalf("expected repeat_offender escalation to permanent ban, got ok=%v reason=%q", ok, reason)
	}
	// Because repeat_offender is global and permanent, a 3rd entrance is blocked too.
	if ok, _ := security.Admit("rdping_c", "203.0.113.7", true); ok {
		t.Fatal("escalated global permanent ban did not block 3rd entrance")
	}
}
