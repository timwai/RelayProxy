package divert

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"unicode/utf16"
)

func TestWFPEventRoundTripLayout(t *testing.T) {
	path := `C:\\Windows\\System32\\svchost.exe`
	pathUnits := utf16.Encode([]rune(path))
	payload := []byte{1, 2, 3, 4}
	data := make([]byte, wfpEventHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[8:12], wfpEventUDPData)
	binary.LittleEndian.PutUint32(data[12:16], wfpEventFlagOutbound)
	binary.LittleEndian.PutUint64(data[16:24], 7)
	binary.LittleEndian.PutUint64(data[24:32], 8)
	binary.LittleEndian.PutUint64(data[32:40], 1234)
	data[44], data[45] = 17, 4
	binary.LittleEndian.PutUint16(data[46:48], 53000)
	binary.LittleEndian.PutUint16(data[48:50], 53)
	binary.LittleEndian.PutUint16(data[50:52], uint16(len(pathUnits)))
	binary.LittleEndian.PutUint32(data[52:56], uint32(len(payload)))
	copy(data[56:60], []byte{192, 0, 2, 10})
	copy(data[72:76], []byte{1, 1, 1, 1})
	for index, unit := range pathUnits {
		binary.LittleEndian.PutUint16(data[88+index*2:90+index*2], unit)
	}
	copy(data[wfpEventHeaderSize:], payload)

	event, err := decodeWFPEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if event.Protocol != ProtoUDP || event.Source != netip.MustParseAddrPort("192.0.2.10:53000") || event.Destination != netip.MustParseAddrPort("1.1.1.1:53") {
		t.Fatalf("unexpected endpoints: %+v", event)
	}
	if event.ProcessPath != path || event.RequestID != 7 || event.AssociationID != 8 || string(event.Payload) != string(payload) {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestWFPVersionRequiresSystemIdentity(t *testing.T) {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], 16)
	binary.LittleEndian.PutUint64(data[8:16], wfpFeatureTCP|wfpFeatureUDP|wfpFeatureIPv6)
	if _, err := decodeWFPVersion(data); err == nil {
		t.Fatal("driver without system identity support was accepted")
	}
}

func TestWFPRedirectContextRejectsZeroRequest(t *testing.T) {
	data := make([]byte, wfpRedirectContextSize)
	binary.LittleEndian.PutUint32(data[0:4], wfpABIVersion)
	binary.LittleEndian.PutUint32(data[4:8], wfpRedirectContextSize)
	if _, err := decodeWFPRedirectContext(data); err == nil {
		t.Fatal("zero request id was accepted")
	}
}

func TestWFPDecisionFlagsLayout(t *testing.T) {
	data, err := encodeWFPDecisionFlags(99, ActionProxy, wfpDecisionFlagDNSAuto)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(data[8:16]); got != 99 {
		t.Fatalf("request id=%d", got)
	}
	if got := binary.LittleEndian.Uint32(data[16:20]); got != wfpActionProxy {
		t.Fatalf("action=%d", got)
	}
	if got := binary.LittleEndian.Uint32(data[20:24]); got != wfpDecisionFlagDNSAuto {
		t.Fatalf("flags=%x", got)
	}
}
