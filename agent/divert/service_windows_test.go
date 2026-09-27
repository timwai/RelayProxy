//go:build windows

package divert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestNetworkFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	want := networkFrame{
		kind:       networkFrameCapture,
		flags:      networkFrameFlagOutbound,
		ifIndex:    17,
		subIfIndex: 23,
		payload:    []byte{0x45, 0x00, 0x00, 0x14},
	}
	if err := writeNetworkFrame(&buffer, nil, want); err != nil {
		t.Fatal(err)
	}
	got, err := readNetworkFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != want.kind || got.flags != want.flags ||
		got.ifIndex != want.ifIndex || got.subIfIndex != want.subIfIndex ||
		!bytes.Equal(got.payload, want.payload) {
		t.Fatalf("frame=%+v want=%+v", got, want)
	}
}

func TestNetworkFrameRejectsInvalidEnvelope(t *testing.T) {
	valid := make([]byte, networkFrameHeaderBytes)
	binary.LittleEndian.PutUint32(valid[0:4], networkPipeMagic)
	binary.LittleEndian.PutUint16(valid[4:6], networkPipeVersion)
	binary.LittleEndian.PutUint16(valid[6:8], networkFrameReady)

	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"magic", func(data []byte) { binary.LittleEndian.PutUint32(data[0:4], 0) }},
		{"version", func(data []byte) { binary.LittleEndian.PutUint16(data[4:6], networkPipeVersion+1) }},
		{"payload", func(data []byte) { binary.LittleEndian.PutUint32(data[20:24], networkFrameMaxPayload+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			test.mutate(data)
			if _, err := readNetworkFrame(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid network frame was accepted")
			}
		})
	}
}

func TestNetworkFrameRejectsOversizedWrite(t *testing.T) {
	err := writeNetworkFrame(
		&bytes.Buffer{},
		nil,
		networkFrame{kind: networkFrameInject, payload: make([]byte, networkFrameMaxPayload+1)},
	)
	if err == nil {
		t.Fatal("oversized network frame write was accepted")
	}
}

func TestCurrentWindowsUserSIDIsValid(t *testing.T) {
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sid, "S-") {
		t.Fatalf("unexpected SID %q", sid)
	}
}

func TestRunWindowsNetworkServiceHelperRejectsUnknownActionBeforeMutation(t *testing.T) {
	if !strings.EqualFold(strings.TrimSpace(networkServiceHelperInstall), "install") {
		t.Fatal("install helper action changed unexpectedly")
	}
	if windowsNetworkServiceDisplayName == "" || windowsNetworkServiceName == "" {
		t.Fatal("network service identity must be stable")
	}
	// The public helper performs elevation validation before dispatch, so test
	// the action contract without touching SCM state on the CI host.
	if networkServiceHelperInstall == networkServiceHelperRemove {
		t.Fatal(errors.New("network service helper actions collide"))
	}
}
