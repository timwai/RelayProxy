package routing

import "fmt"

// ValidateConfig checks disabled rules too, so enabling one cannot widen a
// malformed selector into an accidental catch-all.
func ValidateConfig(cfg Config) error {
	switch cfg.Mode {
	case "", ModeRule, ModeGlobalProxy, ModeDirect:
	default:
		return fmt.Errorf("routing.mode: invalid value %q", cfg.Mode)
	}
	validAction := func(a Action) bool { return a == ActionProxy || a == ActionDirect || a == ActionReject }
	if cfg.DefaultAction != "" && !validAction(cfg.DefaultAction) {
		return fmt.Errorf("routing.default_action: invalid value %q", cfg.DefaultAction)
	}
	for i, rule := range cfg.Rules {
		if !validAction(rule.Action) {
			return fmt.Errorf("routing.rules[%d] (%q): invalid action %q", i, rule.Name, rule.Action)
		}
		if rule.HandleDirect && rule.Action != ActionDirect {
			return fmt.Errorf("routing.rules[%d] (%q): handle_direct is only valid for DIRECT", i, rule.Name)
		}
		if rule.HandleDirect && rule.DatagramRequired {
			return fmt.Errorf("routing.rules[%d] (%q): handle_direct cannot require relay datagrams", i, rule.Name)
		}
		if err := validateCompound(rule); err != nil {
			return fmt.Errorf("routing.rules[%d] (%q): %w", i, rule.Name, err)
		}
	}
	return nil
}
