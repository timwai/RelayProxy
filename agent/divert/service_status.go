package divert

type NetworkServiceStatus struct {
	Supported       bool   `json:"supported"`
	Installed       bool   `json:"installed"`
	Running         bool   `json:"running"`
	AutoStart       bool   `json:"autoStart"`
	AutoStartKnown  bool   `json:"autoStartKnown"`
	Ready           bool   `json:"ready"`
	VersionMatch    bool   `json:"versionMatch"`
	RecoveryEnabled bool   `json:"recoveryEnabled"`
	RecoveryKnown   bool   `json:"recoveryKnown"`
	PID             uint32 `json:"pid,omitempty"`
	BinaryPath      string `json:"binaryPath,omitempty"`
	State           string `json:"state"`
	Message         string `json:"message,omitempty"`
}

type NetworkServiceUninstallResult struct {
	RebootCleanup bool   `json:"rebootCleanup"`
	CleanupPath   string `json:"cleanupPath,omitempty"`
}
