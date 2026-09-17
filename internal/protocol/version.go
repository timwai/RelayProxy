package protocol

const (
	// MagicHeader identifies the RelayProxy stream protocol ('R', 'P')
	MagicHeader uint16 = 0x5250

	// CurrentVersion is the M3-M5 stream protocol generation. P2P signaling,
	// leases and ingress negotiation are intentionally a hard protocol break;
	// older M1/M2 agents are rejected instead of being downgraded.
	CurrentVersion uint8 = 3
)
