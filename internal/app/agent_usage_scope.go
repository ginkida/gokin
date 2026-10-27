package app

import (
	"context"
	"strings"

	"gokin/internal/agent"
	appcontext "gokin/internal/context"
	"gokin/internal/donegate"
)

// withHeadlessInvocationScope is the single headless lifecycle seam. Call it
// once on the run context after beginHeadlessPolicyTracking and before
// processMessageWithContext. Repeated calls in the same invocation reuse the
// token; a fresh headlessTerminal owner gets a fresh token.
func (a *App) withHeadlessInvocationScope(ctx context.Context) context.Context {
	if a == nil {
		return ctx
	}
	a.mu.Lock()
	if !a.headlessRunActive || a.headlessTerminal == nil {
		a.mu.Unlock()
		return ctx
	}
	if a.headlessAgentUsageScopeOwner != a.headlessTerminal || a.headlessAgentUsageScope.IsZero() {
		a.headlessAgentUsageScope = agent.NewInvocationScope()
		a.headlessAgentUsageScopeOwner = a.headlessTerminal
	}
	scope := a.headlessAgentUsageScope
	a.mu.Unlock()
	return agent.WithInvocationScope(ctx, scope)
}

// accumulateScopedAgentResultUsage always folds a completed delegated run
// into cumulative session totals. It folds the same run into headless
// invocation totals only when the immutable scope captured at Spawn/Resume
// exactly matches the currently active headless scope.
func (a *App) accumulateScopedAgentResultUsage(result *agent.AgentResult) {
	if result == nil {
		return
	}
	a.mu.Lock()
	if !result.InvocationScope.IsZero() && (!a.headlessRunActive && result.InvocationScope == a.responseAgentScope ||
		a.headlessRunActive && a.headlessAgentUsageScopeOwner == a.headlessTerminal && result.InvocationScope == a.headlessAgentUsageScope) {
		if partial, _ := result.Metadata["files_changed_partial"].(bool); partial {
			a.responseEvidence.PartialWrites = true
			if a.headlessRunActive {
				a.headlessFilesChangedPartial = true
			}
		}
		gatePaths := result.TouchedPaths
		paths := result.FilesChanged
		if paths == nil {
			paths = gatePaths
		}
		if isolated, _ := result.Metadata["isolated_workspace"].(bool); isolated {
			paths = nil
			switch applied := result.Metadata["isolated_workspace_applied_files"].(type) {
			case []string:
				paths = applied
			case []any:
				for _, value := range applied {
					if path, ok := value.(string); ok {
						paths = append(paths, path)
					}
				}
			}
			gatePaths = paths
		}
		paths = donegate.NormalizeTouchedPaths(a.workDir, paths)
		a.responseEvidence.MutatedPaths = donegate.NormalizeTouchedPaths(a.workDir, append(a.responseEvidence.MutatedPaths, paths...))
		if a.headlessRunActive {
			a.headlessFilesChanged = donegate.NormalizeTouchedPaths(a.workDir, append(a.headlessFilesChanged, paths...))
		}
		if len(paths) > 0 {
			a.responseMutationCount++
			a.responseTrivialEdit = false
		}
		if len(paths) > 0 {
			a.responseTouchedPaths = donegate.NormalizeTouchedPaths(a.workDir, append(a.responseTouchedPaths, gatePaths...))
		}
	}
	a.mu.Unlock()
	input := max(result.InputTokens, 0)
	output := max(result.OutputTokens, 0)
	cacheRead := min(max(result.CacheReadInputTokens, 0), input)
	if input == 0 && output == 0 && cacheRead == 0 {
		return
	}

	cost, costTracked := a.agentResultCost(result, input, output, cacheRead)

	a.mu.Lock()
	// Preserve accumulateAgentResultUsage's session semantics exactly. Agent
	// input is provider-reported billable usage, so every invocation adds.
	a.totalInputTokens += input
	a.totalOutputTokens += output
	a.totalCacheReadTokens += cacheRead
	if a.totalCacheReadTokens > a.totalInputTokens {
		a.totalInputTokens = a.totalCacheReadTokens
	}
	if costTracked {
		a.totalEstimatedCost += cost
		a.costTracked = true
	}

	if a.headlessRunActive && a.headlessAgentUsageScopeOwner == a.headlessTerminal &&
		!result.InvocationScope.IsZero() &&
		result.InvocationScope == a.headlessAgentUsageScope {
		a.headlessInvocationUsage.InputTokens += input
		a.headlessInvocationUsage.OutputTokens += output
		a.headlessInvocationUsage.CacheReadInputTokens += cacheRead
		a.headlessInvocationUsage.TotalTokens =
			a.headlessInvocationUsage.InputTokens + a.headlessInvocationUsage.OutputTokens
		if costTracked {
			a.headlessInvocationCost.EstimatedUSD += cost
			a.headlessInvocationCost.Tracked = true
		} else {
			// This invocation contains usage for which no provider/model price is
			// available. A later priced sample must not turn a partial sum into an
			// apparently complete cost.
			a.headlessCostIncomplete = true
			a.headlessInvocationCost.Tracked = false
		}
	}
	a.mu.Unlock()
}

func (a *App) agentResultCost(result *agent.AgentResult, input, output, cacheRead int) (float64, bool) {
	if result.CostTracked {
		return max(result.EstimatedCost, 0), true
	}

	provider := strings.ToLower(strings.TrimSpace(result.Provider))
	var counter *appcontext.TokenCounter
	switch {
	case provider == "ollama":
		counter = appcontext.NewTokenCounter(nil, result.Model, nil)
	case strings.TrimSpace(result.Model) != "":
		counter = appcontext.NewTokenCounter(nil, result.Model, nil)
	case a != nil && a.contextManager != nil:
		counter = a.contextManager.GetTokenCounter()
	}
	if counter == nil {
		return 0, false
	}
	cost := counter.CalculateCostWithCache(input, output, cacheRead)
	if provider == "ollama" {
		cost = 0
	}
	return max(cost, 0), true
}
