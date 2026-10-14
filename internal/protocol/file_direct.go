package protocol

// FileDirectOffer is issued only on an authenticated Server Relay session,
// after checking both files.client and files.host capabilities.
type FileDirectOffer struct {
	Token           string         `json:"token,omitempty"`
	Endpoints       []string       `json:"endpoints,omitempty"`
	UDPEndpoints    []string       `json:"udpEndpoints,omitempty"`
	Fingerprint     string         `json:"fingerprint,omitempty"`
	ExpiresAt       int64          `json:"expiresAt,omitempty"`
	Error           string         `json:"error,omitempty"`
	PunchSessionID  uint64         `json:"punchSessionId,omitempty"`
	PunchKey        []byte         `json:"punchKey,omitempty"`
	PunchCandidates []P2PCandidate `json:"punchCandidates,omitempty"`
}

type FileDirectGrant struct {
	Token          string         `json:"token"`
	ExpiresAt      int64          `json:"expiresAt"`
	PunchSessionID uint64         `json:"punchSessionId,omitempty"`
	PunchKey       []byte         `json:"punchKey,omitempty"`
	PeerCandidates []P2PCandidate `json:"peerCandidates,omitempty"`
}

type FileDirectAuth struct {
	Token string `json:"token"`
}

type FileDirectAuthResult struct {
	Allowed bool   `json:"allowed"`
	Error   string `json:"error,omitempty"`
}

// FileDirectPunchPrepare is a separate frame, preserving old Agent handshakes.
type FileDirectPunchPrepare struct {
	Candidates []P2PCandidate `json:"candidates"`
}
