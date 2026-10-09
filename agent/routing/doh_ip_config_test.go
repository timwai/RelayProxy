package routing

import "testing"

func TestExplicitDoHIPValidationAndImmutableConfig(t *testing.T) {
	for _, test := range []struct {
		value string
		ok    bool
	}{
		{"9.9.9.9", true},
		{"1.1.1.0/24", true},
		{"2606:4700:4700::/48", true},
		{"10.0.0.1", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"0.0.0.0/0", false},
		{"1.1.0.0/16", false},
		{"2606:4700::/32", false},
		{"junk", false},
		{"", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DoHBlockedIPs = []string{test.value}
			err := ValidateConfig(cfg)
			if (err == nil) != test.ok {
				t.Fatalf("DoH address %q accepted=%v want=%v: %v", test.value, err == nil, test.ok, err)
			}
		})
	}
	cfg := DefaultConfig()
	cfg.FakeIPEnabled = true
	cfg.BlockDoHEndpoints = true
	cfg.DoHBlockedIPs = []string{"9.9.9.9"}
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DoHBlockedIPs[0] = "8.8.8.8"
	got := engine.DoHBlockedIPs()
	if len(got) != 1 || got[0] != "9.9.9.9" {
		t.Fatalf("rule mutation leaked into running routing engine: %v", got)
	}
	got[0] = "1.1.1.1"
	if engine.DoHBlockedIPs()[0] != "9.9.9.9" {
		t.Fatal("caller mutated engine rule list through shared slice")
	}
	cfg.BlockDoHEndpoints = false
	if err := engine.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if len(engine.DoHBlockedIPs()) != 0 {
		t.Fatal("disabled DoH guard continued returning IP denylist")
	}
}
