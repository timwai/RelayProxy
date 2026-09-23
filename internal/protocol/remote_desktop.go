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

type DesktopCaptureBackend string

const (
	DesktopCaptureAuto DesktopCaptureBackend = "auto"
	DesktopCaptureDXGI DesktopCaptureBackend = "dxgi"
	DesktopCaptureGDI  DesktopCaptureBackend = "gdi"
	DesktopCaptureWGC  DesktopCaptureBackend = "wgc"
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

type DesktopCaptureCapability struct {
	Backend    string `json:"backend"`
	DirtyRects bool   `json:"dirtyRects,omitempty"`
	MoveRects  bool   `json:"moveRects,omitempty"`
	Cursor     bool   `json:"cursor,omitempty"`
}

// DesktopCodecH265Validation is an internal diagnostics-only codec sentinel.
// It must never be advertised as a normal codec capability or rendered as a
// user-facing codec choice.
const DesktopCodecH265Validation = "h265-validation"

type DesktopCodecCapability struct {
	Codec      string `json:"codec"`
	Encoder    string `json:"encoder,omitempty"`
	Hardware   bool   `json:"hardware,omitempty"`
	Encode     bool   `json:"encode,omitempty"`
	Decode     bool   `json:"decode,omitempty"`
	Chroma420  bool   `json:"chroma420,omitempty"`
	Chroma444  bool   `json:"chroma444,omitempty"`
	BitDepth8  bool   `json:"bitDepth8,omitempty"`
	BitDepth10 bool   `json:"bitDepth10,omitempty"`
	MaxWidth   int    `json:"maxWidth,omitempty"`
	MaxHeight  int    `json:"maxHeight,omitempty"`
	MaxFPS     int    `json:"maxFps,omitempty"`
}

type DesktopDisplayCapability struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	RefreshHz int    `json:"refreshHz,omitempty"`
	Primary   bool   `json:"primary,omitempty"`
	HDR       bool   `json:"hdr,omitempty"`
}

// DesktopCapabilities is the summary safe to expose to a controller before a
// desktop session starts. Detailed codec negotiation still happens per session.
type DesktopCapabilities struct {
	NativeRDP      bool                       `json:"nativeRdp"`
	RelayDesktop   bool                       `json:"relayDesktop"`
	Captures       []DesktopCaptureCapability `json:"captures,omitempty"`
	Codecs         []DesktopCodecCapability   `json:"codecs,omitempty"`
	Displays       []DesktopDisplayCapability `json:"displays,omitempty"`
	Audio          bool                       `json:"audio,omitempty"`
	Clipboard      bool                       `json:"clipboard,omitempty"`
	MultiMonitor   bool                       `json:"multiMonitor,omitempty"`
	HDR            bool                       `json:"hdr,omitempty"`
	VirtualDisplay bool                       `json:"virtualDisplay,omitempty"`
	MaxWidth       int                        `json:"maxWidth,omitempty"`
	MaxHeight      int                        `json:"maxHeight,omitempty"`
	MaxFPS         int                        `json:"maxFps,omitempty"`
}

// RemoteDesktopTarget is the unified target model used by Agent UI and future
// Relay Desktop negotiation. Existing RDP targets are adapted into this model.
type RemoteDesktopTarget struct {
	DeviceID     string              `json:"deviceId"`
	Name         string              `json:"name"`
	Online       bool                `json:"online"`
	Capabilities DesktopCapabilities `json:"capabilities"`
}

type DesktopResolutionOptions struct {
	Mode      string `json:"mode,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	MaxWidth  int    `json:"maxWidth,omitempty"`
	MaxHeight int    `json:"maxHeight,omitempty"`
}

// RemoteDesktopConnectOptions is shared by Native RDP and Relay Desktop.
// Backend-specific implementations ignore fields that do not apply to them.
type RemoteDesktopConnectOptions struct {
	Backend        DesktopBackend           `json:"backend,omitempty"`
	Scene          DesktopScene             `json:"scene,omitempty"`
	Quality        DesktopQuality           `json:"quality,omitempty"`
	Codec          string                   `json:"codec,omitempty"`
	CaptureBackend DesktopCaptureBackend    `json:"captureBackend,omitempty"`
	Resolution     DesktopResolutionOptions `json:"resolution,omitempty"`
	FPS            int                      `json:"fps,omitempty"`
	MaxBitrate     int                      `json:"maxBitrate,omitempty"`
	DisplayID      string                   `json:"displayId,omitempty"`
	Clipboard      *bool                    `json:"clipboard,omitempty"`
	Audio          *bool                    `json:"audio,omitempty"`
	AutoLaunch     *bool                    `json:"autoLaunch,omitempty"`
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
	State       string         `json:"state"`
	Backend     DesktopBackend `json:"backend,omitempty"`
	TargetID    string         `json:"targetId,omitempty"`
	TargetName  string         `json:"targetName,omitempty"`
	DisplayID   string         `json:"displayId,omitempty"`
	DisplayName string         `json:"displayName,omitempty"`
	Generation  uint32         `json:"generation,omitempty"`
	Codec       string         `json:"codec,omitempty"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	MaxWidth    int            `json:"maxWidth,omitempty"`
	MaxHeight   int            `json:"maxHeight,omitempty"`
	FPS         int            `json:"fps,omitempty"`
	ListenAddr  string         `json:"listenAddr,omitempty"`
	PathTCP     string         `json:"pathTcp,omitempty"`
	PathUDP     string         `json:"pathUdp,omitempty"`
	UDPEnabled  bool           `json:"udpEnabled,omitempty"`
	UDPActive   bool           `json:"udpActive,omitempty"`
	UDPReason   string         `json:"udpReason,omitempty"`
}

