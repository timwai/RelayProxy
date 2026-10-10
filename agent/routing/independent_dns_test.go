package routing

import "testing"

func TestIndependentDNSModesAndLegacyAssociationDefault(t *testing.T) {
	for _, cfg := range []Config{
		{DNSMode: DNSModeProxy, ProxyDNSEnabled: true},
		{DNSMode: DNSModeLocal, ProxyDNSEnabled: true},
		{DNSMode: DNSModeProxy, FakeIPEnabled: true},
		{DNSMode: DNSModeProxy, DNSAssociationEnabled: boolPtr(false)},
	} {
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("valid independent DNS configuration rejected: %+v, %v", cfg, err)
		}
	}
	if err := ValidateConfig(Config{DNSMode: DNSModeProxy, ProxyDNSEnabled: true, FakeIPEnabled: true}); err == nil {
		t.Fatal("conflicting synthetic and real DNS/53 capture was accepted")
	}
	engine, err := NewEngine(Config{DNSMode: DNSModeProxy, ProxyDNSEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !engine.DNSAssociationEnabled() || !engine.ProxyDNSEnabled() || engine.FakeIPEnabled() {
		t.Fatal("legacy DNS association default or independent real-IP mode lost")
	}
	if err := engine.Reload(Config{DNSMode: DNSModeProxy, FakeIPEnabled: true, DNSAssociationEnabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if engine.DNSAssociationEnabled() || engine.ProxyDNSEnabled() || !engine.FakeIPEnabled() {
		t.Fatal("hot switch from proxy real DNS to FakeIP/disabled association failed")
	}
}

func TestDNSAssociationPointerIsCloned(t *testing.T) {
	enabled := false
	original := Config{DNSAssociationEnabled: &enabled}
	cloned := CloneConfig(original)
	*cloned.DNSAssociationEnabled = true
	if *original.DNSAssociationEnabled {
		t.Fatal("cloned DNS policy mutated original config")
	}
}

func boolPtr(v bool) *bool { return &v }
