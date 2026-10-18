package systemproxy

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeBackend struct {
	current  string
	states   map[string]ProxyState
	pac      bool
	fail     bool
	failKind string
	bypasses map[string][]string
	failBypass bool
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
func (f *fakeBackend) ReadBypass(service string) ([]string,error) {
	return append([]string{}, f.bypasses[service]...), nil
}
func (f *fakeBackend) WriteBypass(service string, patterns []string) error {
	if f.failBypass || f.fail { return errors.New("permission denied bypass") }
	if f.bypasses == nil { f.bypasses = make(map[string][]string) }
	f.bypasses[service] = append([]string{}, patterns...)
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

func TestIPBypassFiltering(t *testing.T) {
	cases := []struct{ entry string; remove bool }{
		{"192.168.0.0/16", true}, {"10.0.0.0/8", true}, {"172.16.0.0/12", true},
		{"100.64.0.0/10", true}, {"203.0.113.4", true}, {"[2001:db8::8]", true},
		{"192.168.*.*", true}, {"192.168.1.1", true}, {"127.0.0.0/8", false},
		{"127.*.*.*", false}, {"169.254.0.0/16", false}, {"fe80::/10", false},
		{"localhost", false}, {"*.local", false}, {"internal.example", false},
		{"<-loopback>", false}, {"*", false},
	}
	for _, tc := range cases {
		if got := ipBypassRule(tc.entry); got != tc.remove { t.Errorf("%q removed=%v, want %v", tc.entry,got,tc.remove) }
	}
}
func TestIPBypassSnapshotRestoresWithoutDestroyingHostExceptions(t *testing.T) {
	previous := []string{"localhost","*.local","192.168.0.0/16","10.0.0.0/8","example.test","127.0.0.0/8"}
	f := &fakeBackend{current:"Wi-Fi",states:map[string]ProxyState{},bypasses:map[string][]string{"Wi-Fi":append([]string{},previous...)}}
	m := newTestManager(t,f)
	if err:=m.Apply(Settings{Mode:"system"},8080,1080);err!=nil {t.Fatal(err)}
	got:=f.bypasses["Wi-Fi"]
	want:=[]string{"localhost","*.local","example.test","127.0.0.0/8"}
	if !slices.Equal(got,want) {t.Fatalf("applied bypass=%v want %v",got,want)}
	if err:=m.Apply(Settings{},8080,1080);err!=nil {t.Fatal(err)}
	if !slices.Equal(f.bypasses["Wi-Fi"],previous) {t.Fatalf("restored bypass=%v want %v",f.bypasses["Wi-Fi"],previous)}
}
func TestIPBypassExternalChangePreserved(t *testing.T) {
	f:=&fakeBackend{current:"Wi-Fi",states:map[string]ProxyState{},bypasses:map[string][]string{"Wi-Fi":{"192.168.0.0/16","*.local"}}}
	m:=newTestManager(t,f)
	if err:=m.Apply(Settings{Mode:"system"},8080,1080);err!=nil{t.Fatal(err)}
	f.bypasses["Wi-Fi"]=[]string{"other-app.local"}
	if err:=m.Apply(Settings{},8080,1080);err!=nil{t.Fatal(err)}
	if !slices.Equal(f.bypasses["Wi-Fi"],[]string{"other-app.local"}) {t.Fatal("external change overwritten")}
}
func TestBypassWriteFailureRollsBackProxy(t *testing.T) {
	f:=&fakeBackend{current:"Wi-Fi",states:map[string]ProxyState{},bypasses:map[string][]string{"Wi-Fi":{"192.168.0.0/16"}},failBypass:true}
	m:=newTestManager(t,f)
	if err:=m.Apply(Settings{Mode:"system"},8080,1080);err==nil{t.Fatal("expected bypass write failure")}
	if got:=m.Status();got.Applied!=0||got.Enabled {t.Fatalf("proxy should be disarmed: %+v",got)}
	if got,_:=f.Read("Wi-Fi","web");got.Enabled{t.Fatal("web proxy remained active")}
	if !slices.Equal(f.bypasses["Wi-Fi"],[]string{"192.168.0.0/16"}) {t.Fatal("bypass list mutated")}
}
func TestDisableIPTargetsLeavesExistingBypass(t *testing.T) {
	off:=false
	f:=&fakeBackend{current:"Wi-Fi",states:map[string]ProxyState{},bypasses:map[string][]string{"Wi-Fi":{"192.168.0.0/16"}}}
	m:=newTestManager(t,f)
	if err:=m.Apply(Settings{Mode:"system",ProxyIPLiterals:&off},8080,1080);err!=nil{t.Fatal(err)}
	if !slices.Equal(f.bypasses["Wi-Fi"],[]string{"192.168.0.0/16"}) {t.Fatal("disabled option changed bypass")}
}
