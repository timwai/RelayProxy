// Package secure preserves the former RDP direct-path security import path.
// New direct-path code should use relayproxy/internal/p2p/secure.
package secure

import p2psecure "relayproxy/internal/p2p/secure"

const (
	PunchMagic     = p2psecure.PunchMagic
	DataMagic      = p2psecure.DataMagic
	WireVersion    = p2psecure.WireVersion
	PunchRequest   = p2psecure.PunchRequest
	PunchAck       = p2psecure.PunchAck
	PunchKeep      = p2psecure.PunchKeep
	MaxDataPayload = p2psecure.MaxDataPayload
)

var (
	ErrInvalidPacket = p2psecure.ErrInvalidPacket
	ErrBadMAC         = p2psecure.ErrBadMAC
	ErrReplay         = p2psecure.ErrReplay
	ErrShortBuffer    = p2psecure.ErrShortBuffer

	DecodePunchPacket = p2psecure.DecodePunchPacket
	NewDataCodec      = p2psecure.NewDataCodec
)

type PunchPacket = p2psecure.PunchPacket
type DataCodec = p2psecure.DataCodec
