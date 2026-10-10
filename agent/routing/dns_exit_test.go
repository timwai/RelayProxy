package routing

import "testing"

func TestPinnedDNSCustomExitMustExistAndRemainEnabled(t *testing.T) {
	items := []CustomExit{{
		ID: "local:dns-secure", Name: "Encrypted DNS", Enabled: true,
		Protocol: "socks5", Address: "127.0.0.1:1080",
	}}
	cfg := DefaultConfig()
	cfg.DNSExitID = "local:dns-secure"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCustomReferences(items, "", "", cfg.Rules, cfg.DNSExitID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCustomReferences(nil, "", "", cfg.Rules, cfg.DNSExitID); err == nil {
		t.Fatal("missing DNS exit was accepted")
	}
	items[0].Enabled = false
	if err := ValidateCustomReferences(items, "", "", cfg.Rules, cfg.DNSExitID); err == nil {
		t.Fatal("disabled DNS exit was accepted")
	}
	cfg.DNSExitID = "local:dns secure"
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("malformed DNS exit reference was accepted")
	}
}
