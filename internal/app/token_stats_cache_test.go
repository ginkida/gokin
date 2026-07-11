package app

import (
	"testing"

	"gokin/internal/agent"
	appcontext "gokin/internal/context"
)

func TestGetTokenStatsIncludesCachedInput(t *testing.T) {
	a := &App{
		totalInputTokens:     25_000,
		totalOutputTokens:    2_000,
		totalCacheReadTokens: 20_000,
		totalEstimatedCost:   0.0123,
		costTracked:          true,
	}
	stats := a.GetTokenStats()
	if stats.InputTokens != 25_000 || stats.OutputTokens != 2_000 || stats.CacheReadInputTokens != 20_000 {
		t.Fatalf("token stats = %+v", stats)
	}
	if stats.TotalTokens != 27_000 {
		t.Fatalf("TotalTokens = %d, want 27000", stats.TotalTokens)
	}
	if !stats.CostTracked || stats.EstimatedCost != 0.0123 {
		t.Fatalf("cost ledger = tracked %v cost %v", stats.CostTracked, stats.EstimatedCost)
	}
}

func TestSessionCostLedgerDoesNotRepriceAfterModelSwitch(t *testing.T) {
	glm := appcontext.NewTokenCounter(nil, "glm-5", nil)
	kimi := appcontext.NewTokenCounter(nil, "kimi-for-coding", nil)
	glmTurnCost := glm.CalculateCostWithCache(1_000_000, 0, 1_000_000) // $0.20 cached GLM input
	kimiTurnCost := kimi.CalculateCostWithCache(1_000_000, 0, 0)       // $0.95 Kimi input

	// Simulate two completed turns separated by /model. The ledger stores the
	// price at execution time instead of repricing both million-token prompts
	// with whichever model happens to be active when /stats is opened.
	a := &App{
		totalInputTokens:   2_000_000,
		totalEstimatedCost: glmTurnCost + kimiTurnCost,
		costTracked:        true,
	}
	stats := a.GetTokenStats()
	if stats.EstimatedCost != 1.15 {
		t.Fatalf("mixed-model ledger cost = %v, want 1.15", stats.EstimatedCost)
	}
	if !stats.CostTracked {
		t.Fatal("mixed-model ledger should be authoritative")
	}
}

func TestAccumulateAgentResultUsageWithoutCostCounter(t *testing.T) {
	// Usage accounting itself must remain correct even when no context manager
	// is available (cost stays untracked, token totals do not disappear).
	a := &App{}
	a.accumulateAgentResultUsage(&agent.AgentResult{
		InputTokens:          10_000,
		OutputTokens:         500,
		CacheReadInputTokens: 8_000,
	})
	stats := a.GetTokenStats()
	if stats.InputTokens != 10_000 || stats.OutputTokens != 500 || stats.CacheReadInputTokens != 8_000 {
		t.Fatalf("delegated stats = %+v", stats)
	}
	if stats.CostTracked {
		t.Fatal("cost must remain untracked without a token counter")
	}
}
