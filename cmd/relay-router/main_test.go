package main

import "testing"

func TestParseServerEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{name: "hostname default port", input: "relay.example.com", wantHost: "relay.example.com", wantPort: 443},
		{name: "IPv4 explicit port", input: "192.168.31.14:21000", wantHost: "192.168.31.14", wantPort: 21000},
		{name: "IPv6 explicit port", input: "[2001:db8::1]:21000", wantHost: "2001:db8::1", wantPort: 21000},
		{name: "IPv6 default port", input: "2001:db8::1", wantHost: "2001:db8::1", wantPort: 443},
		{name: "invalid port", input: "relay.example.com:70000", wantErr: true},
		{name: "invalid address", input: "relay.example.com:abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := parseServerEndpoint(tt.input, 443)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseServerEndpoint() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (host != tt.wantHost || port != tt.wantPort) {
				t.Fatalf("parseServerEndpoint() = (%q, %d), want (%q, %d)", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestResolveServerName(t *testing.T) {
	if got, err := resolveServerName("relay.example.com", ""); err != nil || got != "relay.example.com" {
		t.Fatalf("DNS default server name = %q, %v", got, err)
	}
	if got, err := resolveServerName("192.168.31.14", "www.tingjusting.fun"); err != nil || got != "www.tingjusting.fun" {
		t.Fatalf("explicit SNI = %q, %v", got, err)
	}
	if _, err := resolveServerName("192.168.31.14", ""); err == nil {
		t.Fatal("IP endpoint without explicit TLS server name must fail")
	}
}
