package repository

import (
	"slices"
	"testing"
	"time"
)

func TestDesktopAuthorizationDoesNotRequireRDPService(t *testing.T) {
	db, err := OpenDB("sqlite", "file:desktop-auth-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO users
		(id, username, password_hash, display_name, role, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"user-1", "desktop-owner", "hash", "Desktop Owner", "admin", "active", now, now); err != nil {
		t.Fatal(err)
	}
	insertDevice := func(id, name, fingerprint, installation, caps string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO devices
			(id, owner_user_id, name, public_key_fingerprint, installation_id, platform, arch, client_version,
			 approval_state, requested_capabilities, approved_capabilities, last_seen_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "user-1", name, fingerprint, installation, "windows", "amd64", "test",
			EnrollmentApproved, caps, caps, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	insertDevice("controller", "Controller", "fp-controller", "install-controller", `["desktop.controller"]`)
	insertDevice("home-target", "Windows Home", "fp-home", "install-home", `["desktop.host"]`)
	if _, err := db.Exec(`INSERT INTO rdp_access_grants
		(controller_device_id, target_device_id, granted_by, created_at)
		VALUES (?, ?, ?, ?)`, "controller", "home-target", "user-1", now); err != nil {
		t.Fatal(err)
	}
	allowed, err := db.AuthorizeDesktop("controller", "home-target")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("desktop authorization was denied without an RDP service")
	}
	rdpAllowed, err := db.AuthorizeRDP("controller", "home-target")
	if err != nil {
		t.Fatal(err)
	}
	if rdpAllowed {
		t.Fatal("native RDP authorization unexpectedly succeeded")
	}
	targets, err := db.ListRemoteDesktopTargetsForController("controller")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets=%d want 1", len(targets))
	}
	target := targets[0]
	if target.DeviceID != "home-target" || !target.RelayDesktop || target.NativeRDP {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestDesktopCapabilitiesCanBeApproved(t *testing.T) {
	requested := []string{"proxy.client", "desktop.controller", "desktop.host"}
	got := filterApprovedCapabilities([]string{"desktop.controller", "desktop.host"}, requested)
	if !slices.Equal(got, []string{"desktop.controller", "desktop.host"}) {
		t.Fatalf("approved capabilities = %v", got)
	}
}
