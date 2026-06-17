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
