package context

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

var testNow = time.Now()

// ========== GetTokenUsage ==========

func TestContextManager_GetTokenUsage_Nil(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	usage := m.GetTokenUsage()
	if usage == nil {
		t.Fatal("should return non-nil even when lastUsage is nil")
	}
	if usage.MaxTokens != 0 {
		t.Errorf("MaxTokens = %d, want 0 for nil usage", usage.MaxTokens)
	}
}

func TestContextManager_GetTokenUsage_Set(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.lastUsage = &TokenUsage{
		InputTokens: 500,
		MaxTokens:   10000,
		NearLimit:   false,
	}
	m.mu.Unlock()

	usage := m.GetTokenUsage()
	if usage.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", usage.InputTokens)
	}
	if usage.MaxTokens != 10000 {
		t.Errorf("MaxTokens = %d, want 10000", usage.MaxTokens)
	}
}

func TestContextManager_ObserveAPIUsageIsAuthoritative(t *testing.T) {
	m := &ContextManager{
		session: chat.NewSession(),
		tokenCounter: &TokenCounter{
			limits: TokenLimits{MaxInputTokens: 1_000_000},
		},
		lastUsage: &TokenUsage{InputTokens: 100, MaxTokens: 1_000_000, IsEstimate: true},
	}

	m.ObserveAPIUsage(144_400)
	usage := m.GetTokenUsage()
	if usage.InputTokens != 144_400 {
		t.Fatalf("InputTokens = %d, want API value 144400", usage.InputTokens)
	}
	if usage.IsEstimate {
		t.Fatal("provider-reported usage must be authoritative")
	}
	if usage.PercentUsed != 0.1444 {
		t.Fatalf("PercentUsed = %v, want 0.1444", usage.PercentUsed)
	}
}

// ========== GetCurrentTokens ==========

func TestContextManager_GetCurrentTokens(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.currentTokens = 777
	m.mu.Unlock()

	if got := m.GetCurrentTokens(); got != 777 {
		t.Errorf("GetCurrentTokens() = %d, want 777", got)
	}
}

func TestContextManager_GetCurrentTokens_Default(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	if got := m.GetCurrentTokens(); got != 0 {
		t.Errorf("GetCurrentTokens() = %d, want 0 (default)", got)
	}
}

// ========== NeedsSummarization ==========

func TestContextManager_NeedsSummarization_NilUsage(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	if m.NeedsSummarization() {
		t.Error("should be false when lastUsage is nil")
	}
}

func TestContextManager_NeedsSummarization_NearLimit(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.lastUsage = &TokenUsage{NearLimit: true}
	m.mu.Unlock()

	if !m.NeedsSummarization() {
		t.Error("should be true when NearLimit is true")
	}
}

func TestContextManager_NeedsSummarization_NotNearLimit(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.lastUsage = &TokenUsage{NearLimit: false}
	m.mu.Unlock()

	if m.NeedsSummarization() {
		t.Error("should be false when NearLimit is false")
	}
}

// ========== GetContextHealth ==========

func TestContextManager_GetContextHealth(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{limits: TokenLimits{MaxInputTokens: 10000}},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.currentTokens = 5000
	m.mu.Unlock()

	total, max, pct := m.GetContextHealth()
	if total != 5000 {
		t.Errorf("total = %d, want 5000", total)
	}
	if max != 10000 {
		t.Errorf("max = %d, want 10000", max)
	}
	if pct != 0.5 {
		t.Errorf("pct = %v, want 0.5", pct)
	}
}

func TestContextManager_GetContextHealth_ZeroMax(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.currentTokens = 100
	m.mu.Unlock()

	total, max, pct := m.GetContextHealth()
	if total != 100 {
		t.Errorf("total = %d, want 100", total)
	}
	if max != 0 {
		t.Errorf("max = %d, want 0", max)
	}
	if pct != 0.0 {
		t.Errorf("pct = %v, want 0.0", pct)
	}
}

// ========== GetTokenCounter ==========

func TestContextManager_GetTokenCounter(t *testing.T) {
	tc := &TokenCounter{limits: TokenLimits{MaxInputTokens: 5000}}
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: tc,
		keyFiles:     map[string]bool{},
	}
	got := m.GetTokenCounter()
	if got != tc {
		t.Error("should return the same tokenCounter pointer")
	}
}

