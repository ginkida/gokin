package context

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/chat"

	"google.golang.org/genai"
)

// helper: build a ContextManager with a pre-seeded token usage so
// ContextAgent.CheckAndCompact can make decisions without a real client.
func newTestContextAgentManager(t *testing.T, currentTokens, maxTokens int) *ContextManager {
	t.Helper()
	m := &ContextManager{
		session:       chat.NewSession(),
		tokenCounter:  &TokenCounter{limits: TokenLimits{MaxInputTokens: maxTokens}},
		messageScorer: NewMessageScorer(),
		keyFiles:      map[string]bool{},
	}
	m.mu.Lock()
	m.currentTokens = currentTokens
	m.lastUsage = &TokenUsage{
		InputTokens: currentTokens,
		MaxTokens:   maxTokens,
	}
	m.mu.Unlock()
	return m
}

// ========== NewContextAgent ==========

func TestNewContextAgent(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	s := chat.NewSession()
	a := NewContextAgent(m, s, t.TempDir())

	if a == nil {
		t.Fatal("NewContextAgent returned nil")
	}
	if a.manager == nil {
		t.Error("manager should not be nil")
	}
	if a.session == nil {
		t.Error("session should not be nil")
	}
	if a.compactionThreshold != 0.75 {
		t.Errorf("compactionThreshold = %v, want 0.75", a.compactionThreshold)
	}
	if a.checkpointInterval != 10*time.Minute {
		t.Errorf("checkpointInterval = %v, want 10m", a.checkpointInterval)
	}
	if a.stopChan == nil {
		t.Error("stopChan should not be nil")
	}
}

// ========== Stop ==========

func TestContextAgent_Stop(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	// Stop should be idempotent — calling twice must not panic.
	a.Stop()
	a.Stop()
}

// ========== Stop terminates Start ==========

func TestContextAgent_StartStopTerminates(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())
	a.checkpointInterval = 1 * time.Millisecond // speed up

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		a.Start(ctx)
		close(done)
	}()

	// Stop via stopChan — Start should return.
	a.Stop()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not terminate after Stop")
	}
}

func TestContextAgent_StartCtxCancelTerminates(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		a.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not terminate after ctx cancel")
	}
}

// ========== CheckAndCompact — nil usage ==========

func TestContextAgent_CheckAndCompact_NilUsage(t *testing.T) {
	m := &ContextManager{
		session:       chat.NewSession(),
		tokenCounter:  &TokenCounter{},
		messageScorer: NewMessageScorer(),
		keyFiles:      map[string]bool{},
	}
	// lastUsage is nil, MaxTokens=0 → should return immediately (no panic)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())
	a.CheckAndCompact(context.Background())
}

func TestContextAgent_CheckAndCompact_ZeroMaxTokens(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 0)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())
	// MaxTokens=0 → should return immediately
	a.CheckAndCompact(context.Background())
}

// ========== CheckAndCompact — below threshold (no-op) ==========

func TestContextAgent_CheckAndCompact_BelowThreshold(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 10000) // 1% — well below 75%
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())
	a.CheckAndCompact(context.Background())
	// No panic, no compaction triggered — just returns.
}

// ========== CheckAndCompact — growth rate EMA (no panic on idle) ==========

func TestContextAgent_CheckAndCompact_IdleNoGrowthRateDecay(t *testing.T) {
	m := newTestContextAgentManager(t, 500, 10000) // 5%
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	// First call — sets prevTokens
	a.CheckAndCompact(context.Background())

	// Second call with same tokens — idle, should not update EMA
	a.CheckAndCompact(context.Background())

	a.mu.Lock()
	idle := a.prevTokens == 500
	a.mu.Unlock()
	if !idle {
		t.Error("idle session should keep prevTokens unchanged")
	}
}

func TestContextAgent_CheckAndCompact_GrowthRateUpdates(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 100000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	// First call
	a.CheckAndCompact(context.Background())

	// Simulate token growth
	m.mu.Lock()
	m.currentTokens = 200
	m.lastUsage.InputTokens = 200
	m.mu.Unlock()

	// Second call — should compute growth rate
	a.CheckAndCompact(context.Background())

	a.mu.Lock()
	gr := a.growthRate
	a.mu.Unlock()
	if gr <= 0 {
		t.Error("growthRate should be > 0 after token growth")
	}
}

// ========== Checkpoint — empty history ==========

