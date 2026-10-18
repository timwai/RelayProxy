package systemproxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeBackend struct {
	current  string
	states   map[string]ProxyState
	pac      bool
	fail     bool
	failKind string
}

func (f *fakeBackend) CurrentService() (string, error)      { return f.current, nil }
func key(s, k string) string                                { return s + "/" + k }
func (f *fakeBackend) Read(s, k string) (ProxyState, error) { return f.states[key(s, k)], nil }
func (f *fakeBackend) Write(s, k string, x ProxyState) error {
	if f.fail || f.failKind == k {
		return errors.New("permission denied")
	}
	f.states[key(s, k)] = x
	return nil
}
func (f *fakeBackend) Validate(string) error {
	if f.pac {
		return errors.New("PAC configured")
	}
	return nil
}
func newTestManager(t *testing.T, backend *fakeBackend) *Manager {
	t.Helper()
	m := &Manager{backend: backend, path: filepath.Join(t.TempDir(), "journal.json"), stop: make(chan struct{}), stopped: make(chan struct{})}
	go m.monitor()
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func TestSnapshotRestoresPreviousProxy(t *testing.T) {
	f := &fakeBackend{current: "Wi-Fi", states: map[string]ProxyState{
		key("Wi-Fi", "web"):       {Enabled: false, Host: "old.example", Port: 3128},
		key("Wi-Fi", "secureweb"): {Enabled: true, Host: "corp.example", Port: 8443},
		key("Wi-Fi", "socks"):     {Enabled: false, Host: "", Port: 0},
	}}
	m := newTestManager(t, f)
	if err := m.Apply(Settings{Mode: "system"}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
	if m.Status().Applied != 3 {
		t.Fatalf("want 3 applied entries, got %+v", m.Status())
	}
	if err := m.Apply(Settings{}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
	web, _ := f.Read("Wi-Fi", "web")
	if web.Host != "old.example" || web.Port != 3128 || web.Enabled {
		t.Fatalf("web proxy not restored: %+v", web)
	}
	secure, _ := f.Read("Wi-Fi", "secureweb")
	if secure.Host != "corp.example" || secure.Port != 8443 || !secure.Enabled {
		t.Fatalf("secure proxy not restored: %+v", secure)
	}
	if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal should be removed after restoration: %v", err)
	}
}
func TestExternalOverrideIsNeverClobbered(t *testing.T) {
	f := &fakeBackend{current: "Wi-Fi", states: map[string]ProxyState{}}
	m := newTestManager(t, f)
	if err := m.Apply(Settings{Mode: "system"}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
	override := ProxyState{Enabled: true, Host: "other-agent", Port: 9090}
	f.states[key("Wi-Fi", "web")] = override
	if err := m.Apply(Settings{}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
	if actual, _ := f.Read("Wi-Fi", "web"); actual != override {
		t.Fatalf("clobbered user change: %+v", actual)
	}
}
func TestJournalRecoveryAfterCrash(t *testing.T) {
	f := &fakeBackend{current: "Wi-Fi", states: map[string]ProxyState{}}
	m := newTestManager(t, f)
	if err := m.Apply(Settings{Mode: "system"}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
	recovered := &Manager{backend: f, path: m.path}
	if err := recovered.readJournal(); err != nil {
		t.Fatal(err)
	}
	if err := recovered.restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Read("Wi-Fi", "secureweb"); got.Enabled {
		t.Fatalf("unexpected proxy after recovery: %+v", got)
	}
	m.mu.Lock()
	m.records = nil
	m.mu.Unlock() // simulates original crashed process
}
func TestCredentialsAndPACAreNotOverwritten(t *testing.T) {
	f := &fakeBackend{current: "Wi-Fi", states: map[string]ProxyState{key("Wi-Fi", "web"): {Authenticated: true}}}
	m := newTestManager(t, f)
	if err := m.Apply(Settings{Mode: "system"}, 8080, 1080); err == nil || !strings.Contains(err.Error(), "authenticated") {
		t.Fatalf("expected credential protection: %v", err)
	}
	f.states[key("Wi-Fi", "web")] = ProxyState{}
	f.pac = true
	if err := m.Apply(Settings{Mode: "system"}, 8080, 1080); err == nil || !strings.Contains(err.Error(), "PAC") {
		t.Fatalf("expected PAC protection: %v", err)
	}
}
func TestRestoreFailurePreservesJournal(t *testing.T) {
	f := &fakeBackend{current: "Wi-Fi", states: map[string]ProxyState{}}
	m := newTestManager(t, f)
	if err := m.Apply(Settings{Mode: "system"}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
	f.fail = true
	if err := m.Apply(Settings{}, 8080, 1080); err == nil {
		t.Fatal("expected restore failure")
	}
	if m.Status().PendingRecovery == 0 {
		t.Fatal("restore failure discarded recovery state")
	}
	f.fail = false
	if err := m.Apply(Settings{}, 8080, 1080); err != nil {
		t.Fatal(err)
	}
}

func TestPartialActivationImmediatelyRollsBack(t *testing.T) {
	f := &fakeBackend{current: "Wi-Fi", states: map[string]ProxyState{}}
	m := newTestManager(t, f)
	f.failKind = "secureweb"
	err := m.Apply(Settings{Mode: "system"}, 8080, 1080)
	if err == nil {
		t.Fatal("expected partial activation error")
	}
	if got, _ := f.Read("Wi-Fi", "web"); got.Enabled {
		t.Fatalf("partially configured HTTP proxy left active: %+v", got)
	}
	if s := m.Status(); s.Enabled || s.PendingRecovery != 0 {
		t.Fatalf("failed activation should be disarmed and recovered: %+v", s)
	}
}