// ========== GetMetrics ==========

func TestContextManager_GetMetrics(t *testing.T) {
	metrics := NewContextMetrics()
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		metrics:      metrics,
		keyFiles:     map[string]bool{},
	}
	got := m.GetMetrics()
	if got != metrics {
		t.Error("should return the same metrics pointer")
	}
}

// ========== GetCacheStats ==========

func TestContextManager_GetCacheStats(t *testing.T) {
	sc := NewSummaryCache(100, 0)
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		summaryCache: sc,
		keyFiles:     map[string]bool{},
	}
	stats := m.GetCacheStats()
	_ = stats // just verify it doesn't panic
}

func TestContextManager_OptimizeContextRecordsSummaryCacheHit(t *testing.T) {
	sess := chat.NewSession()
	var history []*genai.Content
	for i := 0; i < 10; i++ {
		role := genai.Role(genai.RoleUser)
		if i%2 == 1 {
			role = genai.RoleModel
		}
		history = append(history, genai.NewContentFromText(fmt.Sprintf("message %02d with enough content for token estimates", i), role))
	}
	sess.SetHistory(history)

	mc := testkit.NewMockClient()
	mc.EnqueueText("Detailed stable summary of the middle conversation messages for cache reuse.")
	cfg := &config.ContextConfig{EnableAutoSummary: true, MaxInputTokens: 100000}

	m := &ContextManager{
		session:         sess,
		tokenCounter:    NewTokenCounter(mc, mc.GetModel(), cfg),
		summarizer:      NewSummarizer(mc),
		config:          cfg,
		metrics:         NewContextMetrics(),
		summaryCache:    NewSummaryCache(10, time.Hour),
		messageScorer:   NewMessageScorer(),
		summaryStrategy: SummaryStrategy{InitialMessageCount: 2, RecentMessageCount: 2, MinMessagesForSummary: 3, MaxHistorySize: 50, TargetRatio: 0.5},
		keyFiles:        map[string]bool{},
	}

	if err := m.OptimizeContext(context.Background()); err != nil {
		t.Fatalf("first OptimizeContext returned error: %v", err)
	}
	if got := len(mc.Calls()); got != 1 {
		t.Fatalf("first OptimizeContext LLM calls = %d, want 1", got)
	}

	sess.SetHistory(history)
	if err := m.OptimizeContext(context.Background()); err != nil {
		t.Fatalf("second OptimizeContext returned error: %v", err)
	}
	if got := len(mc.Calls()); got != 1 {
		t.Fatalf("second OptimizeContext should use cached summary; LLM calls = %d, want 1", got)
	}

	hits, misses, hitRate := m.metrics.GetCacheStats()
	if hits != 1 || misses != 1 || hitRate != 0.5 {
		t.Fatalf("manager cache metrics = hits:%d misses:%d rate:%v, want 1/1/0.5", hits, misses, hitRate)
	}

	stats := m.GetCacheStats()
	if stats.Hits != 1 || stats.Misses != 1 || stats.HitRate != 0.5 {
		t.Fatalf("summary cache stats = hits:%d misses:%d rate:%v, want 1/1/0.5", stats.Hits, stats.Misses, stats.HitRate)
	}
}

// ========== SetSummaryStrategy / GetSummaryStrategy ==========

func TestContextManager_SetGetSummaryStrategy(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	strat := SummaryStrategy{
		TargetRatio:           0.5,
		MaxHistorySize:        100,
		MinMessagesForSummary: 5,
	}
	m.SetSummaryStrategy(strat)

	got := m.GetSummaryStrategy()
	if got.TargetRatio != 0.5 {
		t.Errorf("TargetRatio = %v, want 0.5", got.TargetRatio)
	}
	if got.MaxHistorySize != 100 {
		t.Errorf("MaxHistorySize = %v, want 100", got.MaxHistorySize)
	}
}

// ========== SetPlanManager ==========

func TestContextManager_SetPlanManager(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.SetPlanManager(nil) // just verify it doesn't panic with nil
}

// ========== SetClient ==========

