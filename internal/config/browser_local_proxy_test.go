package config

import "testing"

func TestBrowserLocalProxyNoAuthOrDomainAllowlist(t *testing.T) {
	c := &AgentConfigFile{}
	if err := validateBrowserLocalConfig(c); err != nil {
		t.Fatalf("disabled local bridge must preserve existing behavior: %v", err)
	}
	c.BrowserSync.LocalProxy.Enabled = true
	c.BrowserSync.LocalProxy.Listen = "127.0.0.1:21291"
	if err := validateBrowserLocalConfig(c); err != nil {
		t.Fatalf("local bridge must not require credentials or domain whitelist: %v", err)
	}
	for _, addr := range []string{"0.0.0.0:21291", "[::1]:21291", "localhost:21291", "127.0.0.1:80", "127.0.0.1:70000"} {
		c.BrowserSync.LocalProxy.Listen = addr
		if err := validateBrowserLocalConfig(c); err == nil {
			t.Fatalf("local bridge accepted forbidden listen address %q", addr)
		}
	}
}
