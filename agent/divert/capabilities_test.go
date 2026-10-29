package divert

import (
	"errors"
	"testing"
)

func TestPreflightAcceptsIPv4OnlyInterception(t *testing.T) {
	caps := Capabilities{Platform: "linux", TCP: true, UDP: true, ProcessIdentity: true,
		OriginalDestination: true, ReplyInjection: true, LoopBypass: true,
		IPv6UnavailableReason: "ip6tables unavailable"}
	if err := preflightCapabilities(Config{}, caps); err != nil {
		t.Fatalf("optional IPv6 prevented IPv4 interception: %v", err)
	}
	caps.UnavailableReason = "IPv4 mangle unavailable"
	if err := preflightCapabilities(Config{}, caps); !errors.Is(err, ErrPlatformNotReady) {
		t.Fatalf("missing required IPv4 prerequisite was ignored: %v", err)
	}
	caps.UnavailableReason = ""
	caps.ReplyInjection = false
	if err := preflightCapabilities(Config{}, caps); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("missing packet reinjection was ignored: %v", err)
	}
}
