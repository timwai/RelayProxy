package routing

import "testing"

func TestInvalidReloadPreservesPolicy(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []Rule{
		{Name: "bad cidr", Targets: []string{"192.0.2.0/999"}, Action: ActionDirect},
		{Name: "bad port", Ports: []string{"65536"}, Action: ActionDirect},
		{Name: "reverse range", Ports: []string{"443-80"}, Action: ActionDirect},
		{Name: "bad match", Protocols: []string{"unknown"}, Action: ActionDirect},
		{Name: "bad action", Action: "ALLOW"},
	} {
		t.Run(rule.Name, func(t *testing.T) {
			if err := engine.Reload(Config{Mode: ModeRule, DefaultAction: ActionDirect, Rules: []Rule{rule}}); err == nil {
				t.Fatal("expected validation error, including for disabled rules")
			}
			if action, _ := engine.Match("203.0.113.1", 443); action != ActionReject {
				t.Fatalf("failed reload replaced live policy: %s", action)
			}
		})
	}
}

func TestEngineOwnsRuleSnapshot(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{Enabled: true, Action: ActionReject}}}
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rules[0].Action = ActionDirect
	returned := engine.Config()
	returned.Rules[0].Action = ActionProxy
	if action, _ := engine.Match("example.com", 443); action != ActionReject {
		t.Fatalf("caller mutated running policy: %s", action)
	}
}
