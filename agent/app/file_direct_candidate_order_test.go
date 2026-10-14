package app

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestPrioritizeFileDirectEndpointsSameSubnetBeforePublicUPnP(t *testing.T) {
	input := []string{
		"8.8.8.8:42000",
		"10.5.7.8:42001",
		"192.168.10.4:42002",
		"192.168.10.9:42003",
		"[2001:4860:4860::8888]:42004",
	}
	want := []string{
		"192.168.10.4:42002",
		"192.168.10.9:42003",
		"8.8.8.8:42000",
		"10.5.7.8:42001",
		"[2001:4860:4860::8888]:42004",
	}
	got := prioritizeFileDirectEndpoints(input, []netip.Prefix{
		netip.MustParsePrefix("192.168.10.0/24"),
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong same-LAN preference: got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(input, []string{
		"8.8.8.8:42000", "10.5.7.8:42001", "192.168.10.4:42002",
		"192.168.10.9:42003", "[2001:4860:4860::8888]:42004",
	}) {
		t.Fatal("input endpoints must not be mutated")
	}
}

func TestPrioritizeFileDirectEndpointsKeepsUnknownPathsAsFallback(t *testing.T) {
	input := []string{"8.8.4.4:21000", "10.0.0.2:21000", "[2001:db8::2]:21000"}
	got := prioritizeFileDirectEndpoints(input, []netip.Prefix{
		netip.MustParsePrefix("192.168.0.0/24"),
	})
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("unmatched candidates must retain fallback order: %v", got)
	}
}
