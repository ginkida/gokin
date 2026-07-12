package memory

import (
	"os"
	"strings"
	"testing"
	"time"
)

// newTestExampleStore creates an ExampleStore in a temp dir with Flush on cleanup.
func newTestExampleStore(t *testing.T) *ExampleStore {
	t.Helper()
	es, err := NewExampleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewExampleStore: %v", err)
	}
	t.Cleanup(func() { _ = es.Flush() })
	return es
}

// --- LearnFromSuccess / LearnFromSuccessWithTools ---

func TestExampleStore_LearnFromSuccess(t *testing.T) {
	es := newTestExampleStore(t)
	err := es.LearnFromSuccess("implement", "add a new endpoint", "general", "done", 5*time.Second, 500)
	if err != nil {
		t.Fatalf("LearnFromSuccess: %v", err)
	}
	stats := es.GetStats()
	if stats.TotalExamples != 1 {
		t.Errorf("TotalExamples = %d, want 1", stats.TotalExamples)
	}
}

func TestExampleStore_LearnFromSuccessWithTools(t *testing.T) {
	es := newTestExampleStore(t)
	toolSeq := []ToolCallExample{
		{ToolName: "read", Args: map[string]any{"file_path": "main.go"}, Success: true, Output: "ok"},
		{ToolName: "edit", Args: map[string]any{"file_path": "main.go"}, Success: true, Output: "ok"},
		{ToolName: "read", Args: map[string]any{"file_path": "main.go"}, Success: true, Output: "ok"}, // duplicate tool
	}
	err := es.LearnFromSuccessWithTools("refactor", "refactor the auth module", "explore", "success", toolSeq, 10*time.Second, 1000)
	if err != nil {
		t.Fatalf("LearnFromSuccessWithTools: %v", err)
	}
	stats := es.GetStats()
	if stats.TotalExamples != 1 {
		t.Fatalf("TotalExamples = %d, want 1", stats.TotalExamples)
	}
}

func TestExampleStore_LearnFromSuccess_LongOutputTruncated(t *testing.T) {
	es := newTestExampleStore(t)
	longOutput := strings.Repeat("a", 3000)
	err := es.LearnFromSuccess("implement", "build something", "general", longOutput, 1*time.Second, 100)
	if err != nil {
		t.Fatalf("LearnFromSuccess: %v", err)
	}
}

// --- GetSimilarExamples ---

func TestExampleStore_GetSimilarExamples_FindsMatch(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add authentication endpoint", "general", "done", 1*time.Second, 100)

	results := es.GetSimilarExamples("authentication endpoint", 5)
	if len(results) == 0 {
		t.Fatal("expected at least one similar example")
	}
	if results[0].TaskType != "implement" {
		t.Errorf("TaskType = %q, want 'implement'", results[0].TaskType)
	}
}

