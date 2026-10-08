package rdp

import (
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/server/repository"
)

func TestWindowsLoginFailuresAreBoundToUniquePublicRDPPort(t *testing.T) {
	db := openSecurityTestDB(t)
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{
		ID: "login_failure", Enabled: true, Threshold: 2, WindowSeconds: 300, BanSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	started := time.Now().UTC().Add(-time.Second)
	manager.TrackPublicRDPTCP("host-a", 53123, repository.RDPSecurityLog{
		ID: "connection-a", IngressID: "ingress-a", SourceIP: "198.51.100.25", StartedAt: started,
	})
	send := func(record uint64) {
		t.Helper()
		if err := manager.ReportHostAuthFailure("host-a", protocol.RDPHostAuthFailure{
			RecordID: record, SourceAddress: "127.0.0.1", SourcePort: 53123,
			ObservedAt: time.Now().UTC(), LogonType: 3, Username: "Administrator", Status: "0xc000006d",
		}); err != nil {
			t.Fatal(err)
		}
	}
	send(101)
	if ok, _ := manager.Admit("ingress-a", "198.51.100.25", false); !ok {
		t.Fatal("banned before threshold")
	}
	send(101) // duplicate event, must not increment
	if ok, _ := manager.Admit("ingress-a", "198.51.100.25", false); !ok {
		t.Fatal("duplicate triggered ban")
	}
	send(102)
	if ok, _ := manager.Admit("ingress-a", "198.51.100.25", false); ok {
		t.Fatal("login failures did not ban associated IP")
	}
	if ok, _ := manager.Admit("ingress-b", "198.51.100.25", false); !ok {
		t.Fatal("login failure affected another ingress")
	}
	logs, err := db.ListRDPAuthFailures("198.51.100.25", "ingress-a", 100)
	if err != nil || len(logs) != 2 || !logs[0].Correlated || logs[0].ConnectionID != "connection-a" {
		t.Fatalf("invalid correlated login failures: %+v err=%v", logs, err)
	}
}

func TestWindowsLoginFailureCannotGuessSourceIP(t *testing.T) {
	db := openSecurityTestDB(t)
	manager, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	now := time.Now().UTC()
	manager.TrackPublicRDPTCP("host-a", 51000, repository.RDPSecurityLog{
		ID: "connection-a", IngressID: "ingress-a", SourceIP: "203.0.113.12", StartedAt: now.Add(-time.Second),
	})
	event := protocol.RDPHostAuthFailure{RecordID: 201, SourceAddress: "127.0.0.1", SourcePort: 51000,
		ObservedAt: now, LogonType: 3}
	if err := manager.ReportHostAuthFailure("host-b", event); err != nil {
		t.Fatal(err)
	}
	event.RecordID = 202
	event.SourcePort = 0 // RDP Windows event often records zero; must be audit-only
	if err := manager.ReportHostAuthFailure("host-a", event); err != nil {
		t.Fatal(err)
	}
	event.RecordID = 203
	event.SourcePort = 51000
	event.ObservedAt = now.Add(-20 * time.Second)
	if err := manager.ReportHostAuthFailure("host-a", event); err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRDPAuthFailures("", "", 100)
	if err != nil || len(logs) != 3 {
		t.Fatalf("missing unmatched events: %+v %v", logs, err)
	}
	for _, row := range logs {
		if row.Correlated || row.SourceIP != "" {
			t.Fatalf("unmatched event gained public IP: %+v", row)
		}
	}
	event.RecordID = 204
	event.ObservedAt = now
	event.SourceAddress = "198.51.100.11"
	if err := manager.ReportHostAuthFailure("host-a", event); err == nil {
		t.Fatal("non-loopback event accepted")
	}
}

func TestWindowsLoginFailureAllowlistAndAmbiguousPort(t *testing.T) {
	db := openSecurityTestDB(t)
	if err := db.UpdateRDPSecurityRule(repository.RDPSecurityRule{
		ID: "login_failure", Enabled: true, Threshold: 1, WindowSeconds: 300, BanSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreateRDPSecurityBan("203.0.113.0/24", "", "allow", "trusted", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	now := time.Now().UTC()
	manager.TrackPublicRDPTCP("host-c", 53000, repository.RDPSecurityLog{ID: "a", IngressID: "ingress-a", SourceIP: "203.0.113.1", StartedAt: now.Add(-time.Second)})
	report := func(record uint64) {
		t.Helper()
		if err := manager.ReportHostAuthFailure("host-c", protocol.RDPHostAuthFailure{
			RecordID: record, SourceAddress: "127.0.0.1", SourcePort: 53000, LogonType: 10, ObservedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	report(301)
	if ok, _ := manager.Admit("ingress-a", "203.0.113.1", false); !ok {
		t.Fatal("allowlist not honored")
	}
	// Reused source port with two matching windows is ambiguous: never guess.
	manager.TrackPublicRDPTCP("host-c", 53000, repository.RDPSecurityLog{ID: "b", IngressID: "ingress-b", SourceIP: "198.51.100.1", StartedAt: now.Add(-time.Second)})
	report(302)
	logs, err := db.ListRDPAuthFailures("", "", 20)
	if err != nil || len(logs) != 2 || logs[0].Correlated {
		t.Fatalf("ambiguous connection matched: %+v %v", logs, err)
	}
}
