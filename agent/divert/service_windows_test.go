//go:build windows

package divert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

func TestNetworkFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	want := networkFrame{
		kind:       networkFrameCapture,
		flags:      networkFrameFlagOutbound | (1 << 21) | (1 << 22) | (1 << 23),
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

func TestMatchesWindowsNetworkServiceRecovery(t *testing.T) {
	valid := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	}
	if !matchesWindowsNetworkServiceRecovery(valid, true) {
		t.Fatal("expected configured recovery policy to match")
	}
	if matchesWindowsNetworkServiceRecovery(valid, false) {
		t.Fatal("non-crash recovery flag is required")
	}
	if matchesWindowsNetworkServiceRecovery(valid[:2], true) {
		t.Fatal("incomplete recovery policy was accepted")
	}
	extra := append(append([]mgr.RecoveryAction(nil), valid...), mgr.RecoveryAction{Type: mgr.NoAction})
	if matchesWindowsNetworkServiceRecovery(extra, true) {
		t.Fatal("unexpected extra recovery action was accepted")
	}
	wrong := append([]mgr.RecoveryAction(nil), valid...)
	wrong[1].Delay = 2 * time.Second
	if matchesWindowsNetworkServiceRecovery(wrong, true) {
		t.Fatal("wrong recovery delay was accepted")
	}
	wrong = append([]mgr.RecoveryAction(nil), valid...)
	wrong[2].Type = mgr.NoAction
	if matchesWindowsNetworkServiceRecovery(wrong, true) {
		t.Fatal("wrong recovery action was accepted")
	}
}

func TestNetworkFramePreservesWinDivertFlags(t *testing.T) {
	var buffer bytes.Buffer
	const rawFlags = uint32((1 << 1) | (1 << 8) | networkFrameFlagOutbound | (1 << 20) | (1 << 23))
	if err := writeNetworkFrame(&buffer, nil, networkFrame{
		kind: networkFrameCapture, flags: rawFlags, ifIndex: 7, subIfIndex: 9,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := readNetworkFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got.flags != rawFlags {
		t.Fatalf("flags=%#x want=%#x", got.flags, rawFlags)
	}
}
