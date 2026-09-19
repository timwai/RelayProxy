package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"unicode/utf16"
)

const (
	wfpABIVersion          = 1
	wfpEventHeaderSize     = 1128
	wfpMaxPathChars        = 520
	wfpMaxEventPayload     = 65507
	wfpRedirectContextSize = 16
)

const (
	wfpFeatureTCP uint64 = 1 << iota
	wfpFeatureUDP
	wfpFeatureIPv6
	wfpFeatureSystemIdentity
)

const (
	wfpEventFlow uint32 = 1 + iota
	wfpEventUDPData
	wfpEventDNS
	wfpEventClose
)

const (
	wfpEventFlagSystem uint32 = 1 << iota
	wfpEventFlagOutbound
)

const (
	wfpActionDirect uint32 = 1 + iota
	wfpActionProxy
	wfpActionReject
)

const wfpDecisionFlagDNSAuto uint32 = 1 << 0

type wfpVersion struct {
	ABI      uint32
	Size     uint32
	Features uint64
}

type wfpEvent struct {
	Kind          uint32
	Flags         uint32
	RequestID     uint64
	AssociationID uint64
	ProcessID     uint64
	CompartmentID uint32
	Protocol      Protocol
	Family        uint8
	Source        netip.AddrPort
	Destination   netip.AddrPort
	ProcessPath   string
	Payload       []byte
}

func decodeWFPVersion(data []byte) (wfpVersion, error) {
	if len(data) < 16 {
		return wfpVersion{}, errors.New("wfp: short version response")
	}
	v := wfpVersion{
		ABI:      binary.LittleEndian.Uint32(data[0:4]),
		Size:     binary.LittleEndian.Uint32(data[4:8]),
		Features: binary.LittleEndian.Uint64(data[8:16]),
	}
	if v.ABI != wfpABIVersion || v.Size < 16 {
		return wfpVersion{}, fmt.Errorf("wfp: incompatible driver ABI %d size %d", v.ABI, v.Size)
	}
	required := wfpFeatureTCP | wfpFeatureUDP | wfpFeatureIPv6 | wfpFeatureSystemIdentity
	if v.Features&required != required {
		return wfpVersion{}, fmt.Errorf("wfp: driver features 0x%x missing required 0x%x", v.Features, required)
	}
	return v, nil
}

func encodeWFPConfig(pid uint32, port4, port6 uint16, heartbeatMS uint32) []byte {
	data := make([]byte, 24)
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[8:12], pid)
	binary.LittleEndian.PutUint16(data[12:14], port4)
	binary.LittleEndian.PutUint16(data[14:16], port6)
	binary.LittleEndian.PutUint32(data[16:20], heartbeatMS)
	return data
}

func encodeWFPDecision(requestID uint64, action Action) ([]byte, error) {
	return encodeWFPDecisionFlags(requestID, action, 0)
}

func encodeWFPDecisionFlags(requestID uint64, action Action, flags uint32) ([]byte, error) {
	var native uint32
	switch action {
	case ActionDirect:
		native = wfpActionDirect
	case ActionProxy:
		native = wfpActionProxy
	case ActionReject:
		native = wfpActionReject
	default:
		return nil, fmt.Errorf("wfp: invalid decision action %q", action)
	}
	data := make([]byte, 24)
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint64(data[8:16], requestID)
	binary.LittleEndian.PutUint32(data[16:20], native)
	binary.LittleEndian.PutUint32(data[20:24], flags)
	return data, nil
}

func encodeWFPProxyReady(ready bool) []byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)))
	if ready {
		binary.LittleEndian.PutUint32(data[8:12], 1)
	}
	return data
}

func encodeWFPRelease(requestID uint64) ([]byte, error) {
	if requestID == 0 {
		return nil, errors.New("wfp: missing request id")
	}
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint64(data[8:16], requestID)
	return data, nil
}

func encodeWFPUDPInjection(associationID uint64, payload []byte) ([]byte, error) {
	if associationID == 0 {
		return nil, errors.New("wfp: missing UDP association id")
	}
	if len(payload) > wfpMaxEventPayload {
		return nil, fmt.Errorf("wfp: UDP payload exceeds %d bytes", wfpMaxEventPayload)
	}
	data := make([]byte, 24+len(payload))
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint64(data[8:16], associationID)
	binary.LittleEndian.PutUint32(data[16:20], uint32(len(payload)))
	copy(data[24:], payload)
	return data, nil
}

