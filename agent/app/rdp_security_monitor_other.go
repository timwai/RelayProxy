//go:build !windows

package app

import (
	"context"

	"relayproxy/internal/protocol"
)

func monitorWindowsRDPAuthFailures(ctx context.Context, report func(context.Context, protocol.RDPHostAuthFailure) error) {
	// Windows Security event log is not available on other platforms.
}
