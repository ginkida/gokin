package context

import (
	"context"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/config"

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
	m := &ContextManager{
		session:       chat.NewSession(),
		tokenCounter:  &TokenCounter{},
		keyFiles:      map[string]bool{},
		messageScorer: NewMessageScorer(),
	}
	// SetClient with nil-safe client — tokenCounter.SetClient would panic on nil,
	// so we just verify the method path works with a minimal stub.
	// We can't easily test SetClient without a real client.Client, but we can
	// verify it doesn't panic when messageScorer is set.
	// Skipped: requires a full client.Client mock.
	_ = m
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
