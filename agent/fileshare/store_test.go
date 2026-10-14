package fileshare

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripAndDenyByDefault(t *testing.T) {
	folder := t.TempDir()
	root := filepath.Join(folder, "shared")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(filepath.Join(folder, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled || state.Revision != 0 || len(state.Shares) != 0 {
		t.Fatalf("unsafe defaults: %#v", state)
	}
	state.Shares = []Share{{Name: "资料", RootPath: root, Enabled: true, Permissions: map[string]Permission{"devA": PermissionRead, "devB": PermissionReadWrite}}}
	state, err = store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 || len(state.Shares[0].ID) < 10 {
		t.Fatalf("bad persisted state: %#v", state)
	}
	if visible, err := store.Visible("devA"); err != nil || len(visible) != 0 {
		t.Fatalf("disabled sharing leaked: %#v %v", visible, err)
	}
	state.Enabled = true
	state, err = store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id         string
		permission Permission
	}{
		{"unknown", PermissionNone}, {"devA", PermissionRead}, {"devB", PermissionReadWrite},
	} {
		got, err := store.Visible(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if tc.permission == PermissionNone && len(got) != 0 {
			t.Fatalf("unapproved peer %q saw shares", tc.id)
		}
		if tc.permission != PermissionNone && (len(got) != 1 || got[0].Permission != tc.permission) {
			t.Fatalf("%q: %#v", tc.id, got)
		}
	}
	state.Shares[0].Permissions = map[string]Permission{}
	if _, err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Visible("devB"); len(got) != 0 {
		t.Fatalf("revoked access survived: %#v", got)
	}
	reopened, _ := New(filepath.Join(folder, "agent.yaml"))
	saved, err := reopened.Load()
	if err != nil || saved.Revision != 3 {
		t.Fatalf("persistence error: %#v %v", saved, err)
	}
	if info, err := os.Stat(store.Path); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("registry permissions: %v %v", info, err)
	}
}

func TestRevisionRejectsStaleUpdate(t *testing.T) {
	dir := t.TempDir()
	store, _ := New(filepath.Join(dir, "agent.yaml"))
	original, _ := store.Load()
	if _, err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(original); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write accepted: %v", err)
	}
}

func TestOverlappingRootsAndInvalidPermissions(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(a, "nested")
	sibling := filepath.Join(dir, "b")
	for _, name := range []string{a, b, sibling} {
		if err := os.MkdirAll(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, _ := New(filepath.Join(dir, "agent.yaml"))
	state, _ := store.Load()
	state.Shares = []Share{{Name: "a", RootPath: a, Enabled: true}, {Name: "b", RootPath: b, Enabled: true}}
	if _, err := store.Save(state); err == nil {
		t.Fatal("nested root accepted")
	}
	state.Shares[1].RootPath = sibling
	saved, err := store.Save(state)
	if err != nil {
		t.Fatalf("siblings must be valid: %v", err)
	}
	saved.Shares[0].Permissions = map[string]Permission{"dev": "admin"}
	if _, err := store.Save(saved); err == nil {
		t.Fatal("invalid permission accepted")
	}
	saved.Shares[0].Permissions = map[string]Permission{"dev": PermissionNone}
	good, err := store.Save(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Shares[0].Permissions) != 0 {
		t.Fatal("none ACL persisted")
	}
}

func TestMissingAndRelativeRoots(t *testing.T) {
	store, _ := New(filepath.Join(t.TempDir(), "agent.yaml"))
	for _, root := range []string{"./relative", "/path/that/does/not/exist/relayproxy-test"} {
		state, _ := store.Load()
		state.Shares = []Share{{Name: "x", RootPath: root, Enabled: true}}
		if _, err := store.Save(state); err == nil {
			t.Fatalf("invalid root accepted: %s", root)
		}
	}
}

func TestChangingRootRequiresRevokingAccess(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, p := range []string{a, b} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, _ := New(filepath.Join(dir, "agent.yaml"))
	state, _ := store.Load()
	state.Shares = []Share{{Name: "files", RootPath: a, Enabled: true, Permissions: map[string]Permission{"dev": PermissionRead}}}
	state, err := store.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Shares[0].RootPath = b
	if _, err := store.Save(state); err == nil {
		t.Fatal("root changed while keeping existing device ACL")
	}
	state.Shares[0].Permissions = map[string]Permission{}
	if _, err := store.Save(state); err != nil {
		t.Fatalf("root change after ACL revocation failed: %v", err)
	}
}
