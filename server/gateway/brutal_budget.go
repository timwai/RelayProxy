package gateway

import "sync"

const minBudgetedBrutalBPS = 125_000 // 1 Mbps; smaller leftover quotas use BBR.

// brutalBandwidthBudget reserves *configured targets*, not actual interface
// capacity or measured throughput. It never shapes BBR, P2P or Public Direct.
// A zero limit preserves legacy unlimited-per-server behavior.
type brutalBandwidthBudget struct {
	mu       sync.Mutex
	clientTx uint64
	serverTx uint64
}

func (b *brutalBandwidthBudget) reserve(clientRequest, serverRequest, clientLimit, serverLimit uint64) (clientRate, serverRate uint64, release func()) {
	b.mu.Lock()
	clientRate = reserveBrutalDirection(clientRequest, clientLimit, &b.clientTx)
	serverRate = reserveBrutalDirection(serverRequest, serverLimit, &b.serverTx)
	b.mu.Unlock()

	var once sync.Once
	return clientRate, serverRate, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if clientLimit > 0 {
				b.clientTx -= clientRate
			}
			if serverLimit > 0 {
				b.serverTx -= serverRate
			}
		})
	}
}

func reserveBrutalDirection(request, limit uint64, used *uint64) uint64 {
	if request == 0 || limit == 0 {
		return request
	}
	if *used >= limit {
		return 0
	}
	available := limit - *used
	if request > available {
		request = available
	}
	if request < minBudgetedBrutalBPS {
		return 0
	}
	*used += request
	return request
}
