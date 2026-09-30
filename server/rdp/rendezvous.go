package rdp

import (
	"context"

	serverp2p "relayproxy/server/p2p"
)

// Rendezvous is retained as an alias for the existing RDP caller. Both RDP and
// proxy direct paths now share the generic P2P reflexive-address observer.
type Rendezvous = serverp2p.Rendezvous

func StartRendezvous(ctx context.Context, address string, maxPerMinute int) (*Rendezvous, error) {
	return serverp2p.StartRendezvous(ctx, address, maxPerMinute)
}