// RemoteDesktopFrame is the MVP viewer surface. JPEG bytes are carried only
// across the local Agent -> Wails bridge; Relay transport uses binary RD/1
// datagrams and never base64-encodes media on the network.
type RemoteDesktopFrame struct {
	Sequence   uint64 `json:"sequence"`
	Generation uint32 `json:"generation,omitempty"`
	MimeType   string `json:"mimeType"`
	Codec      string `json:"codec,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Timestamp  uint64 `json:"timestamp,omitempty"`
	KeyFrame   bool   `json:"keyFrame,omitempty"`
	Data       []byte `json:"data,omitempty"`
}

// DesktopInputEvent is a normalized interactive input event carried on the
// reliable side channel of a Relay Desktop media association. Pointer
// coordinates use the Windows SendInput absolute range [0, 65535].
type DesktopInputKind string

const (
	DesktopInputKeyDown    DesktopInputKind = "key_down"
	DesktopInputKeyUp      DesktopInputKind = "key_up"
	DesktopInputMouseMove  DesktopInputKind = "mouse_move"
	DesktopInputMouseDown  DesktopInputKind = "mouse_button_down"
	DesktopInputMouseUp    DesktopInputKind = "mouse_button_up"
	DesktopInputMouseWheel DesktopInputKind = "mouse_wheel"
)

const (
	DesktopMouseButtonLeft   = "left"
	DesktopMouseButtonRight  = "right"
	DesktopMouseButtonMiddle = "middle"
	DesktopMouseButtonX1     = "x1"
	DesktopMouseButtonX2     = "x2"
)

type DesktopInputEvent struct {
	Sequence   uint64           `json:"sequence,omitempty"`
	Kind       DesktopInputKind `json:"kind"`
	VirtualKey uint16           `json:"virtualKey,omitempty"`
	Extended   bool             `json:"extended,omitempty"`
	X          uint16           `json:"x,omitempty"`
	Y          uint16           `json:"y,omitempty"`
	Button     string           `json:"button,omitempty"`
	WheelDelta int32            `json:"wheelDelta,omitempty"`
	Horizontal bool             `json:"horizontal,omitempty"`
}

const (
	DesktopSessionInput        = "input"
	DesktopSessionVideoConfig  = "video_config"
	DesktopSessionAudioConfig  = "audio_config"
	DesktopSessionIDRRequest   = "idr_request"
	DesktopSessionCursor       = "cursor"
	DesktopSessionClipboard    = "clipboard"
	DesktopSessionPing         = "ping"
	DesktopSessionPong         = "pong"
	DesktopSessionStatsReport  = "stats"
	DesktopSessionVideoControl = "video_control"
)

type DesktopSessionMessage struct {
	Type         string                 `json:"type"`
	Input        *DesktopInputEvent     `json:"input,omitempty"`
	VideoConfig  *DesktopVideoConfig    `json:"videoConfig,omitempty"`
	AudioConfig  *DesktopAudioConfig    `json:"audioConfig,omitempty"`
	Cursor       *DesktopCursorState    `json:"cursor,omitempty"`
	Clipboard    *DesktopClipboardState `json:"clipboard,omitempty"`
	Probe        *DesktopSessionProbe   `json:"probe,omitempty"`
	Stats        *DesktopSessionStats   `json:"stats,omitempty"`
	VideoControl *DesktopVideoControl   `json:"videoControl,omitempty"`
}

const (
	DesktopControlCapabilities    = "capabilities"
	DesktopControlConnectRequest  = "connect_request"
	DesktopControlConnectNotify   = "connect_notify"
	DesktopControlConnectResponse = "connect_response"
	DesktopControlConfig          = "config"
	DesktopControlConfigAck       = "config_ack"
	DesktopControlStats           = "stats"
	DesktopControlIDRRequest      = "idr_request"
	DesktopControlPathChange      = "path_change"
	DesktopControlLeaseRenew      = "lease_renew"
	DesktopControlLeaseAck        = "lease_ack"
	DesktopControlSessionClose    = "session_close"
	DesktopControlError           = "error"
)

type DesktopSessionProbe struct {
	Sequence uint64 `json:"sequence"`
	SentAtUS int64  `json:"sentAtUs"`
}

const MaxDesktopClipboardBytes = 1 << 20

type DesktopClipboardState struct {
	Sequence uint64 `json:"sequence"`
	Text     string `json:"text"`
}

type DesktopCursorState struct {
	Sequence     uint64 `json:"sequence"`
	X            int    `json:"x"`
	Y            int    `json:"y"`
	ScreenWidth  int    `json:"screenWidth"`
	ScreenHeight int    `json:"screenHeight"`
	Visible      bool   `json:"visible"`
	CursorID     string `json:"cursorId,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	HotspotX     int    `json:"hotspotX,omitempty"`
	HotspotY     int    `json:"hotspotY,omitempty"`
	PNG          []byte `json:"png,omitempty"`
}

