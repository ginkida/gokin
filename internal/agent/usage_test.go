package agent

import (
	"context"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestRecordResponseUsageIncludesCachedInput(t *testing.T) {
	a := &Agent{}

	a.stateMu.Lock()
	a.recordResponseUsageLocked(&client.Response{
		InputTokens:          10_000,
		OutputTokens:         500,
		CacheReadInputTokens: 8_000,
	})
	a.recordResponseUsageLocked(&client.Response{
		InputTokens:          12_000,
		OutputTokens:         600,
		CacheReadInputTokens: 20_000, // malformed metadata is clamped per round
	})
	a.stateMu.Unlock()

	if a.usageInputTokens != 22_000 {
		t.Fatalf("input usage = %d, want 22000 (cached input must remain included)", a.usageInputTokens)
	}
	if a.usageOutputTokens != 1_100 {
		t.Fatalf("output usage = %d, want 1100", a.usageOutputTokens)
	}
	if a.usageCacheReadTokens != 20_000 {
		t.Fatalf("cache-read usage = %d, want 20000", a.usageCacheReadTokens)
	}
}

func TestRecordResponseUsageSanitizesNegativeMetadata(t *testing.T) {
	a := &Agent{}
	a.stateMu.Lock()
	a.recordResponseUsageLocked(&client.Response{
		InputTokens:          -1,
		OutputTokens:         -2,
		CacheReadInputTokens: -3,
	})
	a.recordResponseUsageLocked(nil)
	a.stateMu.Unlock()

	if a.usageInputTokens != 0 || a.usageOutputTokens != 0 || a.usageCacheReadTokens != 0 {
		t.Fatalf("negative usage changed ledger: in=%d out=%d cache=%d",
			a.usageInputTokens, a.usageOutputTokens, a.usageCacheReadTokens)
	}
}

func TestAgentResultCarriesResolvedClientModel(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.SetModel("resolved-provider-model")
	mock.EnqueueText("done")

	a := NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 2, "requested-alias", nil, nil)
	// NewAgent was intentionally constructed without a client to avoid cloning
	// the scripted mock; inject the isolated resolved client used by Run.
	a.client = mock
	result, err := a.Run(context.Background(), "finish")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Model != "resolved-provider-model" {
		t.Fatalf("result.Model = %q, want resolved-provider-model", result.Model)
	}
}
