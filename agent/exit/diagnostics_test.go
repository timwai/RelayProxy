package exit

import (
	"testing"

	"relayproxy/internal/tunnel"
)

func TestDiagnosticsReturnsIndependentSortedSnapshot(t *testing.T) {
	h := NewHandler(HandlerConfig{})
	first := h.beginTCPDiagnostic("first", 8443, "192.0.2.1:8443", &tunnel.PipeMetrics{})
	second := h.beginTCPDiagnostic("second", 443, "192.0.2.2:443", &tunnel.PipeMetrics{})
	snapshot := h.Diagnostics()
	if len(snapshot.ActiveTCP) != 2 || snapshot.ActiveTCP[0].ID != first || snapshot.ActiveTCP[1].ID != second {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	h.endTCPDiagnostic(first)
	if len(snapshot.ActiveTCP) != 2 || len(h.Diagnostics().ActiveTCP) != 1 {
		t.Fatal("snapshot aliases live registry")
	}
}
