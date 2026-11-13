package e2ee

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func pinTestKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey
}

func TestPeerKeyPinStorePinsAndPersistsExitIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "peer-key-pins.json")
	store, err := OpenPeerKeyPinStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := pinTestKey(t)
	if err := store.VerifyOrPin("server-instance-a", "exit-a", key); err != nil {
		t.Fatalf("first use should pin key: %v", err)
	}
	if err := store.VerifyOrPin("server-instance-a", "exit-a", key); err != nil {
		t.Fatalf("same key should verify: %v", err)
	}
	reloaded, err := OpenPeerKeyPinStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.VerifyOrPin("server-instance-a", "exit-a", key); err != nil {
		t.Fatalf("persisted key should verify after restart: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("pin store permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestPeerKeyPinStoreRejectsSilentKeyRotation(t *testing.T) {
	store, err := OpenPeerKeyPinStore(filepath.Join(t.TempDir(), "peer-key-pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyOrPin("server-instance-a", "exit-a", pinTestKey(t)); err != nil {
		t.Fatal(err)
	}
	err = store.VerifyOrPin("server-instance-a", "exit-a", pinTestKey(t))
	if !errors.Is(err, ErrPeerKeyChanged) {
		t.Fatalf("changed Exit key error=%v, want ErrPeerKeyChanged", err)
	}
}

func TestPeerKeyPinStoreScopesPinsByServerAndExit(t *testing.T) {
	store, err := OpenPeerKeyPinStore(filepath.Join(t.TempDir(), "peer-key-pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	keyA, keyB := pinTestKey(t), pinTestKey(t)
	if err := store.VerifyOrPin("server-instance-a", "exit-a", keyA); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyOrPin("server-instance-b", "exit-a", keyB); err != nil {
		t.Fatalf("same Exit ID on another Server must have an independent pin: %v", err)
	}
	if err := store.VerifyOrPin("server-instance-a", "exit-b", keyB); err != nil {
		t.Fatalf("another Exit ID must have an independent pin: %v", err)
	}
}

func TestPeerKeyPinStoreRejectsInvalidAndSymlinkFiles(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPeerKeyPinStore(invalid); !errors.Is(err, ErrPeerPinInvalid) {
		t.Fatalf("invalid pin file error=%v, want ErrPeerPinInvalid", err)
	}
	if os.PathSeparator == '/' {
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, []byte(`{"version":1,"pins":{}}`), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenPeerKeyPinStore(link); !errors.Is(err, ErrPeerPinInvalid) {
			t.Fatalf("symlink pin file error=%v, want ErrPeerPinInvalid", err)
		}
	}
}

func TestPeerKeyPinStoreRequiresAuthenticatedScope(t *testing.T) {
	store, err := OpenPeerKeyPinStore(filepath.Join(t.TempDir(), "peer-key-pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyOrPin("", "exit-a", pinTestKey(t)); err == nil {
		t.Fatal("empty Server instance ID was accepted")
	}
	if err := store.VerifyOrPin("server-instance-a", "", pinTestKey(t)); err == nil {
		t.Fatal("empty Exit Device ID was accepted")
	}
}
