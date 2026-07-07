package client

import (
	"sync"
	"testing"
	"time"
)

func TestCacheTracker_FirstCallNeverBreaks(t *testing.T) {
	ct := NewCacheTracker()
	bt := ct.RecordState("system prompt", "tools json")
	if bt != CacheBreakNone {
		t.Errorf("first call should not detect break, got %q", bt)
	}
}

func TestCacheTracker_SameDataNoBreak(t *testing.T) {
	ct := NewCacheTracker()
	ct.RecordState("prompt", "tools")
	bt := ct.RecordState("prompt", "tools")
	if bt != CacheBreakNone {
		t.Errorf("same data should not break, got %q", bt)
	}
}

func TestCacheTracker_SystemPromptChanged(t *testing.T) {
	ct := NewCacheTracker()
	ct.RecordState("prompt v1", "tools")
	bt := ct.RecordState("prompt v2", "tools")
	if bt != CacheBreakSystemPrompt {
		t.Errorf("expected system_prompt_changed, got %q", bt)
	}
}

func TestCacheTracker_ToolsChanged(t *testing.T) {
	ct := NewCacheTracker()
	ct.RecordState("prompt", "tools v1")
	bt := ct.RecordState("prompt", "tools v2")
	if bt != CacheBreakToolsChanged {
		t.Errorf("expected tools_changed, got %q", bt)
	}
}

func TestCacheTracker_BreakCounter(t *testing.T) {
	ct := NewCacheTracker()
	ct.RecordState("a", "b")
	ct.RecordState("c", "b") // break 1
	ct.RecordState("c", "d") // break 2
	ct.RecordState("c", "d") // no break

	stats := ct.GetStats()
	if stats.CacheBreaks != 2 {
		t.Errorf("expected 2 breaks, got %d", stats.CacheBreaks)
	}
}

func TestCacheTracker_RecordUsage(t *testing.T) {
	ct := NewCacheTracker()
	ct.RecordUsage(1000, 3000)
	ct.RecordUsage(500, 2000)

	stats := ct.GetStats()
	if stats.TotalCreationTokens != 1500 {
		t.Errorf("creation: got %d, want 1500", stats.TotalCreationTokens)
	}
	if stats.TotalReadTokens != 5000 {
		t.Errorf("read: got %d, want 5000", stats.TotalReadTokens)
	}
}

func TestCacheTracker_Efficiency(t *testing.T) {
	ct := NewCacheTracker()
	if ct.CacheEfficiency() != 0 {
		t.Error("empty tracker should have 0 efficiency")
	}
	ct.RecordUsage(1000, 3000)
	eff := ct.CacheEfficiency()
	if eff < 0.74 || eff > 0.76 {
		t.Errorf("expected ~0.75, got %f", eff)
	}
}

func TestCacheTracker_GetStats(t *testing.T) {
	ct := NewCacheTracker()
	ct.RecordState("a", "b")
	ct.RecordState("c", "b")
	ct.RecordUsage(100, 200)

	stats := ct.GetStats()
	if stats.CacheBreaks != 1 {
		t.Errorf("breaks: %d", stats.CacheBreaks)
	}
	if stats.LastBreakReason != string(CacheBreakSystemPrompt) {
		t.Errorf("reason: %q", stats.LastBreakReason)
	}
	if stats.TotalCreationTokens != 100 || stats.TotalReadTokens != 200 {
		t.Errorf("tokens: c=%d r=%d", stats.TotalCreationTokens, stats.TotalReadTokens)
	}
}

func TestCacheTracker_Concurrent(t *testing.T) {
	ct := NewCacheTracker()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			ct.RecordState("p"+string(rune('a'+n%5)), "t")
		}(i)
		go func(n int) {
			defer wg.Done()
			ct.RecordUsage(n*10, n*20)
		}(i)
	}
	wg.Wait()
	_ = ct.GetStats()
}

// TestCacheTracker_GetStatsConcurrentWithWriter (round 6) pins the fix for a
// recursive-RLock self-deadlock: GetStats used to call the public
// CacheEfficiency() while ALREADY holding t.mu.RLock() — sync.RWMutex.RLock
// is not reentrant once a writer is queued (Go blocks new readers behind a
// pending writer to prevent starvation), so a RecordUsage (writer) call
// arriving between GetStats's outer and inner RLock could deadlock BOTH
// goroutines forever. Unlike TestCacheTracker_Concurrent (which only calls
// GetStats once, AFTER all writers finish), this calls GetStats and
// RecordUsage CONCURRENTLY in a tight loop to give the pathological
// interleaving a real chance to occur, wrapped in an explicit timeout so a
// reintroduced deadlock fails the test instead of hanging the suite.
func TestCacheTracker_GetStatsConcurrentWithWriter(t *testing.T) {
	ct := NewCacheTracker()
	done := make(chan struct{})

	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 500; i++ {
			wg.Add(2)
			go func(n int) {
				defer wg.Done()
				ct.RecordUsage(n, n*2)
			}(i)
			go func() {
				defer wg.Done()
				_ = ct.GetStats()
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("GetStats()/RecordUsage() concurrent load deadlocked — recursive RLock reintroduced?")
	}
}