func TestExampleStore_GetSimilarExamples_NoMatch(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add authentication", "general", "done", 1*time.Second, 100)

	results := es.GetSimilarExamples("completely different topic", 5)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestExampleStore_GetSimilarExamples_NegativeLimit(t *testing.T) {
	es := newTestExampleStore(t)
	results := es.GetSimilarExamples("anything", -1)
	if results != nil {
		t.Errorf("negative limit should return nil, got %v", results)
	}
}

func TestExampleStore_GetSimilarExamples_ZeroLimit(t *testing.T) {
	es := newTestExampleStore(t)
	results := es.GetSimilarExamples("anything", 0)
	if results != nil {
		t.Errorf("zero limit should return nil, got %v", results)
	}
}

func TestExampleStore_GetSimilarExamples_NoTagsFromPrompt(t *testing.T) {
	es := newTestExampleStore(t)
	// Prompt with only stop words → no tags extracted
	results := es.GetSimilarExamples("the a an is", 5)
	if results != nil {
		t.Errorf("prompt with only stop words should return nil, got %v", results)
	}
}

// --- GetExamplesForContext ---

func TestExampleStore_GetExamplesForContext_ReturnsFormattedString(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add user registration", "general", "created user model", 1*time.Second, 100)

	result := es.GetExamplesForContext("implement", "user registration", 5)
	if result == "" {
		t.Fatal("expected non-empty context string")
	}
	if !strings.Contains(result, "Similar Past Tasks") {
		t.Error("context should contain 'Similar Past Tasks' header")
	}
	if !strings.Contains(result, "user registration") {
		t.Error("context should contain the prompt")
	}
}

func TestExampleStore_GetExamplesForContext_NoMatches(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add auth", "general", "done", 1*time.Second, 100)

	result := es.GetExamplesForContext("implement", "completely unrelated topic", 5)
	if result != "" {
		t.Errorf("expected empty string for no matches, got %q", result)
	}
}

// --- RecordFeedback ---

func TestExampleStore_RecordFeedback_Positive(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add feature", "general", "done", 1*time.Second, 100)

	var id string
	for exID := range es.examples {
		id = exID
		break
	}
	if id == "" {
		t.Fatal("no example ID found")
	}

	es.RecordFeedback(id, true)

	es.mu.RLock()
	score := es.examples[id].SuccessScore
	es.mu.RUnlock()
	// Score starts at 1.0; positive feedback keeps it at 1.0: 1.0 + (1.0-1.0)*0.1 = 1.0
	if score != 1.0 {
		t.Errorf("positive feedback on perfect score should keep it at 1.0, got %f", score)
	}
}

func TestExampleStore_RecordFeedback_Negative(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add feature", "general", "done", 1*time.Second, 100)

	var id string
	for exID := range es.examples {
		id = exID
		break
	}

	es.RecordFeedback(id, false)

	es.mu.RLock()
	score := es.examples[id].SuccessScore
	es.mu.RUnlock()
	if score >= 1.0 {
		t.Errorf("negative feedback should lower score below 1.0, got %f", score)
	}
}

func TestExampleStore_RecordFeedback_NotFound(t *testing.T) {
	es := newTestExampleStore(t)
	before := es.GetStats().TotalExamples
	es.RecordFeedback("nonexistent", true)
	after := es.GetStats().TotalExamples
	if after != before {
		t.Errorf("RecordFeedback on nonexistent ID changed TotalExamples: before=%d after=%d", before, after)
	}
}

// --- GetStats ---

func TestExampleStore_GetStats_Empty(t *testing.T) {
	es := newTestExampleStore(t)
	stats := es.GetStats()
	if stats.TotalExamples != 0 {
		t.Errorf("TotalExamples = %d, want 0", stats.TotalExamples)
	}
}

func TestExampleStore_GetStats_WithExamples(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add feature a", "general", "done", 1*time.Second, 100)
	es.LearnFromSuccess("refactor", "refactor module b", "explore", "done", 2*time.Second, 200)

	stats := es.GetStats()
	if stats.TotalExamples != 2 {
		t.Errorf("TotalExamples = %d, want 2", stats.TotalExamples)
	}
	if stats.ByType["implement"] != 1 {
		t.Errorf("ByType['implement'] = %d, want 1", stats.ByType["implement"])
	}
	if stats.ByType["refactor"] != 1 {
		t.Errorf("ByType['refactor'] = %d, want 1", stats.ByType["refactor"])
	}
	if stats.AvgSuccessScore <= 0 {
		t.Errorf("AvgSuccessScore = %f, want > 0", stats.AvgSuccessScore)
	}
}

// --- Clear ---

func TestExampleStore_Clear(t *testing.T) {
	es := newTestExampleStore(t)
	es.LearnFromSuccess("implement", "add feature", "general", "done", 1*time.Second, 100)

	if err := es.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	stats := es.GetStats()
	if stats.TotalExamples != 0 {
		t.Errorf("TotalExamples after Clear = %d, want 0", stats.TotalExamples)
	}
}

// --- truncateString ---

func TestTruncateString_ShortString(t *testing.T) {
	result := truncateString("short", 100)
	if result != "short" {
		t.Errorf("truncateString = %q, want 'short'", result)
	}
}

func TestTruncateString_LongString(t *testing.T) {
	long := strings.Repeat("x", 200)
	result := truncateString(long, 50)
	if !strings.HasSuffix(result, "...") {
		t.Errorf("truncated string should end with '...'")
	}
	if len([]rune(result)) != 53 {
		t.Errorf("truncated length = %d, want 53", len([]rune(result)))
	}
}

// --- pruneGloballyLocked ---

func TestExampleStore_PruneGloballyLocked(t *testing.T) {
	es := newTestExampleStore(t)
	es.mu.Lock()
	for i := range 10 {
		es.examples["ex"+string(rune('A'+i))] = &TaskExample{
			ID:           "ex" + string(rune('A'+i)),
			TaskType:     "implement",
			InputPrompt:  "prompt",
			AgentType:    "general",
			FinalOutput:  "output",
			SuccessScore: float64(i) / 10.0,
			Created:      time.Now().Add(-time.Duration(i) * time.Hour),
		}
		es.byType["implement"] = append(es.byType["implement"], "ex"+string(rune('A'+i)))
	}
	es.pruneGloballyLocked(5)
	count := len(es.examples)
	es.mu.Unlock()

	if count != 5 {
		t.Errorf("after global prune, count = %d, want 5", count)
	}
}

// --- pruneOldExamples ---

func TestExampleStore_PruneOldExamples(t *testing.T) {
	es := newTestExampleStore(t)
	es.mu.Lock()
	for i := range 55 {
		id := "prune" + string(rune('A'+i))
		es.examples[id] = &TaskExample{
			ID:           id,
			TaskType:     "implement",
			SuccessScore: float64(i) / 55.0,
			Created:      time.Now().Add(-time.Duration(i) * time.Minute),
		}
		es.byType["implement"] = append(es.byType["implement"], id)
	}
	es.pruneOldExamples("implement", 50)
	count := len(es.examples)
	es.mu.Unlock()

	if count != 50 {
		t.Errorf("after pruneOldExamples, count = %d, want 50", count)
	}
}

// --- load from existing file ---

func TestExampleStore_LoadFromExistingFile(t *testing.T) {
	dir := t.TempDir()
	es1, err := NewExampleStore(dir)
	if err != nil {
		t.Fatalf("NewExampleStore: %v", err)
	}
	es1.LearnFromSuccess("implement", "persist this", "general", "done", 1*time.Second, 100)
	es1.Flush()

	// Create a second store pointing at the same dir — should load the saved example
	es2, err := NewExampleStore(dir)
	if err != nil {
		t.Fatalf("NewExampleStore (reload): %v", err)
	}
	t.Cleanup(func() { _ = es2.Flush() })

	stats := es2.GetStats()
	if stats.TotalExamples != 1 {
		t.Errorf("after reload, TotalExamples = %d, want 1", stats.TotalExamples)
	}
}

// --- load with corrupt data ---

func TestExampleStore_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	memDir := dir + "/memory"
	os.MkdirAll(memDir, 0755)
	os.WriteFile(memDir+"/examples.json", []byte("not valid json"), 0644)

	es, err := NewExampleStore(dir)
	if err != nil {
		t.Fatalf("NewExampleStore with corrupt file should not error: %v", err)
	}
	t.Cleanup(func() { _ = es.Flush() })
	if es.GetStats().TotalExamples != 0 {
		t.Error("corrupt file should result in empty store")
	}
}

