package app

import (
	"slices"

	"github.com/apernet/quic-go"
	"net/netip"
	"path/filepath"
	"testing"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
)

func TestFileDirectStatusOnlyPublishesValidPublicWANWithOptIn(t *testing.T) {
	store, err := fileshare.New(filepath.Join(t.TempDir(), "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{fileShareStore: store, approvedCapabilities: []string{protocol.CapabilityFilesHost}}
	if got := a.FileDirectStatus(); got.UPnPEnabled || got.MappingState != "disabled" {
		t.Fatalf("TCP UPnP must be off by default: %+v", got)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	state.DirectUPnPEnabled = true
	if _, err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if got := a.FileDirectStatus(); !got.UPnPEnabled || got.MappingState != "awaiting_request" {
		t.Fatalf("enabled but inactive mapping should be clear: %+v", got)
	}
	runtime := &fileDirectRuntime{
		a: a, upnpState: "mapped", upnpAddress: netip.MustParseAddrPort("100.64.1.9:32450"),
	}
	a.fileDirect = runtime
	if got := a.FileDirectStatus(); got.PublicAddress != "" {
		t.Fatalf("CGNAT address must not be displayed as public: %+v", got)
	}
	runtime.upnpAddress = netip.MustParseAddrPort("8.8.8.8:32450")
	if got := a.FileDirectStatus(); got.PublicAddress != "8.8.8.8:32450" {
		t.Fatalf("mapped public candidate missing: %+v", got)
	}
	next, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	next.DirectUPnPEnabled = false
	if _, err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	a.RefreshFileDirectUPnP()
	if got := a.FileDirectStatus(); got.UPnPEnabled || got.PublicAddress != "" {
		t.Fatalf("disabled mapping must not expose public candidate: %+v", got)
	}
}

func TestFileDirectQUICUPnPCandidateRequiresPublicWANAndActiveLease(t *testing.T) {
	store, err := fileshare.New(filepath.Join(t.TempDir(), "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	state.DirectUPnPEnabled = true
	if _, err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	a := &Agent{fileShareStore: store, approvedCapabilities: []string{protocol.CapabilityFilesHost}}
	cfg, _, err := makeFileDirectTLS()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := quic.ListenAddr("127.0.0.1:0", fileDirectQUICServerTLS(cfg), fileDirectQUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	runtime := &fileDirectRuntime{a: a, quicListener: listener,
		udpUPnPState: "mapped", udpUPnPAddress: netip.MustParseAddrPort("100.64.1.1:51234")}
	a.fileDirect = runtime
	if status := a.FileDirectStatus(); status.QUICPublicAddress != "" {
		t.Fatalf("CGNAT mapping must not be published: %+v", status)
	}
	if got := runtime.quicCandidates(); slices.Contains(got, "100.64.1.1:51234") {
		t.Fatalf("private UDP mapping must not become a QUIC candidate: %v", got)
	}
	runtime.udpUPnPAddress = netip.MustParseAddrPort("8.8.4.4:51234")
	if status := a.FileDirectStatus(); status.QUICPublicAddress != "8.8.4.4:51234" {
		t.Fatalf("missing valid QUIC public candidate: %+v", status)
	}
	if got := runtime.quicCandidates(); !slices.Contains(got, "8.8.4.4:51234") {
		t.Fatalf("missing mapped UDP endpoint: %v", got)
	}
	runtime.udpUPnPAddress = netip.AddrPort{}
	if got := runtime.quicCandidates(); slices.Contains(got, "8.8.4.4:51234") {
		t.Fatalf("expired mapping must be withdrawn: %v", got)
	}
	next, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	next.DirectUPnPEnabled = false
	if _, err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	a.RefreshFileDirectUPnP()
	if status := a.FileDirectStatus(); status.QUICPublicAddress != "" || status.QUICMappingState != "disabled" {
		t.Fatalf("disabling UPnP must hide the QUIC endpoint: %+v", status)
	}
}

func TestFileDirectUPnPToggleDuringLeaseCleanup(t *testing.T) {
	store, err := fileshare.New(filepath.Join(t.TempDir(), "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	state.DirectUPnPEnabled = true
	if _, err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	a := &Agent{fileShareStore: store}
	// Model a mapper whose cancellation was requested but whose UDP/TCP
	// ownership-checked Close has not yet finished.
	runtime := &fileDirectRuntime{a: a, upnpRunning: true, upnpStopping: true}
	runtime.syncUPnP()
	if !runtime.upnpRestart {
		t.Fatal("re-enable during shutdown must schedule a fresh mapping")
	}
	next, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	next.DirectUPnPEnabled = false
	if _, err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	runtime.syncUPnP()
	if runtime.upnpRestart || runtime.upnpState != "disabled" || runtime.udpUPnPState != "disabled" {
		t.Fatalf("final opt-out must withdraw both candidates and cancel restart: %+v", runtime)
	}
}
