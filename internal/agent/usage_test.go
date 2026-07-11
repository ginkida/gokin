package agent

import (
	"context"
	"errors"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type providerMockClient struct {
	*testkit.MockClient
	provider string
}

func (c *providerMockClient) GetProvider() string { return c.provider }

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

func TestRecordResponseUsagePricesEachProviderRound(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.SetModel("glm-5")
	identified := &providerMockClient{MockClient: mock, provider: "glm"}
	a := &Agent{client: identified}

	a.stateMu.Lock()
	a.recordResponseUsageLocked(&client.Response{
		InputTokens:          1_000_000,
		CacheReadInputTokens: 1_000_000,
	}) // cached GLM-5 input: $0.20
	identified.provider = "kimi"
	mock.SetModel("kimi-for-coding")
	a.recordResponseUsageLocked(&client.Response{
		InputTokens: 1_000_000,
	}) // Kimi input: $0.95
	a.stateMu.Unlock()

	if !a.usageCostTracked || a.usageEstimatedCost != 1.15 {
		t.Fatalf("mixed-provider ledger = tracked %v cost %v, want 1.15",
			a.usageCostTracked, a.usageEstimatedCost)
	}
}

func TestAgentResultCarriesResolvedClientModel(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.SetModel("primary-model")
	mock.EnqueueText("done")
	identified := &providerMockClient{MockClient: mock, provider: "glm"}
	// Simulate a fallback switch during the request. Identity captured before
	// executeLoop would incorrectly retain glm/primary-model.
	mock.OnSend = func(context.Context) {
		identified.provider = "deepseek"
		mock.SetModel("resolved-provider-model")
	}

	a := NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 2, "requested-alias", nil, nil)
	// NewAgent was intentionally constructed without a client to avoid cloning
	// the scripted mock; inject the isolated resolved client used by Run.
	a.client = identified
	result, err := a.Run(context.Background(), "finish")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Model != "resolved-provider-model" {
		t.Fatalf("result.Model = %q, want resolved-provider-model", result.Model)
	}
	if result.Provider != "deepseek" {
		t.Fatalf("result.Provider = %q, want deepseek", result.Provider)
	}
}

func TestAgentResultAccountsUsageBeforeStreamError(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.SetModel("glm-5")
	mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{InputTokens: 1_000, OutputTokens: 25, CacheReadInputTokens: 700},
		{Error: errors.New("terminal invalid request"), Done: true},
	}})
	a := NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 2, "", nil, nil)
	a.client = &providerMockClient{MockClient: mock, provider: "glm"}

	result, err := a.Run(context.Background(), "fail after usage")
	if err == nil {
		t.Fatal("Run() error = nil, want stream failure")
	}
	if result.InputTokens != 1_000 || result.OutputTokens != 25 || result.CacheReadInputTokens != 700 {
		t.Fatalf("partial-error agent result = %+v", result)
	}
	if !result.CostTracked || result.EstimatedCost <= 0 {
		t.Fatalf("partial-error cost = tracked %v cost %v", result.CostTracked, result.EstimatedCost)
	}
}
