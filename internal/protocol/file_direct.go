package protocol

// FileDirectOffer is issued only on an authenticated Server Relay session,
// after checking both files.client and files.host capabilities.
type FileDirectOffer struct {
	Token        string   `json:"token,omitempty"`
	Endpoints    []string `json:"endpoints,omitempty"`
	UDPEndpoints []string `json:"udpEndpoints,omitempty"`
	Fingerprint  string   `json:"fingerprint,omitempty"`
	ExpiresAt    int64    `json:"expiresAt,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type FileDirectGrant struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

type FileDirectAuth struct {
	Token string `json:"token"`
}

type FileDirectAuthResult struct {
	Allowed bool   `json:"allowed"`
	Error   string `json:"error,omitempty"`
}
