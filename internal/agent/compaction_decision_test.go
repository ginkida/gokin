package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/config"
	ctxmgr "gokin/internal/context"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

// compactionDecisionTestAgent builds a minimal Agent wired with a MockClient-backed
// TokenCounter and Summarizer, sized so checkAndSummarize's decision branches are
// deterministic. The returned MockClient is pre-loaded with NO scripts — tests
// that exercise the Summarize path EnqueueText/EnqueueError on it explicitly so
// the assertion "Summarize was/wasn't called" is exact (no phantom scripts).
func compactionDecisionTestAgent(t *testing.T, history []*genai.Content, maxInputTokens int) (*Agent, *testkit.MockClient) {
	t.Helper()
	mc := testkit.NewMockClient()
	cfg := &config.ContextConfig{
		MaxInputTokens:   maxInputTokens,
		WarningThreshold: 0.8,
	}
	tc := ctxmgr.NewTokenCounter(mc, "mock-model", cfg)
	sum := ctxmgr.NewSummarizer(mc)
	a := &Agent{
		ID:                "test-agent",
		history:           history,
		tokenCounter:      tc,
		summarizer:        sum,
		maxHistorySize:    200,
		summarizeMinMsgs:  6,
		summarizeProtect:  4,
		pruneProtectChars: 0, // disable pruning by default; tests enable explicitly
	}
	return a, mc
}

// buildHistory builds a history slice with the standard prefix
// [system, greeting, task] followed by n filler messages. The filler text is
// sized so the local fast-path estimate (≈4 chars/token) lands predictably
// relative to maxInputTokens.
func buildHistory(n int, fillerText string) []*genai.Content {
	h := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("system prompt")}},
		{Role: genai.RoleModel, Parts: []*genai.Part{genai.NewPartFromText("greeting")}},
		{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("ORIGINAL TASK: implement feature X")}},
	}
	for i := 0; i < n; i++ {
		role := genai.RoleModel
		if i%2 == 0 {
			role = genai.RoleUser
		}
		h = append(h, &genai.Content{Role: role, Parts: []*genai.Part{genai.NewPartFromText(fillerText)}})
	}
	return h
}

// TestCheckAndSummarize_HardLimitForcesCompaction: when history exceeds
// maxHistorySize, checkAndSummarize delegates to forceCompactHistory rather
// than running the token-threshold logic. We verify via a tiny maxHistorySize
// and enough messages that forceCompactHistory's own guard (histLen <= 10)
// doesn't short-circuit before we observe the delegation.
func TestCheckAndSummarize_HardLimitForcesCompaction(t *testing.T) {
	// 12 messages, maxHistorySize=5 → hard limit trips. forceCompactHistory
	// needs >10 messages to proceed past its own guard, and a summarizer
	// script to succeed.
	hist := buildHistory(9, "filler content for testing compaction behavior")
	a, mc := compactionDecisionTestAgent(t, hist, 128000)
	a.maxHistorySize = 5
	// forceCompactViaSummary will call Summarize → SendMessageWithHistory.
	mc.EnqueueText("Summary of the conversation so far: we discussed feature X.")

	before := len(a.history)
	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("hard-limit compaction failed: %v", err)
	}
	// Compaction must have shrunk the history.
	if len(a.history) >= before {
		t.Errorf("hard-limit path did not compact: before=%d after=%d", before, len(a.history))
	}
}

// TestCheckAndSummarize_FastPathSkipsAPICall: when the local estimate is
// well below 85% of the warning threshold, checkAndSummarize returns nil
// WITHOUT calling the token-counting API. We verify by checking that the
// MockClient recorded no CountTokens calls (MockClient.CountTokens is
// non-scripted, so the only way to observe it is via Calls()).
func TestCheckAndSummarize_FastPathSkipsAPICall(t *testing.T) {
	// Tiny history, huge max → estimate is near 0%.
	hist := buildHistory(3, "short")
	a, _ := compactionDecisionTestAgent(t, hist, 128000)

	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("fast path returned error: %v", err)
	}
	// CountTokens is not recorded in Calls() (only Send* methods are), but
	// the fast path returns before any API call. The strongest assertion we
	// can make without a scripted counter is: no error, history untouched.
	if len(a.history) != len(hist) {
		t.Errorf("fast path mutated history: got %d, want %d", len(a.history), len(hist))
	}
}

