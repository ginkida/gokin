package app

import "testing"

func TestResolveTurnTokenUsage(t *testing.T) {
	tests := []struct {
		name                           string
		apiIn, apiOut, cache, estimate int
		response                       string
		wantIn, wantOut, wantCache     int
	}{
		{
			name:      "provider usage wins",
			apiIn:     10_000,
			apiOut:    500,
			cache:     8_000,
			estimate:  99_000,
			response:  "ignored estimate",
			wantIn:    10_000,
			wantOut:   500,
			wantCache: 8_000,
		},
		{
			name:     "local fallback",
			estimate: 12_000,
			response: "12345678",
			wantIn:   12_000,
			wantOut:  2,
		},
		{
			name:     "unicode output uses runes",
			estimate: 100,
			response: "Привет!!", // 8 runes, 14 bytes
			wantIn:   100,
			wantOut:  2,
		},
		{
			name:      "cache is clamped to prompt",
			apiIn:     1_000,
			apiOut:    10,
			cache:     2_000,
			wantIn:    1_000,
			wantOut:   10,
			wantCache: 1_000,
		},
		{
			name:     "negative metadata is sanitized",
			apiIn:    -1,
			apiOut:   -1,
			cache:    -1,
			estimate: -1,
			wantIn:   0,
			wantOut:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, out, cached := resolveTurnTokenUsage(tt.apiIn, tt.apiOut, tt.cache, tt.estimate, tt.response)
			if in != tt.wantIn || out != tt.wantOut || cached != tt.wantCache {
				t.Fatalf("resolveTurnTokenUsage() = (%d,%d,%d), want (%d,%d,%d)",
					in, out, cached, tt.wantIn, tt.wantOut, tt.wantCache)
			}
		})
	}
}

func TestAccumulateTurnTokenUsageMixedProviderAndFallback(t *testing.T) {
	a := &App{}

	// A provider-reported turn is a true per-request delta.
	a.accumulateTurnTokenUsage(10_000, 10_000, 500, 8_000)
	// The next response omits usage. Its local context estimate is smaller
	// than the already accumulated billable total and must not erase it.
	a.accumulateTurnTokenUsage(0, 9_000, 100, 0)

	stats := a.GetTokenStats()
	if stats.InputTokens != 10_000 {
		t.Fatalf("mixed-mode input total = %d, want 10000", stats.InputTokens)
	}
	if stats.OutputTokens != 600 || stats.CacheReadInputTokens != 8_000 {
		t.Fatalf("mixed-mode stats = %+v", stats)
	}

	// A later fallback estimate can raise the lower bound as the context grows,
	// but it still is not added as though it were a provider delta.
	a.accumulateTurnTokenUsage(0, 12_000, 50, 20_000)
	stats = a.GetTokenStats()
	if stats.InputTokens != 20_000 || stats.OutputTokens != 650 {
		t.Fatalf("grown fallback stats = %+v", stats)
	}
	// Cache reads are clamped to the corresponding turn input defensively.
	if stats.CacheReadInputTokens != 20_000 {
		t.Fatalf("cache total = %d, want 20000", stats.CacheReadInputTokens)
	}
}

func TestCommitSessionUsagePersistsTerminalAttempt(t *testing.T) {
	a := &App{}
	cost, tracked := a.commitSessionUsage(1_000, 1_000, 25, 700, 0.42, true)
	if !tracked || cost != 0.42 {
		t.Fatalf("committed cost = tracked %v cost %v", tracked, cost)
	}
	stats := a.GetTokenStats()
	if stats.InputTokens != 1_000 || stats.OutputTokens != 25 || stats.CacheReadInputTokens != 700 {
		t.Fatalf("terminal-attempt stats = %+v", stats)
	}
	if !stats.CostTracked || stats.EstimatedCost != 0.42 {
		t.Fatalf("terminal-attempt ledger = tracked %v cost %v",
			stats.CostTracked, stats.EstimatedCost)
	}
}
