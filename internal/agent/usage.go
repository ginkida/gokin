package agent

import "gokin/internal/client"

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
}
