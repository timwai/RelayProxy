package divert

type NetworkServiceStatus struct {
	Supported       bool   `json:"supported"`
	Installed       bool   `json:"installed"`
	Running         bool   `json:"running"`
	Ready           bool   `json:"ready"`
	VersionMatch    bool   `json:"versionMatch"`
	RecoveryEnabled bool   `json:"recoveryEnabled"`
	RecoveryKnown   bool   `json:"recoveryKnown"`
	PID             uint32 `json:"pid,omitempty"`
	BinaryPath      string `json:"binaryPath,omitempty"`
	State           string `json:"state"`
	Message         string `json:"message,omitempty"`
}
