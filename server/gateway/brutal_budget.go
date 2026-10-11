package gateway

import "sync"

const minBudgetedBrutalBPS = 125_000 // 1 Mbps; smaller leftover quotas use BBR.

// brutalBandwidthBudget reserves conservative pacing capacity for configured
// Brutal targets. Loss compensation can increase pacing to 125% of the target
// (ackRate floor 0.8). UDP/IP overhead and independent BBR traffic are NOT
// accounted for. Zero budget preserves the original unlimited behavior.
type brutalBandwidthBudget struct {
	mu       sync.Mutex
	clientTx uint64 // charged B/s, including compensation headroom
	serverTx uint64
}

func (b *brutalBandwidthBudget) reserve(clientRequest, serverRequest, clientLimit, serverLimit uint64) (clientRate, serverRate uint64, release func()) {
	b.mu.Lock()
	var clientCharge, serverCharge uint64
	clientRate, clientCharge = reserveBrutalDirection(clientRequest, clientLimit, &b.clientTx)
	serverRate, serverCharge = reserveBrutalDirection(serverRequest, serverLimit, &b.serverTx)
	b.mu.Unlock()

	var once sync.Once
	return clientRate, serverRate, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.clientTx -= clientCharge
			b.serverTx -= serverCharge
		})
	}
}

// reserveBrutalDirection grants a target only if its worst-case 1.25x
// loss-compensated pacer fits in the configured aggregate reservation pool.
// The 4/5 computation avoids multiplication overflow on untrusted values.
func reserveBrutalDirection(request, limit uint64, used *uint64) (granted, charge uint64) {
	if request == 0 || limit == 0 {
		return request, 0
	}
	if *used >= limit {
		return 0, 0
	}
	available := limit - *used
	maxTarget := available/5*4 + available%5*4/5
	if request > maxTarget {
		request = maxTarget
	}
	if request < minBudgetedBrutalBPS {
		return 0, 0
	}
	charge = request + request/4
	if request%4 != 0 {
		charge++
	}
	*used += charge
	return request, charge
}