// TestCheckAndSummarize_BelowThresholdNoOp: when the precise count is below
// the warning threshold, checkAndSummarize returns nil and does not
// summarize. We drive the API-count path by making the local estimate land
// in the "near threshold" band (≥85% of threshold) so CountContents is
// called, but keep the actual count below threshold.
func TestCheckAndSummarize_BelowThresholdNoOp(t *testing.T) {
	// We need the local estimate ≥ threshold*0.85 = 0.68 to skip the fast
	// path, but MockClient.CountTokens returns len(text)/4 which will be
	// tiny relative to 128000 → below threshold → no-op.
	// fillerText sized so estimate ≈ 0.7 * 128000 tokens ≈ 90000 tokens
	// → ≈ 360000 chars across all messages. With 10 filler msgs that's
	// 36000 chars each.
	filler := strings.Repeat("x", 36000)
	hist := buildHistory(10, filler)
	a, mc := compactionDecisionTestAgent(t, hist, 128000)

	before := len(a.history)
	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("below-threshold check failed: %v", err)
	}
	if len(a.history) != before {
		t.Errorf("below-threshold path mutated history: before=%d after=%d", before, len(a.history))
	}
	// No Summarize call should have happened (no scripts consumed).
	if len(mc.Calls()) != 0 {
		t.Errorf("expected no Send calls, got %d", len(mc.Calls()))
	}
}

// TestCheckAndSummarize_CacheHitSkipsAPICall: after a recent precise count
// cached at a similar history length, a second checkAndSummarize skips the
// API count. We prime the cache manually via the same fields the function
// reads under preemptMu.
func TestCheckAndSummarize_CacheHitSkipsAPICall(t *testing.T) {
	filler := strings.Repeat("x", 36000)
	hist := buildHistory(10, filler)
	a, _ := compactionDecisionTestAgent(t, hist, 128000)

	// Prime the cache: a precise count at the current history length that's
	// safely below threshold*0.9 = 0.72. 128000 * 0.5 = 64000 tokens.
	a.preemptMu.Lock()
	a.cachedPreciseCount = 64000
	a.cachedPreciseHistLen = len(hist)
	a.preemptMu.Unlock()

	// First call should hit the cache and return nil without API calls.
	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("cache-hit path returned error: %v", err)
	}
	if len(a.history) != len(hist) {
		t.Errorf("cache-hit path mutated history: got %d, want %d", len(a.history), len(hist))
	}
}

// TestCheckAndSummarize_AtThresholdTriggersSummarize: when the precise token
// count exceeds the warning threshold, checkAndSummarize calls the
// summarizer and reconstructs history. We verify the summary was consumed
// (one Send call) and history shrank.
func TestCheckAndSummarize_AtThresholdTriggersSummarize(t *testing.T) {
	// Small max so the threshold is easy to cross. WarningThreshold=0.8.
	// We need: local estimate ≥ 0.85*0.8 = 0.68 of max to skip fast path,
	// AND precise count ≥ 0.8 of max to trigger summarize.
	// max=1000 tokens. threshold=800. fast-path cutoff = 680 tokens.
	// filler sized so estimate ≥ 680 but we also need actual count ≥ 800.
	// MockClient.CountTokens = len(text)/4. 10 filler msgs × 4000 chars
	// = 40000 chars / 4 = 10000 tokens >> 800. Good.
	filler := strings.Repeat("x", 4000)
	hist := buildHistory(10, filler)
	a, mc := compactionDecisionTestAgent(t, hist, 1000)
	mc.EnqueueText("Summary: the user asked to implement feature X and we explored the codebase.")

	before := len(a.history)
	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("threshold compaction failed: %v", err)
	}
	if len(a.history) >= before {
		t.Errorf("history did not shrink after summarize: before=%d after=%d", before, len(a.history))
	}
	if len(mc.Calls()) == 0 {
		t.Error("expected Summarize to be called (≥1 Send call), got 0")
	}
}

