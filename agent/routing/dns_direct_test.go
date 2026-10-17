package routing

import (
	"strings"
	"testing"
)

func TestDNSDirectDomainsValidationAndPolicyCopies(t *testing.T) {
	for _, value := range []string{"corp.example", "Corp.Example.", "corp"} {
		if err := ValidateConfig(Config{DNSDirectDomains: []string{value}}); err != nil {
			t.Fatalf("valid suffix %q: %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "*", "*.example.com", ".example.com", "example..com", " example.com", "127.0.0.1", "https://example.com", "-corp.example", strings.Repeat("a", 64) + ".example"} {
		if err := ValidateConfig(Config{DNSDirectDomains: []string{value}}); err == nil {
			t.Fatalf("unsafe suffix accepted: %q", value)
		}
	}
	domains := []string{"corp.example"}
	engine, err := NewEngine(Config{DNSDirectDomains: domains})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	domains[0] = "changed.example"
	view := engine.DNSDirectDomains()
	view[0] = "changed.again"
	if got := engine.DNSDirectDomains()[0]; got != "corp.example" {
		t.Fatalf("published DNS policy was mutated: %q", got)
	}
	if err := engine.Reload(Config{}); err != nil {
		t.Fatal(err)
	}
	if len(engine.DNSDirectDomains()) != 0 {
		t.Fatal("removing DNS exceptions left the old bypass active")
	}
}
