package protocol

// DesktopBackend identifies the remote desktop implementation used for a
// session. "auto" is a controller preference and is never reported as an
// active session backend.
type DesktopBackend string

const (
	DesktopBackendAuto  DesktopBackend = "auto"
	DesktopBackendRDP   DesktopBackend = "rdp"
	DesktopBackendRelay DesktopBackend = "relay"
)

// DesktopScene describes the user's intent. It is deliberately higher level
// than codec/bitrate knobs so backend and media policy can evolve independently.
type DesktopScene string

const (
	DesktopSceneAuto        DesktopScene = "auto"
	DesktopSceneOffice      DesktopScene = "office"
	DesktopScenePerformance DesktopScene = "performance"
	DesktopSceneGaming      DesktopScene = "gaming"
	DesktopSceneQuality     DesktopScene = "quality"
)

type DesktopQuality string

const (
	DesktopQualityAuto     DesktopQuality = "auto"
	DesktopQualitySmooth   DesktopQuality = "smooth"
	DesktopQualityBalanced DesktopQuality = "balanced"
	DesktopQualityHigh     DesktopQuality = "high"
	DesktopQualityExtreme  DesktopQuality = "extreme"
	DesktopQualityCustom   DesktopQuality = "custom"
)

// DesktopCapabilities is the summary safe to expose to a controller before a
// desktop session starts. Detailed codec negotiation belongs to the session.
type DesktopCapabilities struct {
	NativeRDP    bool `json:"nativeRdp"`
	RelayDesktop bool `json:"relayDesktop"`
}

// RemoteDesktopTarget is the unified target model used by Agent UI and future
// Relay Desktop negotiation. Existing RDP targets are adapted into this model.
type RemoteDesktopTarget struct {
	DeviceID     string              `json:"deviceId"`
	Name         string              `json:"name"`
	Online       bool                `json:"online"`
	Capabilities DesktopCapabilities `json:"capabilities"`
}

// RemoteDesktopConnectOptions intentionally starts small. Media-specific fields
// will be added when Relay Desktop lands; legacy RDP ignores them.
type RemoteDesktopConnectOptions struct {
	Backend    DesktopBackend `json:"backend,omitempty"`
	Scene      DesktopScene   `json:"scene,omitempty"`
	Quality    DesktopQuality `json:"quality,omitempty"`
	AutoLaunch *bool          `json:"autoLaunch,omitempty"`
}

// RemoteDesktopSessionInfo is returned after a backend has successfully
// prepared a session. For Native RDP, ListenAddr is the local loopback endpoint
// handed to mstsc.
type RemoteDesktopSessionInfo struct {
	Target       RemoteDesktopTarget `json:"target"`
	Backend      DesktopBackend      `json:"backend"`
	State        string              `json:"state"`
	ListenAddr   string              `json:"listenAddr,omitempty"`
	PathTCP      string              `json:"pathTcp,omitempty"`
	PathUDP      string              `json:"pathUdp,omitempty"`
	UDPEnabled   bool                `json:"udpEnabled,omitempty"`
	AutoLaunched bool                `json:"autoLaunched,omitempty"`
}

// RemoteDesktopStatus is a stable UI-facing snapshot. The GUI must not infer
// desktop state by parsing log lines.
type RemoteDesktopStatus struct {
	State      string         `json:"state"`
	Backend    DesktopBackend `json:"backend,omitempty"`
	TargetID   string         `json:"targetId,omitempty"`
	TargetName string         `json:"targetName,omitempty"`
	ListenAddr string         `json:"listenAddr,omitempty"`
	PathTCP    string         `json:"pathTcp,omitempty"`
	PathUDP    string         `json:"pathUdp,omitempty"`
	UDPEnabled bool           `json:"udpEnabled,omitempty"`
	UDPActive  bool           `json:"udpActive,omitempty"`
	UDPReason  string         `json:"udpReason,omitempty"`
}