func TestContextManager_SetClient(t *testing.T) {
	mc := testkit.NewMockClient()
	tc := NewTokenCounter(mc, mc.GetModel(), nil)
	m := &ContextManager{
		ctx:           context.Background(),
		session:       chat.NewSession(),
		tokenCounter:  tc,
		client:        mc,
		keyFiles:      map[string]bool{},
		messageScorer: NewMessageScorer(),
	}
	// SetClient with a real mock — exercises tokenCounter.SetClient + messageScorer.SetSemanticClient
	mc2 := testkit.NewMockClient()
	m.SetClient(mc2)
	// Verify it didn't panic and client was updated
	m.mu.RLock()
	got := m.client
	m.mu.RUnlock()
	if got != mc2 {
		t.Error("SetClient should update the client")
	}
}

// ========== SetConfig ==========

func TestContextManager_SetConfig(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	cfg := &config.ContextConfig{
		MaxInputTokens: 50000,
	}
	m.SetConfig(cfg)

	m.mu.RLock()
	got := m.config
	m.mu.RUnlock()
	if got == nil || got.MaxInputTokens != 50000 {
		t.Error("config was not set correctly")
	}
}

// ========== StartSessionWatcher ==========

func TestContextManager_StartSessionWatcher(t *testing.T) {
	m := &ContextManager{
		ctx:          context.Background(),
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	// Just verify it doesn't panic
	m.StartSessionWatcher()
}

// ========== ForceSummarize ==========

func TestContextManager_ForceSummarize_NoSummarizer(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	err := m.ForceSummarize(context.Background())
	if err == nil {
		t.Fatal("expected error when summarizer is nil")
	}
}

// ========== predictTokensAfterRequest ==========

func TestContextManager_PredictTokensAfterRequest_NotEnoughData(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.tokenHistory = []tokenSnapshot{
		{tokens: 100, timestamp: testNow},
		{tokens: 200, timestamp: testNow},
	}
	m.mu.Unlock()

	if got := m.predictTokensAfterRequest(); got != 0 {
		t.Errorf("predictTokensAfterRequest() = %d, want 0 for < 3 samples", got)
	}
}

func TestContextManager_PredictTokensAfterRequest_EnoughData(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.tokenHistory = []tokenSnapshot{
		{tokens: 100, timestamp: testNow},
		{tokens: 200, timestamp: testNow},
		{tokens: 300, timestamp: testNow},
	}
	m.mu.Unlock()

	got := m.predictTokensAfterRequest()
	// avgGrowth = (100+100)/2 = 100, predicted = 300+100 = 400
	if got != 400 {
		t.Errorf("predictTokensAfterRequest() = %d, want 400", got)
	}
}

func TestContextManager_PredictTokensAfterRequest_ZeroGrowth(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.mu.Lock()
	m.tokenHistory = []tokenSnapshot{
		{tokens: 200, timestamp: testNow},
		{tokens: 200, timestamp: testNow},
		{tokens: 200, timestamp: testNow},
	}
	m.mu.Unlock()

	// All zero growth → count=0 → returns 0
	if got := m.predictTokensAfterRequest(); got != 0 {
		t.Errorf("predictTokensAfterRequest() = %d, want 0 for zero growth", got)
	}
}

// ========== trackKeyFiles ==========

func TestContextManager_TrackKeyFiles_EmptyHistory(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	m.trackKeyFiles(chat.ChangeEvent{OldCount: 0, NewCount: 0})
	if len(m.keyFiles) != 0 {
		t.Error("should not track any files for empty history")
	}
}

func TestContextManager_TrackKeyFiles_ExtractsPaths(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{
					Name: "edit",
					Args: map[string]any{"file_path": "/src/main.go"},
				}},
			},
		},
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{"path": "/src/util.go"},
				}},
			},
		},
	}
	m.session.SetHistory(history)

	m.trackKeyFiles(chat.ChangeEvent{OldCount: 0, NewCount: 2})

	m.mu.RLock()
	hasMain := m.keyFiles["/src/main.go"]
	hasUtil := m.keyFiles["/src/util.go"]
	m.mu.RUnlock()

	if !hasMain {
		t.Error("should track /src/main.go from file_path arg")
	}
	if !hasUtil {
		t.Error("should track /src/util.go from path arg")
	}
}

