package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestFileP2POnlyRunsForBulkTransfers(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   bool
	}{
		{protocol.FileShareDownload, true},
		{protocol.FileShareUpload, true},
		{protocol.FileShareListDir, false},
		{protocol.FileShareListShares, false},
		{"", false},
	} {
		if got := fileDirectNeedsPunch(tc.action); got != tc.want {
			t.Fatalf("file action %q: punch=%t, want %t", tc.action, got, tc.want)
		}
	}
}

func TestFileMetadataSkipsDirectNegotiation(t *testing.T) {
	for _, tc := range []struct {
		action string
		want bool
	}{
		{protocol.FileShareListDir, false},
		{protocol.FileShareListShares, false},
		{protocol.FileShareDownload, true},
		{protocol.FileShareUpload, true},
		{"unknown", false},
	} {
		if got := fileSharePrefersDirect(tc.action); got != tc.want {
			t.Errorf("action %q direct=%t, want %t", tc.action, got, tc.want)
		}
	}
}
