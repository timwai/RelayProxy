package divert

import (
	"errors"
	"fmt"
)

var ErrUnsupportedPlatform = errors.New("system transparent proxy is not supported by this build")
var ErrPlatformNotReady = errors.New("system transparent proxy prerequisites are not satisfied")

type Capabilities struct {
	Platform            string `json:"platform"`
	Backend             string `json:"backend,omitempty"`
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

func ValidatePlatformRules(cfg Config) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	return validatePlatformRules(cfg, PlatformCapabilities())
}
