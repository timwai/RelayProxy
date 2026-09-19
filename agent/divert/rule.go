package divert

import (
	"path"
	"runtime"
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
}

// Config is the divert network configuration.
const (
	DNSModeRule   = "rule"
	DNSModeAuto   = "auto"
	DNSModeDirect = "direct"
	DNSModeProxy  = "proxy"
)

type Config struct {
	Mode             string   `yaml:"mode" json:"mode"` // "" | divert
	DNSMode          string   `yaml:"dns_mode,omitempty" json:"dns_mode,omitempty"`
	DefaultAction    Action   `yaml:"default_action" json:"default_action"`
	ExcludeProcesses []string `yaml:"exclude_processes" json:"exclude_processes"`
	Rules            []Rule   `yaml:"rules" json:"rules"`
}

// Flow is one connection/datagram subject to matching.
type Flow struct {
	Process      string // OS process identity, never inferred from a network-layer packet buffer
	ProcessID    uint32
	Host         string // requested or DNS-associated hostname if known, else empty
	DomainSource string
	SourceIP     string
	SourcePort   uint16
	IP           string // original destination IP
	Port         uint16 // original destination port
	Protocol     Protocol
}

// Decision is the match result.
type Decision struct {
	Action           Action
	ExitID           string
	Rule             string // matched rule name, or "default" / "exclude"
	DatagramRequired bool   `yaml:"datagram_required,omitempty" json:"datagram_required,omitempty"`
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
	if runtime.GOOS == "windows" || windowsProcessPath(pattern) || windowsProcessPath(process) {
		pat, proc = strings.ToLower(pat), strings.ToLower(proc)
	}
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

func windowsProcessPath(p string) bool {
	return strings.Contains(p, "\\") || (len(p) >= 2 && p[1] == ':')
}