func TestContextManager_TrackKeyFiles_StartBeyondHistory(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	history := []*genai.Content{
		genai.NewContentFromText("msg", genai.RoleUser),
	}
	m.session.SetHistory(history)

	// OldCount beyond history length → should return without tracking
	m.trackKeyFiles(chat.ChangeEvent{OldCount: 10, NewCount: 11})
	if len(m.keyFiles) != 0 {
		t.Error("should not track when start >= len(history)")
	}
}

// ========== tryAutoCompact ==========

func TestContextManager_TryAutoCompact_NoSummarizer(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	// summarizer is nil → should return immediately
	m.tryAutoCompact(context.Background(), 99999)
}

func TestContextManager_TryAutoCompact_Disabled(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
		config:       &config.ContextConfig{EnableAutoSummary: false},
	}
	// EnableAutoSummary=false → should return
	m.tryAutoCompact(context.Background(), 99999)
}

func TestContextManager_TryAutoCompact_BelowThreshold(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{limits: TokenLimits{MaxInputTokens: 100000}},
		keyFiles:     map[string]bool{},
		config:       &config.ContextConfig{EnableAutoSummary: true},
	}
	// 100 tokens / 100000 max = 0.1% — well below 0.75 threshold
	m.tryAutoCompact(context.Background(), 100)
}

// ========== IncrementalCompact — short history ==========

func TestContextManager_IncrementalCompact_ShortHistory(t *testing.T) {
	m := &ContextManager{
		session:         chat.NewSession(),
		tokenCounter:    &TokenCounter{},
		keyFiles:        map[string]bool{},
		summaryStrategy: DefaultSummaryStrategy(),
	}
	m.session.SetHistory([]*genai.Content{
		genai.NewContentFromText("msg1", genai.RoleUser),
		genai.NewContentFromText("msg2", genai.RoleModel),
	})

	err := m.IncrementalCompact(context.Background())
	if err != nil {
		t.Errorf("IncrementalCompact on short history should not error: %v", err)
	}
}

// ========== NewContextManager ==========

func TestNewContextManager_NilConfig(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, mc, nil)
	if m == nil {
		t.Fatal("NewContextManager returned nil")
	}
	if m.session != sess {
		t.Error("session not set")
	}
	if m.tokenCounter == nil {
		t.Error("tokenCounter not set")
	}
	if m.metrics == nil {
		t.Error("metrics not set")
	}
	if m.summaryCache == nil {
		t.Error("summaryCache not set")
	}
	if m.messageScorer == nil {
		t.Error("messageScorer not set")
	}
	if m.responseCompressor == nil {
		t.Error("responseCompressor not set")
	}
	if m.summarizer != nil {
		t.Error("summarizer should be nil when EnableAutoSummary is false/nil config")
	}
}

func TestNewContextManager_WithAutoSummary(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	cfg := &config.ContextConfig{EnableAutoSummary: true}
	m := NewContextManager(context.Background(), sess, mc, cfg)
	if m.summarizer == nil {
		t.Error("summarizer should be set when EnableAutoSummary is true")
	}
}

func TestNewContextManager_WithToolResultMaxChars(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	cfg := &config.ContextConfig{ToolResultMaxChars: 5000}
	m := NewContextManager(context.Background(), sess, mc, cfg)
	rc := m.responseCompressor
	if rc.maxChars != 5000 {
		t.Errorf("maxChars = %d, want 5000", rc.maxChars)
	}
}

// ========== PrepareForRequest ==========

func TestPrepareForRequest_EmptyHistory(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, mc, nil)
	err := m.PrepareForRequest(context.Background())
	if err != nil {
		t.Fatalf("PrepareForRequest on empty history: %v", err)
	}
	// Should set currentTokens to 0 for empty history
	if got := m.GetCurrentTokens(); got != 0 {
		t.Errorf("currentTokens = %d, want 0 for empty history", got)
	}
}

func TestPrepareForRequest_WithHistory(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	sess.SetHistory([]*genai.Content{
		genai.NewContentFromText("hello world this is a test message", genai.RoleUser),
	})
	m := NewContextManager(context.Background(), sess, mc, nil)
	err := m.PrepareForRequest(context.Background())
	if err != nil {
		t.Fatalf("PrepareForRequest: %v", err)
	}
	// Should have some token estimate
	if got := m.GetCurrentTokens(); got <= 0 {
		t.Errorf("currentTokens = %d, want > 0 for non-empty history", got)
	}
}

