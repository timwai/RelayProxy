// Package candidate preserves the former RDP candidate import path.
// New direct-path code should use relayproxy/internal/p2p/candidate.
package candidate

import p2pcandidate "relayproxy/internal/p2p/candidate"

const (
	MaxCandidates = p2pcandidate.MaxCandidates
	ProbeMagic    = p2pcandidate.ProbeMagic
	ProbeVersion  = p2pcandidate.ProbeVersion
)

var (
	ErrInvalidCandidate = p2pcandidate.ErrInvalidCandidate
	ErrProbeUnavailable = p2pcandidate.ErrProbeUnavailable

	Validate       = p2pcandidate.Validate
	Discover       = p2pcandidate.Discover
	ProbeReflexive = p2pcandidate.ProbeReflexive
)