// --- extractTags ---

func TestExtractTags_FiltersStopWords(t *testing.T) {
	tags := extractTags("the a an is how what please help me authentication")
	for _, tag := range tags {
		switch tag {
		case "the", "a", "an", "is", "how", "what", "please", "help", "me":
			t.Errorf("stop word %q should not be extracted as tag", tag)
		}
	}
}

func TestExtractTags_EmptyString(t *testing.T) {
	tags := extractTags("")
	if len(tags) != 0 {
		t.Errorf("extractTags('') = %v, want empty", tags)
	}
}

// --- Flush ---

func TestExampleStore_Flush_PersistsData(t *testing.T) {
	dir := t.TempDir()
	es, err := NewExampleStore(dir)
	if err != nil {
		t.Fatalf("NewExampleStore: %v", err)
	}
	es.LearnFromSuccess("implement", "flush test", "general", "done", 1*time.Second, 100)

	if err := es.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	data, err := os.ReadFile(dir + "/memory/examples.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "flush test") {
		t.Error("flushed file should contain the example prompt")
	}
}

// --- NewExampleStore MkdirAll failure ---

func TestNewExampleStore_MkdirFailure(t *testing.T) {
	tmp := t.TempDir()
	filePath := tmp + "/iamfile"
	os.WriteFile(filePath, []byte("x"), 0644)

	_, err := NewExampleStore(filePath)
	if err != nil {
		// NewExampleStore doesn't return error on MkdirAll failure (load handles it)
		// But it should not panic
		t.Logf("NewExampleStore returned error (acceptable): %v", err)
	}
}
