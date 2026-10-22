package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/goals"
	"gokin/internal/tools"
)

func (a *App) evaluateGoal(ctx context.Context, goal *goals.Goal, history []chat.SerializedContent) (goals.Verdict, int, int, error) {
	var verdict goals.Verdict
	if err := a.goalPolicyError(); err != nil {
		return verdict, 0, 0, err
	}
	base := client.RequestSettingsBase(a.GetMainClient())
	isolated, ok := client.CloneForAuxiliary(base)
	if !ok {
		return verdict, 0, 0, fmt.Errorf("goal evaluator requires an isolated tool-free client")
	}
	if err := client.AuxiliaryModelError(isolated); err != nil {
		return verdict, 0, 0, err
	}
	provider := ""
	if identified, ok := isolated.(client.ProviderIdentity); ok {
		provider = identified.GetProvider()
	}
	isolated.SetTools(nil)
	isolated.SetSystemInstruction(goals.EvaluatorInstruction)
	isolated.SetTurnContext("")
	isolated = client.WithRequestGuard(isolated, func(context.Context) error { return a.goalPolicyError() })
	budget := config.DefaultModelRoundTimeout
	if a.executor != nil {
		budget = a.executor.ModelRoundTimeout()
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	prompt, err := goals.EvaluatorPrompt(goal, history, a.snapshotResponseEvidence().FormatForCompletionReview())
	if err != nil {
		return verdict, 0, 0, err
	}
	model := isolated.GetModel()
	tracked := provider == "ollama" || appcontext.HasKnownPricing(model)
	ledger, budgeted := tools.InvocationBudgetLedgerFromContext(ctx)
	if budgeted {
		if !tracked {
			return verdict, 0, 0, ledger.MarkCostUnavailable(provider, model)
		}
		if err := ledger.BeginRound(ctx); err != nil {
			return verdict, 0, 0, err
		}
		defer ledger.EndRound()
	}
	if !tools.ClaimInvocationModelTurn(ctx) {
		return verdict, 0, 0, tools.ErrMaxTurnsExceeded
	}
	ctx = tools.ContextWithClaimedModelTurn(ctx)
	stream, err := isolated.SendMessage(ctx, prompt)
	if err != nil {
		return verdict, 0, 0, err
	}
	if stream == nil {
		return verdict, 0, 0, fmt.Errorf("goal evaluator returned no response")
	}
	response, streamErr := stream.CollectContext(ctx)
	if response == nil {
		return verdict, 0, 0, streamErr
	}
	// An explicitly configured fallback may serve this request on another
	// provider/model. Price the actual selection after the response too.
	if identified, ok := isolated.(client.ProviderIdentity); ok {
		provider = identified.GetProvider()
	}
	model = isolated.GetModel()
	tracked = provider == "ollama" || appcontext.HasKnownPricing(model)
	input, output := response.TotalInputTokens(), max(response.OutputTokens, 0)
	if input == 0 {
		input = max(1, utf8.RuneCountInString(prompt)/4)
	}
	if output == 0 && response.Text != "" {
		output = max(1, utf8.RuneCountInString(response.Text)/4)
	}
	cost := 0.0
	if tracked && provider != "ollama" {
		cost = appcontext.NewTokenCounter(nil, model, nil).CalculateCostWithCache(input, output, response.CacheReadInputTokens)
	}
	usage := turnUsageAccumulator{}
	usage.add(response.TotalInputTokens(), response.OutputTokens, response.CacheCreationInputTokens, response.CacheReadInputTokens, input, response.Text, cost, tracked)
	usage.modelRounds = 1
	usage.commit(a)
	if budgeted {
		if !tracked {
			return verdict, input, output, ledger.MarkCostUnavailable(provider, model)
		}
		limit, spent := ledger.AddSpend(cost)
		if spent > limit {
			return verdict, input, output, &tools.BudgetExceededError{LimitUSD: limit, SpentUSD: spent}
		}
	}
	if streamErr != nil {
		return verdict, input, output, streamErr
	}
	if len(response.FunctionCalls) > 0 {
		return verdict, input, output, fmt.Errorf("goal evaluator returned tool calls instead of a verdict")
	}
	verdict, err = goals.ParseVerdict(strings.TrimSpace(response.Text))
	return verdict, input, output, err
}
