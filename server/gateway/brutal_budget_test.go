package gateway

import (
	"sync"
	"testing"
)

func TestBrutalBandwidthBudgetUnlimitedPreservesLegacyBehavior(t *testing.T) {
	var budget brutalBandwidthBudget
	up, down, release := budget.reserve(5_000_000, 7_000_000, 0, 0)
	if up != 5_000_000 || down != 7_000_000 {
		t.Fatalf("zero aggregate budget changed authenticated rates: %d/%d", up, down)
	}
	release()
	release()
	if budget.clientTx != 0 || budget.serverTx != 0 {
		t.Fatalf("unlimited budget recorded phantom reservations: %+v", budget)
	}
}

func TestBrutalBandwidthBudgetClampAndReclaim(t *testing.T) {
	var budget brutalBandwidthBudget
	const clientLimit, serverLimit = uint64(3_000_000), uint64(5_000_000)
	firstUp, firstDown, releaseFirst := budget.reserve(2_000_000, 3_000_000, clientLimit, serverLimit)
	secondUp, secondDown, releaseSecond := budget.reserve(2_000_000, 3_000_000, clientLimit, serverLimit)
	thirdUp, thirdDown, releaseThird := budget.reserve(2_000_000, 3_000_000, clientLimit, serverLimit)
	if firstUp != 2_000_000 || firstDown != 3_000_000 || secondUp != 1_000_000 ||
		secondDown != 2_000_000 || thirdUp != 0 || thirdDown != 0 {
		t.Fatalf("aggregated brutal grants first=%d/%d second=%d/%d third=%d/%d",
			firstUp, firstDown, secondUp, secondDown, thirdUp, thirdDown)
	}
	releaseFirst()
	releaseFirst() // failed handshakes or double cleanup must not overrelease.
	up, down, releaseFourth := budget.reserve(2_000_000, 3_000_000, clientLimit, serverLimit)
	if up != 2_000_000 || down != 3_000_000 {
		t.Fatalf("budget was not reclaimed: %d/%d", up, down)
	}
	releaseFourth()
	releaseSecond()
	releaseThird()
	if budget.clientTx != 0 || budget.serverTx != 0 {
		t.Fatalf("outstanding reservations after disconnect: client=%d server=%d", budget.clientTx, budget.serverTx)
	}
}

func TestBrutalBandwidthBudgetTinyRemainderFallsBackToBBR(t *testing.T) {
	var budget brutalBandwidthBudget
	const limit = uint64(1_000_000)
	_, firstDown, release := budget.reserve(0, 900_000, 0, limit)
	if firstDown != 900_000 {
		t.Fatalf("first grant=%d", firstDown)
	}
	_, secondDown, secondRelease := budget.reserve(0, 500_000, 0, limit)
	if secondDown != 0 || budget.serverTx != 900_000 {
		t.Fatalf("100 KB/s remainder should keep new sender on BBR; got=%d used=%d", secondDown, budget.serverTx)
	}
	secondRelease()
	release()
}

func TestBrutalBandwidthBudgetConcurrentHandshakeNeverOvercommits(t *testing.T) {
	var budget brutalBandwidthBudget
	const limit uint64 = 8_000_000
	const contenders = 64
	var wg sync.WaitGroup
	var rates [contenders]uint64
	var releases [contenders]func()
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, rates[idx], releases[idx] = budget.reserve(0, 1_000_000, 0, limit)
		}(i)
	}
	wg.Wait()
	var granted uint64
	var nonzero int
	for _, value := range rates {
		granted += value
		if value != 0 {
			nonzero++
		}
	}
	if granted != limit || nonzero != 8 || budget.serverTx != limit {
		t.Fatalf("budget race granted=%d active=%d reserved=%d", granted, nonzero, budget.serverTx)
	}
	for _, release := range releases {
		wg.Add(1)
		go func(cleanup func()) {
			defer wg.Done()
			cleanup()
			cleanup()
		}(release)
	}
	wg.Wait()
	if budget.serverTx != 0 {
		t.Fatalf("bandwidth leaked after concurrent disconnects: %d", budget.serverTx)
	}
}