func TestContextAgent_Checkpoint_EmptyHistory(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	a.Checkpoint(context.Background())

	// No files should be created
	dir := filepath.Join(a.storageDir, "checkpoints")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected 0 checkpoints for empty history, got %d", len(entries))
	}
}

// ========== Checkpoint — with history ==========

func TestContextAgent_Checkpoint_WithHistory(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	s := chat.NewSession()
	s.SetHistory([]*genai.Content{
		genai.NewContentFromText("hello", genai.RoleUser),
		genai.NewContentFromText("world", genai.RoleModel),
	})
	a := NewContextAgent(m, s, t.TempDir())

	a.Checkpoint(context.Background())

	dir := filepath.Join(a.storageDir, "checkpoints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read checkpoint dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 checkpoint, got %d", len(entries))
	}
}

// ========== Checkpoint — creates directory ==========

func TestContextAgent_Checkpoint_CreatesDir(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	s := chat.NewSession()
	s.SetHistory([]*genai.Content{
		genai.NewContentFromText("test", genai.RoleUser),
	})
	storageDir := filepath.Join(t.TempDir(), "nested", "deep")
	a := NewContextAgent(m, s, storageDir)

	a.Checkpoint(context.Background())

	dir := filepath.Join(storageDir, "checkpoints")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("checkpoint directory was not created")
	}
}

// ========== rotateCheckpoints ==========

func TestContextAgent_RotateCheckpoints_NoFiles(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	// Empty dir — should not panic
	a.rotateCheckpoints(t.TempDir(), 5)
}

func TestContextAgent_RotateCheckpoints_BelowMax(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	dir := t.TempDir()
	// Create 3 files — below max of 5
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, "cp_2024010"+string(rune('1'+i))+"_120000.json")
		os.WriteFile(path, []byte("{}"), 0644)
		time.Sleep(10 * time.Millisecond)
	}

	a.rotateCheckpoints(dir, 5)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("expected 3 files (below max), got %d", len(entries))
	}
}

func TestContextAgent_RotateCheckpoints_DeletesOldest(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	dir := t.TempDir()
	maxKeep := 3
	// Create 5 files with distinct timestamps
	var files []string
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "cp_2024010"+string(rune('1'+i))+"_120000.json")
		os.WriteFile(path, []byte("{}"), 0644)
		files = append(files, path)
		time.Sleep(15 * time.Millisecond)
	}

	a.rotateCheckpoints(dir, maxKeep)

	entries, _ := os.ReadDir(dir)
	if len(entries) != maxKeep {
		t.Errorf("expected %d files after rotation, got %d", maxKeep, len(entries))
	}
}

func TestContextAgent_RotateCheckpoints_IgnoresNonJSON(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	a := NewContextAgent(m, chat.NewSession(), t.TempDir())

	dir := t.TempDir()
	// Create JSON + non-JSON files
	os.WriteFile(filepath.Join(dir, "cp_001.json"), []byte("{}"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644)
	os.WriteFile(filepath.Join(dir, "cp_002.json"), []byte("{}"), 0644)

	// max=1 — should keep 1 JSON, ignore txt
	a.rotateCheckpoints(dir, 1)

	entries, _ := os.ReadDir(dir)
	jsonCount := 0
	txtCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
		if filepath.Ext(e.Name()) == ".txt" {
			txtCount++
		}
	}
	if jsonCount != 1 {
		t.Errorf("expected 1 JSON file, got %d", jsonCount)
	}
	if txtCount != 1 {
		t.Errorf("txt file should be preserved (not JSON), got %d", txtCount)
	}
}

// ========== Checkpoint + rotate integration ==========

func TestContextAgent_Checkpoint_RotatesAfterMax(t *testing.T) {
	m := newTestContextAgentManager(t, 100, 1000)
	s := chat.NewSession()
	s.SetHistory([]*genai.Content{
		genai.NewContentFromText("history", genai.RoleUser),
	})
	a := NewContextAgent(m, s, t.TempDir())

	// Create 5 checkpoints (the max in Checkpoint)
	dir := filepath.Join(a.storageDir, "checkpoints")
	os.MkdirAll(dir, 0755)
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "cp_2024010"+string(rune('1'+i))+"_120000.json")
		os.WriteFile(path, []byte("{}"), 0644)
		time.Sleep(20 * time.Millisecond)
	}

	// Now call Checkpoint — it creates a 6th and should rotate to 5
	a.Checkpoint(context.Background())

	entries, _ := os.ReadDir(dir)
	jsonCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 5 {
		t.Errorf("expected 5 JSON files after rotation, got %d", jsonCount)
	}
}
