package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

type cancelAwareSummarizer struct {
	started  chan struct{}
	canceled chan struct{}
}

func (c *cancelAwareSummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	return "", ctx.Err()
}

type blockedSummarySummarizer struct {
	started chan struct{}
	release chan struct{}
	summary string
}

func (b *blockedSummarySummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	close(b.started)
	select {
	case <-b.release:
		return b.summary, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
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
	mgr.Wait()

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

func TestSessionMemoryManager_CloseCancelsInFlightLLMExtraction(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	summarizer := &cancelAwareSummarizer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	mgr.SetSummarizer(summarizer)

	history := longHistory()
	mgr.Extract(history, 20000)
	mgr.Extract(history, 20001)
	mgr.Extract(history, 20002)

	select {
	case <-summarizer.started:
	case <-time.After(time.Second):
		t.Fatal("LLM extraction did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-summarizer.canceled:
	default:
		t.Fatal("Close returned before canceling the in-flight summarizer")
	}
}

func TestSessionMemoryManager_StaleLLMSuccessDoesNotOverwriteLatestExtraction(t *testing.T) {
	workDir := t.TempDir()
	mgr := NewSessionMemoryManager(workDir, DefaultSessionMemoryConfig())
	summarizer := &blockedSummarySummarizer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		summary: "obsolete LLM summary",
	}
	mgr.SetSummarizer(summarizer)

	oldHistory := append(longHistory(), userMsg("Investigate the old authentication task only"))
	mgr.Extract(oldHistory, 20_000)
	mgr.Extract(oldHistory, 20_001)
	mgr.Extract(oldHistory, 20_002)
	select {
	case <-summarizer.started:
	case <-time.After(time.Second):
		t.Fatal("LLM extraction did not start")
	}

	latestTask := "Preserve this newest heuristic session memory after async completion"
	var updates int32
	mgr.SetOnUpdate(func() { atomic.AddInt32(&updates, 1) })
	mgr.Extract(append(longHistory(), userMsg(latestTask)), 20_003)
	updatesBeforeLLM := atomic.LoadInt32(&updates)
	close(summarizer.release)
	mgr.Wait()

	if got := mgr.GetContent(); !strings.Contains(got, latestTask) || strings.Contains(got, summarizer.summary) {
		t.Fatalf("stale successful LLM overwrote latest extraction:\n%s", got)
	}
	if got := atomic.LoadInt32(&updates); got != updatesBeforeLLM {
		t.Fatalf("discarded stale LLM success fired onUpdate: before=%d after=%d", updatesBeforeLLM, got)
	}
	disk, err := os.ReadFile(filepath.Join(workDir, ".gokin", ".session-memory.md"))
	if err != nil {
		t.Fatalf("read persisted session memory: %v", err)
	}
	if got := string(disk); !strings.Contains(got, latestTask) || strings.Contains(got, summarizer.summary) {
		t.Fatalf("stale successful LLM reached disk:\n%s", got)
	}
}

func TestSessionMemoryManager_CloseDoesNotLetStaleLLMFallbackOverwriteLatestExtraction(t *testing.T) {
	workDir := t.TempDir()
	mgr := NewSessionMemoryManager(workDir, DefaultSessionMemoryConfig())
	summarizer := &cancelAwareSummarizer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	mgr.SetSummarizer(summarizer)

	oldTask := "Investigate the legacy authentication path and do not touch the new task"
	latestTask := "Implement the latest shutdown-safe session memory lifecycle regression"
	withTask := func(task string) []*genai.Content {
		history := longHistory()
		return append(history, userMsg(task), modelMsg("Acknowledged; working on that task now."))
	}

	oldHistory := withTask(oldTask)
	mgr.Extract(oldHistory, 20_000)
	mgr.Extract(oldHistory, 20_001)
	mgr.Extract(oldHistory, 20_002) // every third extraction starts the blocked LLM call

	select {
	case <-summarizer.started:
	case <-time.After(time.Second):
		t.Fatal("LLM extraction did not start")
	}

	var updates int32
	mgr.SetOnUpdate(func() { atomic.AddInt32(&updates, 1) })
	mgr.Extract(withTask(latestTask), 20_003) // newer heuristic while the old LLM is blocked
	if got := mgr.GetContent(); !strings.Contains(got, latestTask) {
		t.Fatalf("newer heuristic extraction did not commit before shutdown:\n%s", got)
	}
	updatesBeforeClose := atomic.LoadInt32(&updates)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := mgr.GetContent(); !strings.Contains(got, latestTask) || strings.Contains(got, oldTask) {
		t.Fatalf("stale canceled LLM fallback overwrote the latest extraction:\n%s", got)
	}
	if got := atomic.LoadInt32(&updates); got != updatesBeforeClose {
		t.Fatalf("discarded stale LLM result fired onUpdate: before=%d after=%d", updatesBeforeClose, got)
	}
	disk, err := os.ReadFile(filepath.Join(workDir, ".gokin", ".session-memory.md"))
	if err != nil {
		t.Fatalf("read persisted session memory: %v", err)
	}
	if got := string(disk); !strings.Contains(got, latestTask) || strings.Contains(got, oldTask) {
		t.Fatalf("shutdown persisted stale session memory:\n%s", got)
	}
}

// errorSummarizer always fails — used to exercise the extraction-failed path.
type errorSummarizer struct{ err error }

func (e *errorSummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	return "", e.err
}

// TestSessionMemoryManager_ExtractionFailedCallback pins the fix for
// invisible quota-burning background LLM calls (round-14 #7): a failed
// LLM-backed extraction (a real ~30s API call on a quota-limited provider)
// was Debug-log only. SetOnExtractionFailed must fire on a genuine failure.
func TestSessionMemoryManager_ExtractionFailedCallback(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	mgr.SetSummarizer(&errorSummarizer{err: context.DeadlineExceeded})

	var failedErr error
	failed := false
	mgr.SetOnExtractionFailed(func(err error) {
		failed = true
		failedErr = err
	})

	history := longHistory()
	// extractionCount%3==0 triggers the LLM path — call 3 times to reach it.
	mgr.Extract(history, 1000)
	mgr.Extract(history, 1000)
	mgr.Extract(history, 1000)
	mgr.Wait()

	if !failed {
		t.Fatal("a genuine Summarize error must fire SetOnExtractionFailed")
	}
	if failedErr == nil {
		t.Fatal("SetOnExtractionFailed fired with a nil error")
	}
}

// TestSessionMemoryManager_ExtractionEmptyNoErrorDoesNotNotify guards against
// a false alarm: an empty summary with NO error is a legitimate "nothing to
// add" response, not a wasted/failed call.
func TestSessionMemoryManager_ExtractionEmptyNoErrorDoesNotNotify(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	mgr.SetSummarizer(&errorSummarizer{err: nil}) // empty string, nil error

	failed := false
	mgr.SetOnExtractionFailed(func(error) { failed = true })

	history := longHistory()
	mgr.Extract(history, 1000)
	mgr.Extract(history, 1000)
	mgr.Extract(history, 1000)
	mgr.Wait()

	if failed {
		t.Fatal("an empty summary with no error must NOT fire SetOnExtractionFailed")
	}
}