type DesktopVideoControl struct {
	TargetBitrate int `json:"targetBitrate,omitempty"`
	TargetFPS     int `json:"targetFps,omitempty"`
	TargetWidth   int `json:"targetWidth,omitempty"`
	TargetHeight  int `json:"targetHeight,omitempty"`
}

const (
	DesktopAudioCodecPCMS16LE = "pcm_s16le"
	DesktopAudioCodecOpus     = "opus"
)

// DesktopAudioConfig describes one audio generation carried on the dedicated
// RD/1 audio media stream. The first implementation may use PCM for bring-up;
// the model intentionally supports compressed codecs without another protocol
// revision.
type DesktopAudioConfig struct {
	Generation      uint32 `json:"generation"`
	Codec           string `json:"codec"`
	SampleRate      int    `json:"sampleRate"`
	Channels        int    `json:"channels"`
	BitsPerSample   int    `json:"bitsPerSample,omitempty"`
	FrameDurationMs int    `json:"frameDurationMs,omitempty"`
	TargetBitrate   int    `json:"targetBitrate,omitempty"`
}

type DesktopVideoConfig struct {
	Generation    uint32 `json:"generation"`
	Codec         string `json:"codec"`
	CodecString   string `json:"codecString,omitempty"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	MaxWidth      int    `json:"maxWidth,omitempty"`
	MaxHeight     int    `json:"maxHeight,omitempty"`
	FPS           int    `json:"fps"`
	TargetBitrate int    `json:"targetBitrate"`
	MaxBitrate    int    `json:"maxBitrate,omitempty"`
	Chroma        string `json:"chroma,omitempty"`
	BitDepth      int    `json:"bitDepth,omitempty"`
	DisplayID     string `json:"displayId,omitempty"`
}

type DesktopSessionStats struct {
	CaptureFPS       float64 `json:"captureFps,omitempty"`
	EncodeFPS        float64 `json:"encodeFps,omitempty"`
	ReceiveFPS       float64 `json:"receiveFps,omitempty"`
	DecodeFPS        float64 `json:"decodeFps,omitempty"`
	RenderFPS        float64 `json:"renderFps,omitempty"`
	ActualBitrate    int64   `json:"actualBitrate,omitempty"`
	TargetBitrate    int64   `json:"targetBitrate,omitempty"`
	TargetFPS        int     `json:"targetFps,omitempty"`
	RTTMs            float64 `json:"rttMs,omitempty"`
	JitterMs         float64 `json:"jitterMs,omitempty"`
	LossPercent      float64 `json:"lossPercent,omitempty"`
	DeliveryRate     int64   `json:"deliveryRate,omitempty"`
	SendQueueDelayMs float64 `json:"sendQueueDelayMs,omitempty"`
	CaptureMs        float64 `json:"captureMs,omitempty"`
	EncodeMs         float64 `json:"encodeMs,omitempty"`
	DecodeMs         float64 `json:"decodeMs,omitempty"`
	RenderMs         float64 `json:"renderMs,omitempty"`
	DroppedFrames    uint64  `json:"droppedFrames,omitempty"`
	Path             string  `json:"path,omitempty"`
	CaptureBackend   string  `json:"captureBackend,omitempty"`
	CaptureFormat    string  `json:"captureFormat,omitempty"`
	EncoderBackend   string  `json:"encoderBackend,omitempty"`
	EncoderHardware  bool    `json:"encoderHardware,omitempty"`
	DecoderBackend   string  `json:"decoderBackend,omitempty"`
	DecoderHardware  bool    `json:"decoderHardware,omitempty"`
}

// DesktopControlMessage is carried over FrameTypeDesktopControl. The server
// derives the sender identity from DeviceSession; ControllerID/TargetID are
// descriptive fields and must not be trusted as authentication.
type DesktopControlMessage struct {
	Type           string                       `json:"type"`
	SessionID      uint64                       `json:"sessionId,omitempty"`
	ControllerID   string                       `json:"controllerId,omitempty"`
	TargetID       string                       `json:"targetId,omitempty"`
	SessionToken   []byte                       `json:"sessionToken,omitempty"`
	Capabilities   *DesktopCapabilities         `json:"capabilities,omitempty"`
	Options        *RemoteDesktopConnectOptions `json:"options,omitempty"`
	Config         *DesktopVideoConfig          `json:"config,omitempty"`
	Stats          *DesktopSessionStats         `json:"stats,omitempty"`
	LeaseExpiresAt int64                        `json:"leaseExpiresAt,omitempty"`
	LeaseSec       int                          `json:"leaseSec,omitempty"`
	Path           string                       `json:"path,omitempty"`
	ErrorCode      string                       `json:"errorCode,omitempty"`
	ErrorMessage   string                       `json:"errorMessage,omitempty"`
}
