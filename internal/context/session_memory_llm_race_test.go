package context

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

// countingSummarizer tracks concurrent Summarize invocations, blocking each
// call briefly so overlapping calls (if the guard is missing) have a real
// window to actually overlap rather than racing to finish instantly.
type countingSummarizer struct {
	mu        sync.Mutex
	current   int32
	maxConcur int32
	calls     int32
}

func (c *countingSummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	n := atomic.AddInt32(&c.current, 1)
	atomic.AddInt32(&c.calls, 1)
	c.mu.Lock()
	if n > c.maxConcur {
		c.maxConcur = n
	}
	c.mu.Unlock()

	time.Sleep(80 * time.Millisecond) // hold the "in flight" window open

	atomic.AddInt32(&c.current, -1)
	return "LLM summary", nil
}

func longHistory() []*genai.Content {
	h := minHistory()
	for i := 0; i < 8; i++ {
		h = append(h, userMsg("continuing the task"), modelMsg("working on it"))
	}
	return h
}

// TestSessionMemoryManager_ExtractWithLLM_NoOverlap (round 5) pins the fix:
// Extract() used to launch `go s.extractWithLLM(...)` on every qualifying
// (every-3rd) extraction with NO tracking of whether a prior such goroutine
// was still running. A burst of qualifying extractions within the ~30s LLM
// call window (token/tool-call thresholds firing in quick succession, or
// concurrent sub-agent tool activity — CLAUDE.md notes sub-agent tool
// activity counts toward the extraction thresholds too) could launch TWO
// overlapping extractWithLLM calls racing to set s.content — network latency,
// not recency, decided which summary stuck, plus an extra unbudgeted LLM
// call against a quota-limited provider. Fixed with an in-flight guard.
func TestSessionMemoryManager_ExtractWithLLM_NoOverlap(t *testing.T) {
	workDir := t.TempDir()
	cfg := DefaultSessionMemoryConfig()
	mgr := NewSessionMemoryManager(workDir, cfg)
	summarizer := &countingSummarizer{}
	mgr.SetSummarizer(summarizer)

	history := longHistory()

	// Fire many concurrent Extract() calls — s.extractionCount increments
	// under lock across all of them, so roughly 1-in-3 will qualify for LLM
	// extraction (extractionCount%3==0 && len(history)>=10). Without the
	// in-flight guard, several of those could launch extractWithLLM
	// concurrently within the same 80ms window the fake summarizer holds
	// open.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.Extract(history, 20000)
		}()
	}
	wg.Wait()

	// Give any launched extractWithLLM goroutines time to finish (each holds
	// its "in flight" window for 80ms via the fake summarizer).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&summarizer.current) > 0 {
		time.Sleep(5 * time.Millisecond)
	}

	if calls := atomic.LoadInt32(&summarizer.calls); calls == 0 {
		t.Fatal("test setup invalid: no qualifying (every-3rd) extraction ever fired an LLM call")
	}

	summarizer.mu.Lock()
	maxConcur := summarizer.maxConcur
	summarizer.mu.Unlock()
	if maxConcur > 1 {
		t.Fatalf("max concurrent Summarize() calls = %d, want <= 1 — overlapping LLM extractions raced to set s.content", maxConcur)
	}
}
