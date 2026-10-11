package config

import "testing"

func TestBrowserLocalProxyOptInAndStrictHostPolicy(t *testing.T) {
	c := &AgentConfigFile{}
	if err := validateBrowserLocalConfig(c); err != nil {
		t.Fatalf("disabled local proxy changed legacy Agent behavior: %v", err)
	}
	c.BrowserSync.LocalProxy.Enabled = true
	c.BrowserSync.LocalProxy.Listen = "127.0.0.1:21291"
	c.BrowserSync.LocalProxy.Token = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGH"
	c.BrowserSync.LocalProxy.AllowedHosts = []string{"login.example.com"}
	if err := validateBrowserLocalConfig(c); err != nil {
		t.Fatalf("approved local proxy rejected: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "a.local", "a.internal", "example.com:443", "a..example.com", "EXAMPLE.COM"} {
		c.BrowserSync.LocalProxy.AllowedHosts = []string{host}
		if err := validateBrowserLocalConfig(c); err == nil {
			t.Fatalf("invalid browser host allowed: %q", host)
		}
	}
	c.BrowserSync.LocalProxy.AllowedHosts = []string{"login.example.com"}
	c.BrowserSync.LocalProxy.Listen = "0.0.0.0:21291"
	if err := validateBrowserLocalConfig(c); err == nil {
		t.Fatal("public local bridge listen permitted")
	}
	c.BrowserSync.LocalProxy.Listen = "127.0.0.1:21291"
	c.BrowserSync.LocalProxy.Token = "weak"
	if err := validateBrowserLocalConfig(c); err == nil {
		t.Fatal("weak Browser Sync token permitted")
	}
}
