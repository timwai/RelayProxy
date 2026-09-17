package divert

import "testing"

func TestLoopGuard(t *testing.T) {
	g := LoopGuard{
		SelfNames:  []string{"relayproxy.exe"},
		RelayHost:  "relay.example.com",
		RelayPorts: []int{443},
		LocalProxy: []string{"127.0.0.1:1080"},
	}
	if !g.MustDirect("C:\\x\\relayproxy.exe", "1.1.1.1", 80) {
		t.Fatal("self process should be direct")
	}
	if !g.MustDirect("chrome.exe", "relay.example.com", 443) {
		t.Fatal("relay addr should be direct")
	}
	if !g.MustDirect("chrome.exe", "127.0.0.1", 1080) {
		t.Fatal("local socks should be direct")
	}
	if g.MustDirect("chrome.exe", "1.1.1.1", 443) {
		t.Fatal("normal traffic should not be forced direct")
	}
}
