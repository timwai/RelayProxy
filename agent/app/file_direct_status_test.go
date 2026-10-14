package app

import (
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
