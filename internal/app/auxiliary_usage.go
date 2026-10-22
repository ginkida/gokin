package app

import (
	"context"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/tools"
)

func (a *App) withAuxiliaryRequests(ctx context.Context) context.Context {
	scope := agent.InvocationScopeFromContext(ctx)
	_, bounded := tools.MaxTurnsFromContext(ctx)
	_, budgeted := tools.InvocationBudgetLedgerFromContext(ctx)
	bounded = bounded || budgeted
	return client.ContextWithAuxiliaryRequests(ctx, client.AuxiliaryRequestPolicy{
		Client: func(base client.Client) client.Client {
			return client.WithRequestGuard(agent.ClientWithInvocationLimits(base), hooks.AsyncRewakeRequestGuard)
		},
		Bounded: bounded,
		Record: func(provider, model string, response *client.Response) {
			a.accumulateScopedAgentResultUsage(&agent.AgentResult{
				Provider: provider, Model: model, InvocationScope: scope,
				InputTokens: response.TotalInputTokens(), OutputTokens: response.OutputTokens,
				CacheReadInputTokens: response.CacheReadInputTokens,
			})
		},
	})
}
