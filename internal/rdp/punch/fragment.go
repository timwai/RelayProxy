// Package punch preserves the former RDP punch import path.
// New direct-path code should use relayproxy/internal/p2p/punch.
package punch

import p2ppunch "relayproxy/internal/p2p/punch"

type LookupKey = p2ppunch.LookupKey
type UDPResult = p2ppunch.UDPResult
type Reassembler = p2ppunch.Reassembler
type PacketConn = p2ppunch.PacketConn

var (
	Dial           = p2ppunch.Dial
	Accept         = p2ppunch.Accept
	FragmentUDP    = p2ppunch.FragmentUDP
	NewReassembler = p2ppunch.NewReassembler
	Punch          = p2ppunch.Punch
	DecodePunch    = p2ppunch.DecodePunch
	WritePunchAck  = p2ppunch.WritePunchAck
	NewPacketConn  = p2ppunch.NewPacketConn
)