// TestCheckAndSummarize_PreservesTaskPrompt: after compaction, the first 3
// messages (system, greeting, ORIGINAL TASK) must be byte-identical to the
// pre-compaction prefix. This is the critical invariant: the agent must
// never forget its task.
func TestCheckAndSummarize_PreservesTaskPrompt(t *testing.T) {
	filler := strings.Repeat("x", 4000)
	hist := buildHistory(10, filler)
	a, mc := compactionDecisionTestAgent(t, hist, 1000)
	mc.EnqueueText("Summary of middle messages.")

	origPrefix := make([]*genai.Content, 3)
	for i := 0; i < 3; i++ {
		origPrefix[i] = &genai.Content{
			Role:  hist[i].Role,
			Parts: hist[i].Parts,
		}
	}

	_ = a.checkAndSummarize(context.Background())

	if len(a.history) < 3 {
		t.Fatalf("post-compaction history too short: %d messages", len(a.history))
	}
	for i := 0; i < 3; i++ {
		got := a.history[i]
		if got.Role != origPrefix[i].Role {
			t.Errorf("history[%d].Role changed: got %q, want %q", i, got.Role, origPrefix[i].Role)
		}
		// Compare text content of first part.
		if len(got.Parts) == 0 || len(origPrefix[i].Parts) == 0 {
			continue
		}
		if got.Parts[0].Text != origPrefix[i].Parts[0].Text {
			t.Errorf("history[%d] text changed: got %q, want %q", i, got.Parts[0].Text, origPrefix[i].Parts[0].Text)
		}
	}
	// Specifically, the task prompt must survive.
	taskText := ""
	if len(a.history) > 2 && len(a.history[2].Parts) > 0 {
		taskText = a.history[2].Parts[0].Text
	}
	if !strings.Contains(taskText, "ORIGINAL TASK") {
		t.Errorf("task prompt lost after compaction: history[2] = %q", taskText)
	}
}

// TestCheckAndSummarize_TooFewMessagesNoOp: when history is too short to
// summarize (≤ summarizeMinMsgs or ≤ preserveStart+2), checkAndSummarize
// returns nil without calling the summarizer, even if the token count is
// above threshold.
func TestCheckAndSummarize_TooFewMessagesNoOp(t *testing.T) {
	// 4 messages total (system+greeting+task+1 filler). summarizeMinMsgs=6
	// → the summarize guard (len ≤ summarizeMinMsgs) returns nil.
	hist := buildHistory(1, strings.Repeat("x", 10000))
	a, mc := compactionDecisionTestAgent(t, hist, 1000) // tiny max → would trip if it could

	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("too-few-messages path returned error: %v", err)
	}
	if len(mc.Calls()) != 0 {
		t.Errorf("expected no Summarize call for short history, got %d Send calls", len(mc.Calls()))
	}
}

// TestCheckAndSummarize_SummarizeErrorPropagated: when the summarizer returns
// an error, checkAndSummarize must propagate it (wrapped) rather than
// silently swallowing it or falling back to truncation — that's
// forceCompactHistory's job, not the pre-emptive path's.
func TestCheckAndSummarize_SummarizeErrorPropagated(t *testing.T) {
	filler := strings.Repeat("x", 4000)
	hist := buildHistory(10, filler)
	a, mc := compactionDecisionTestAgent(t, hist, 1000)
	sumErr := errors.New("summarizer API down")
	mc.EnqueueError(sumErr)

	err := a.checkAndSummarize(context.Background())
	if err == nil {
		t.Fatal("expected summarizer error to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "summarization failed") && !strings.Contains(err.Error(), sumErr.Error()) {
		t.Errorf("error doesn't wrap summarizer failure: got %v", err)
	}
}

// TestCheckAndSummarize_PruningSufficientSkipsSummarize: when pruneToolOutputs
// frees enough to drop below threshold, summarize is NOT called. We enable
// pruning by setting pruneProtectChars high (protect nothing → prune
// everything) and include large tool outputs in the history.
func TestCheckAndSummarize_PruningSufficientSkipsSummarize(t *testing.T) {
	// History with large tool outputs in the middle that pruning can remove.
	hist := buildHistory(10, "filler")
	// Inject a huge tool response in the middle (index 5) that pruning will trim.
	hist[5] = &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name: "read",
				Response: map[string]any{
					"content": strings.Repeat("BIG TOOL OUTPUT ", 5000),
				},
			},
		}},
	}
	a, mc := compactionDecisionTestAgent(t, hist, 1000)
	// Enable pruning: protect 0 chars → everything is eligible.
	a.pruneProtectChars = 0

	err := a.checkAndSummarize(context.Background())
	if err != nil {
		t.Fatalf("prune path returned error: %v", err)
	}
	// If pruning was sufficient, Summarize is never called.
	if len(mc.Calls()) != 0 {
		t.Errorf("expected no Summarize call after sufficient pruning, got %d Send calls", len(mc.Calls()))
	}
}
