package divert

import (
	"path"
	"strings"
)

// Action is the divert routing decision.
type Action string

const (
	ActionProxy  Action = "PROXY"
	ActionDirect Action = "DIRECT"
	ActionReject Action = "REJECT"
)

// Protocol is tcp or udp.
type Protocol string

const (
	ProtoTCP Protocol = "tcp"
	ProtoUDP Protocol = "udp"
)

// Rule is one process-first divert rule (first match wins).
type Rule struct {
	Name      string   `yaml:"name" json:"name"`
	Enabled   bool     `yaml:"enabled" json:"enabled"`
	Process   string   `yaml:"process" json:"process"` // glob against basename or full path
	Hosts     []string `yaml:"hosts" json:"hosts"`
	CIDRs     []string `yaml:"cidrs" json:"cidrs"`
	Ports     []string `yaml:"ports" json:"ports"`         // "443" or "80-90"
	Protocols []string `yaml:"protocols" json:"protocols"` // empty = tcp+udp
	Action    Action   `yaml:"action" json:"action"`
	ExitID    string   `yaml:"exit_id" json:"exit_id"`
	// DatagramRequired forbids reliable stream fallback for PROXY UDP flows.
	DatagramRequired bool `yaml:"datagram_required,omitempty" json:"datagram_required,omitempty"`
	// HandleDirect keeps DIRECT local but lets RelayProxy own the socket and relay bytes.
	HandleDirect bool `yaml:"handle_direct,omitempty" json:"handle_direct,omitempty"`
}

// Config is the divert network configuration.
type Config struct {
	Mode             string   `yaml:"mode" json:"mode"` // "" | divert
	DefaultAction    Action   `yaml:"default_action" json:"default_action"`
	ExcludeProcesses []string `yaml:"exclude_processes" json:"exclude_processes"`
	Rules            []Rule   `yaml:"rules" json:"rules"`
}

// Flow is one connection/datagram subject to matching.
type Flow struct {
	Process        string // OS process identity, never inferred from a network-layer packet buffer
	ProcessID      uint32
	ProcessAliases []string // safe alternate identities, e.g. service:Dnscache when one service owns the PID
	Services       []string // Windows services hosted by this PID; shared hosts are telemetry-only
	Host           string   // requested or DNS-associated hostname if known, else empty
	DomainSource   string
	SourceIP       string
	SourcePort     uint16
	IP             string // original destination IP
	Port           uint16 // original destination port
	Protocol       Protocol
}

// Decision is the match result.
type Decision struct {
	Action           Action
	ExitID           string
	Rule             string // matched rule name, or "default" / "exclude"
	DatagramRequired bool   `yaml:"datagram_required,omitempty" json:"datagram_required,omitempty"`
	HandleDirect     bool   `yaml:"handle_direct,omitempty" json:"handle_direct,omitempty"`
}

func processBasename(p string) string {
	return path.Base(normalizeProcessPath(p))
}

func matchProcess(pattern, process string) bool {
	pat := normalizeProcessPath(pattern)
	if pat == "" || pat == "*" {
		return true
	}
	proc := normalizeProcessPath(process)
	if proc == "" {
		return false
	}
	// A path glob constrains the entire path; /opt/trusted/* must not become *.
	if !strings.Contains(pat, "/") {
		proc = path.Base(proc)
	}
	// Process matching is case-insensitive across platforms, including
	// full paths, basename globs and process aliases.
	pat, proc = strings.ToLower(pat), strings.ToLower(proc)
	matched, _ := path.Match(pat, proc)
	return matched
}

func normalizeProcessPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