func decodeWFPEvent(data []byte) (wfpEvent, error) {
	if len(data) < wfpEventHeaderSize {
		return wfpEvent{}, errors.New("wfp: short event")
	}
	abi := binary.LittleEndian.Uint32(data[0:4])
	size := binary.LittleEndian.Uint32(data[4:8])
	if abi != wfpABIVersion || size < wfpEventHeaderSize || int(size) > len(data) {
		return wfpEvent{}, fmt.Errorf("wfp: invalid event ABI/size %d/%d", abi, size)
	}
	pathChars := int(binary.LittleEndian.Uint16(data[50:52]))
	payloadLen := int(binary.LittleEndian.Uint32(data[52:56]))
	if pathChars > wfpMaxPathChars || payloadLen > wfpMaxEventPayload || wfpEventHeaderSize+payloadLen > int(size) {
		return wfpEvent{}, errors.New("wfp: invalid event lengths")
	}
	protoByte := data[44]
	var proto Protocol
	switch protoByte {
	case 6:
		proto = ProtoTCP
	case 17:
		proto = ProtoUDP
	default:
		return wfpEvent{}, fmt.Errorf("wfp: unsupported protocol %d", protoByte)
	}
	family := data[45]
	srcAddr, err := decodeWFPAddr(family, data[56:72])
	if err != nil {
		return wfpEvent{}, err
	}
	dstAddr, err := decodeWFPAddr(family, data[72:88])
	if err != nil {
		return wfpEvent{}, err
	}
	pathUnits := make([]uint16, pathChars)
	for i := range pathUnits {
		pathUnits[i] = binary.LittleEndian.Uint16(data[88+i*2 : 90+i*2])
	}
	path := string(utf16.Decode(pathUnits))
	payload := append([]byte(nil), data[wfpEventHeaderSize:wfpEventHeaderSize+payloadLen]...)
	return wfpEvent{
		Kind:          binary.LittleEndian.Uint32(data[8:12]),
		Flags:         binary.LittleEndian.Uint32(data[12:16]),
		RequestID:     binary.LittleEndian.Uint64(data[16:24]),
		AssociationID: binary.LittleEndian.Uint64(data[24:32]),
		ProcessID:     binary.LittleEndian.Uint64(data[32:40]),
		CompartmentID: binary.LittleEndian.Uint32(data[40:44]),
		Protocol:      proto,
		Family:        family,
		Source:        netip.AddrPortFrom(srcAddr, binary.LittleEndian.Uint16(data[46:48])),
		Destination:   netip.AddrPortFrom(dstAddr, binary.LittleEndian.Uint16(data[48:50])),
		ProcessPath:   path,
		Payload:       payload,
	}, nil
}

func decodeWFPAddr(family uint8, raw []byte) (netip.Addr, error) {
	if len(raw) < 16 {
		return netip.Addr{}, errors.New("wfp: short address")
	}
	switch family {
	case 4:
		var a [4]byte
		copy(a[:], raw[:4])
		return netip.AddrFrom4(a), nil
	case 6:
		var a [16]byte
		copy(a[:], raw[:16])
		return netip.AddrFrom16(a).Unmap(), nil
	default:
		return netip.Addr{}, fmt.Errorf("wfp: unsupported address family %d", family)
	}
}

func decodeWFPRedirectContext(data []byte) (uint64, error) {
	if len(data) < wfpRedirectContextSize {
		return 0, errors.New("wfp: short redirect context")
	}
	if binary.LittleEndian.Uint32(data[0:4]) != wfpABIVersion || binary.LittleEndian.Uint32(data[4:8]) < wfpRedirectContextSize {
		return 0, errors.New("wfp: invalid redirect context")
	}
	id := binary.LittleEndian.Uint64(data[8:16])
	if id == 0 {
		return 0, errors.New("wfp: redirect context has no request id")
	}
	return id, nil
}
