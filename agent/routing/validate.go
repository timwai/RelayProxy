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
		if err := validateCompound(rule); err != nil {
			return fmt.Errorf("routing.rules[%d] (%q): %w", i, rule.Name, err)
		}
	}
	return nil
}
