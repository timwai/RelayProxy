package routing

// Action represents a routing decision
type Action string

const (
	ActionProxy  Action = "PROXY"  // Route through relay tunnel
	ActionDirect Action = "DIRECT" // Connect directly without tunnel
	ActionReject Action = "REJECT" // Block the connection
)

// Mode selects the global routing behavior
type Mode string

const (
	ModeRule        Mode = "rule"         // Match against rule list
	ModeGlobalProxy Mode = "global_proxy" // All traffic through tunnel (default)
	ModeDirect      Mode = "direct"       // All traffic direct
)

// Rule is a single routing rule. Rules are evaluated in order (first match wins).
type Rule struct {
	Name             string   `yaml:"name"    json:"name"`                        // Human-readable name
	Enabled          bool     `yaml:"enabled" json:"enabled"`                     // Whether this rule is active
	Action           Action   `yaml:"action"  json:"action"`                      // What to do on match
	ExitID           string   `yaml:"exit_id,omitempty" json:"exit_id,omitempty"` // Optional: specific exit node (PROXY only)
	DatagramRequired bool     `yaml:"datagram_required,omitempty" json:"datagram_required,omitempty"`
	Processes        []string `yaml:"processes,omitempty" json:"processes,omitempty"`
	Targets          []string `yaml:"targets,omitempty" json:"targets,omitempty"` // domain/IP glob, exact IP or CIDR
	Ports            []string `yaml:"ports,omitempty" json:"ports,omitempty"`
	Protocols        []string `yaml:"protocols,omitempty" json:"protocols,omitempty"`
}

// Flow contains only observed metadata. An unknown process or hostname stays
// empty and cannot satisfy a condition that requires it.
type Flow struct {
	Process  string
	Host     string
	IP       string
	Port     uint16
	Protocol string
}

type Decision struct {
	Action           Action
	ExitID           string
	DatagramRequired bool
	Rule             string
	Matched          bool
}

// Config holds all routing configuration.
type Config struct {
	Mode          Mode   `yaml:"mode"           json:"mode"`           // Routing mode
	DefaultAction Action `yaml:"default_action" json:"default_action"` // When no rule matches in rule mode
	Rules         []Rule `yaml:"rules"          json:"rules"`          // Ordered rule list
}

// DefaultConfig returns a sensible default routing configuration.
func DefaultConfig() Config {
	return Config{
		Mode:          ModeGlobalProxy,
		DefaultAction: ActionProxy,
		Rules: []Rule{
			{Name: "本机与局域网", Enabled: true, Targets: []string{"localhost", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "127.0.0.0/8", "::1", "fc00::/7", "fe80::/10"}, Action: ActionDirect},
		},
	}
}