func TestPrepareForRequest_SetsUsage(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	sess.SetHistory([]*genai.Content{
		genai.NewContentFromText("test message", genai.RoleUser),
	})
	m := NewContextManager(context.Background(), sess, mc, nil)
	if err := m.PrepareForRequest(context.Background()); err != nil {
		t.Fatalf("PrepareForRequest: %v", err)
	}
	usage := m.GetTokenUsage()
	if usage == nil {
		t.Fatal("usage should not be nil after PrepareForRequest")
	}
	if !usage.IsEstimate {
		t.Error("usage should be an estimate after PrepareForRequest")
	}
}

// ========== UpdateTokenCount ==========

func TestUpdateTokenCount_EmptyHistory(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, mc, nil)
	err := m.UpdateTokenCount(context.Background())
	if err != nil {
		t.Fatalf("UpdateTokenCount: %v", err)
	}
	if got := m.GetCurrentTokens(); got != 0 {
		t.Errorf("currentTokens = %d, want 0 for empty history", got)
	}
}

func TestUpdateTokenCount_WithHistory(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	sess.SetHistory([]*genai.Content{
		genai.NewContentFromText("hello world this is a test", genai.RoleUser),
	})
	m := NewContextManager(context.Background(), sess, mc, nil)
	err := m.UpdateTokenCount(context.Background())
	if err != nil {
		t.Fatalf("UpdateTokenCount: %v", err)
	}
	// Should have some token count (estimated since mock client won't have CountTokens)
	if got := m.GetCurrentTokens(); got <= 0 {
		t.Errorf("currentTokens = %d, want > 0", got)
	}
}

// ========== Close ==========

func TestClose_DoesNotPanic(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, mc, nil)
	// Close should cancel the internal context without panicking
	m.Close()
}

// ========== GetKeyFiles ==========

func TestGetKeyFiles_Empty(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{},
	}
	files := m.GetKeyFiles()
	if len(files) != 0 {
		t.Errorf("GetKeyFiles = %v, want empty", files)
	}
}

func TestGetKeyFiles_WithFiles(t *testing.T) {
	m := &ContextManager{
		session:      chat.NewSession(),
		tokenCounter: &TokenCounter{},
		keyFiles:     map[string]bool{"main.go": true, "test.go": true},
	}
	files := m.GetKeyFiles()
	if len(files) != 2 {
		t.Errorf("GetKeyFiles len = %d, want 2", len(files))
	}
}

// fakePlanManagerForTaskContext returns a fixed contract string.
type fakePlanManagerForTaskContext struct{ contract string }

func (f fakePlanManagerForTaskContext) GetActiveContractContext() string { return f.contract }

// TestSyncTaskContext_ParsesRealContractFormat pins the v0.100.73 #11 fix:
// syncTaskContext parsed a "Current step:" shape the real generator
// (plan/executor.go GetActiveContractContext) never emits, so CurrentStep and
// SuccessCriteria were always empty (dead branches) and task-aware summarization
// silently dropped the in-flight step's constraints. Feed the REAL emitted shape.
func TestSyncTaskContext_ParsesRealContractFormat(t *testing.T) {
	// Exactly the shape plan/executor.go GetActiveContractContext emits.
	contract := "Plan: Ship feature X\n" +
		"Goal: make the widget configurable\n\n" +
		"Current Step Contract:\n" +
		"- Step 3: Wire the config flag\n" +
		"- Success criteria: flag parsed; default preserved; tests pass\n" +
		"- Completion proof required: changed files and/or commands/output evidence\n"

	m := &ContextManager{
		summarizer:  NewSummarizer(testkit.NewMockClient()),
		planManager: fakePlanManagerForTaskContext{contract: contract},
	}
	m.syncTaskContext()

	tc := m.summarizer.taskContext
	if tc == nil {
		t.Fatal("task context should be populated")
	}
	if tc.Title != "Ship feature X" {
		t.Fatalf("Title = %q", tc.Title)
	}
	if tc.CurrentStep != "Step 3: Wire the config flag" {
		t.Fatalf("CurrentStep = %q, want the real '- Step N: ...' line", tc.CurrentStep)
	}
	if len(tc.SuccessCriteria) != 3 {
		t.Fatalf("SuccessCriteria = %v, want 3 items split on '; '", tc.SuccessCriteria)
	}
}
