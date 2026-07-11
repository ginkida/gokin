package agent

import (
	"strings"

	"gokin/internal/client"
	appcontext "gokin/internal/context"
)

func (a *Agent) populateResultIdentity(result *AgentResult) {
	if result == nil || a.client == nil {
		return
	}
	result.Model = a.client.GetModel()
	if identified, ok := a.client.(client.ProviderIdentity); ok {
		result.Provider = identified.GetProvider()
	}
}

// recordResponseUsageLocked adds one successful provider round to the current
// run's ledger. Caller must hold stateMu.
func (a *Agent) recordResponseUsageLocked(resp *client.Response) {
	if resp == nil {
		return
	}
	input := max(resp.InputTokens, 0)
	cacheRead := min(max(resp.CacheReadInputTokens, 0), input)
	a.usageInputTokens += input
	a.usageOutputTokens += max(resp.OutputTokens, 0)
	a.usageCacheReadTokens += cacheRead

	// Price each provider request immediately. FallbackClient may switch
	// providers between tool rounds, so repricing the aggregate at Run end with
	// the final provider silently applies one tariff to several backends.
	if a.client != nil {
		model := strings.TrimSpace(a.client.GetModel())
		provider := ""
		if identified, ok := a.client.(client.ProviderIdentity); ok {
			provider = strings.ToLower(strings.TrimSpace(identified.GetProvider()))
		}
		if provider == "ollama" {
			a.usageCostTracked = true // known zero
		} else if model != "" {
			tc := appcontext.NewTokenCounter(nil, model, nil)
			a.usageEstimatedCost += tc.CalculateCostWithCache(input, max(resp.OutputTokens, 0), cacheRead)
			a.usageCostTracked = true
		}
	}
}
