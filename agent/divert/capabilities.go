package divert

import (
	"errors"
	"fmt"
)

var ErrUnsupportedPlatform = errors.New("system transparent proxy is not supported by this build")
var ErrPlatformNotReady = errors.New("system transparent proxy prerequisites are not satisfied")

// Capabilities describes verified OS interception, not just a compiled rule
// engine or an installed driver. False capabilities must never be advertised
// as working through best-effort packet rewriting.
type Capabilities struct {
	Platform            string `json:"platform"`
	TCP                 bool   `json:"tcp"`
	UDP                 bool   `json:"udp"`
	IPv6                bool   `json:"ipv6"`
	ProcessIdentity     bool   `json:"process_identity"`
	OriginalDestination bool   `json:"original_destination"`
	ReplyInjection      bool   `json:"reply_injection"`
	LoopBypass          bool   `json:"loop_bypass"`
	Hostnames           bool   `json:"hostnames"`
	HostnameSource      string `json:"hostname_source,omitempty"`
	UnavailableReason   string `json:"unavailable_reason,omitempty"`
}

func PlatformCapabilities() Capabilities { return platformCapabilities() }

// Preflight is side-effect free: it loads no driver, opens no listener and
// changes no firewall rule. It checks starting interception, regardless of the
// stored mode, so callers that keep interception disabled need not call it.
func Preflight(cfg Config) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	caps := PlatformCapabilities()
	if !caps.TCP || !caps.UDP || !caps.IPv6 || !caps.ProcessIdentity ||
		!caps.OriginalDestination || !caps.ReplyInjection || !caps.LoopBypass {
		return fmt.Errorf("%w (%s): %s; use SOCKS5/HTTP with network.mode empty", ErrUnsupportedPlatform, caps.Platform, caps.UnavailableReason)
	}
	if caps.UnavailableReason != "" {
		return fmt.Errorf("%w: %s", ErrPlatformNotReady, caps.UnavailableReason)
	}
	return validatePlatformRules(cfg, caps)
}

func validatePlatformRules(cfg Config, caps Capabilities) error {
	if !caps.Hostnames {
		for _, rule := range cfg.Rules {
			if rule.Enabled && len(rule.Hosts) > 0 {
				return fmt.Errorf("%w (%s): hostname metadata is unavailable for rule %q", ErrUnsupportedPlatform, caps.Platform, rule.Name)
			}
		}
	}
	return nil
}

// ValidatePlatformRules checks rules applied to an active adapter, including
// when a saved mode change will only take effect after restart. It performs no
// environment/driver preflight and is safe before committing a configuration.
func ValidatePlatformRules(cfg Config) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	return validatePlatformRules(cfg, PlatformCapabilities())
}
