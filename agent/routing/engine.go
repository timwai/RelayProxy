package routing

import (
	"strconv"
	"strings"
	"sync"
)

// Engine evaluates the same ordered compound rules for every client entry.
type Engine struct {
	mu       sync.RWMutex
	config   Config
	compound []compoundRule
}
type portRange struct{ start, end uint16 }

func NewEngine(cfg Config) (*Engine, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeGlobalProxy
	}
	if cfg.DefaultAction == "" {
		cfg.DefaultAction = ActionProxy
	}
	cfg = CloneConfig(cfg)
	e := &Engine{config: cfg, compound: make([]compoundRule, len(cfg.Rules))}
	for i, rule := range cfg.Rules {
		e.compound[i] = compileCompound(rule)
	}
	return e, nil
}

func (e *Engine) Reload(cfg Config) error {
	compiled, err := NewEngine(cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.config, e.compound = compiled.config, compiled.compound
	e.mu.Unlock()
	return nil
}

func (e *Engine) Config() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return CloneConfig(e.config)
}

func (e *Engine) Match(host string, port uint16) (Action, string) {
	d := e.Decide(host, port)
	return d.Action, d.ExitID
}
func (e *Engine) Decide(host string, port uint16) Decision {
	return e.DecideFlow(Flow{Host: host, Port: port, Protocol: "tcp"})
}
func (e *Engine) DecideFlow(flow Flow) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	switch e.config.Mode {
	case ModeGlobalProxy:
		return Decision{Action: ActionProxy, Rule: "global_proxy"}
	case ModeDirect:
		return Decision{Action: ActionDirect, Rule: "direct"}
	}
	for i, rule := range e.config.Rules {
		if rule.Enabled && e.compound[i].matches(rule, flow) {
			return Decision{Action: rule.Action, ExitID: rule.ExitID, DatagramRequired: rule.DatagramRequired, Rule: rule.Name, Matched: true}
		}
	}
	return Decision{Action: e.config.DefaultAction, Rule: "default"}
}

func matchDomainSuffix(host, suffix string) bool {
	host, suffix = strings.ToLower(host), strings.ToLower(suffix)
	suffix = strings.TrimPrefix(suffix, ".")
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

func parsePortRange(s string) portRange {
	if parts := strings.SplitN(s, "-", 2); len(parts) == 2 {
		start, _ := strconv.ParseUint(parts[0], 10, 16)
		end, _ := strconv.ParseUint(parts[1], 10, 16)
		return portRange{uint16(start), uint16(end)}
	}
	p, _ := strconv.ParseUint(s, 10, 16)
	return portRange{start: uint16(p)}
}
