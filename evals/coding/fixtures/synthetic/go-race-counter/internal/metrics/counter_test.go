package metrics

import (
	"sync"
	"testing"
)

func TestCounterConcurrentInc(t *testing.T) {
	c := NewCounter()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Inc("requests")
			}
		}()
	}
	wg.Wait()
	if got := c.Get("requests"); got != 1000 {
		t.Fatalf("Get(requests) = %d, want 1000", got)
	}
}
