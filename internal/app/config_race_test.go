package app

import (
	"sync"
	"testing"

	"gokin/internal/config"
)

// TestConfigReaders_NoRaceWithApplyConfigSwap pins the two confirmed data races
// (correctness audit): GetUIRuntimeStatus runs on the Bubble Tea status
// goroutine and sendTokenUsageUpdate on a background /loop callback, both reading
// a.config while ApplyConfig swaps it under a.mu. With the locked snapshots the
// readers no longer race the swap; under `go test -race` this fails loudly if a
// future edit drops the lock.
func TestConfigReaders_NoRaceWithApplyConfigSwap(t *testing.T) {
	a := &App{config: config.DefaultConfig()}
	c1 := config.DefaultConfig()
	c2 := config.DefaultConfig()

	var wg sync.WaitGroup
	wg.Add(2)
	// Reader #1: the ~1/sec status goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = a.GetUIRuntimeStatus()
		}
	}()
	// Reader #2: the background /loop token-usage callback (nil contextManager →
	// returns after the locked a.config snapshot, which is the racing read).
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			a.sendTokenUsageUpdate()
		}
	}()

	// Writer: ApplyConfig swaps a.config under a.mu.
	for i := 0; i < 1000; i++ {
		a.mu.Lock()
		if i%2 == 0 {
			a.config = c1
		} else {
			a.config = c2
		}
		a.mu.Unlock()
	}
	wg.Wait()
}

// TestClientSnapshot_NoRaceWithClientSwap pins finding #4/#10: a.client is read
// lock-free from background goroutines (pushTurnContext via session-memory
// onUpdate, the /loop spawner) while ApplyConfig/failover swap it under a.mu. The
// leaf-lock accessors (clientSnapshot reads under clientMu; setClientLocked
// writes under clientMu with a.mu held) remove the race. Under `go test -race`
// this fails loudly if a future edit drops clientMu.
func TestClientSnapshot_NoRaceWithClientSwap(t *testing.T) {
	a := &App{}

	var wg sync.WaitGroup
	wg.Add(2)
	// Reader: a background goroutine (e.g. pushTurnContext / loop spawner).
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = a.clientSnapshot()
		}
	}()
	// Writer: ApplyConfig/failover swap a.client. They hold a.mu; mirror that.
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			a.mu.Lock()
			a.setClientLocked(nil)
			a.mu.Unlock()
		}
	}()
	wg.Wait()
}
