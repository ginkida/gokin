package context

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/memory"
)

func TestSessionMemorySwitchRejectsStaleExtractions(t *testing.T) {
	s := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	s.RestoreForSession("session-a", "old snapshot")
	summarizer := &blockedSummarySummarizer{started: make(chan struct{}), release: make(chan struct{}), summary: "obsolete LLM summary"}
	s.SetSummarizer(summarizer)
	s.Extract(longHistory(), 20_000)
	s.Extract(longHistory(), 20_001)
	s.Extract(longHistory(), 20_002)
	select {
	case <-summarizer.started:
	case <-time.After(time.Second):
		t.Fatal("LLM extraction did not start")
	}
	s.mu.RLock()
	queuedGeneration := s.sessionGeneration
	s.mu.RUnlock()
	s.RestoreForSession("session-b", "B's own snapshot")
	close(summarizer.release)
	s.Wait()
	// A heuristic job queued before the switch must be rejected as well.
	s.extractForSession(context.Background(), longHistory(), 20_003, queuedGeneration)
	id, content := s.Snapshot()
	if id != "session-b" || content != "B's own snapshot" {
		t.Fatalf("old extraction overwrote new session: %s %q", id, content)
	}
	s.RestoreForSession("session-c", "")
	if s.ShouldExtract(1) {
		t.Fatal("new session retained old extraction thresholds")
	}
}

func TestSessionPromotionKeepsCommandsNotCompletionClaims(t *testing.T) {
	root := t.TempDir()
	pl, err := memory.NewProjectLearning(root)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	s.SetProjectLearning(pl)
	history := minHistory()
	history = append(history,
		modelMsg("Done. Added // Add adds two ints. in m.go; go build ./... passed."),
		funcCallMsg("bash", map[string]any{"command": "go build ./..."}),
		funcRespMsg("bash", map[string]any{"success": true, "content": "build passed"}),
	)
	s.Extract(history, 20_000)
	prompt := pl.FormatForPrompt()
	if !strings.Contains(prompt, "build_command") || !strings.Contains(prompt, "go build ./...") {
		t.Fatalf("durable command recipe missing: %s", prompt)
	}
	for _, text := range []string{"Added", "passed", "Files changed", "Latest result", "Verification already run", "m.go"} {
		if strings.Contains(prompt, text) {
			t.Errorf("turn-scoped claim promoted as durable project knowledge: %q in %s", text, prompt)
		}
	}
}
