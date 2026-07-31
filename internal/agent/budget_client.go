package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"gokin/internal/client"
	appcontext "gokin/internal/context"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// invocationCostCalculator prices one provider response. The boolean is false
// when a hard ceiling cannot be proven for the provider/model combination.
type invocationCostCalculator func(
	provider, model string,
	inputTokens, outputTokens, cacheReadTokens int,
) (float64, bool)

// invocationBudgetClient meters every generative request made by an Agent,
// including summarization, semantic reflection, and tree planning. All wrappers
// inheriting the same invocation context serialize through the ledger itself.
type invocationBudgetClient struct {
	base       client.Client
	calculator invocationCostCalculator

	mu          sync.Mutex
	localCost   float64
	localRounds int
	allTracked  bool
}

func newInvocationBudgetClient(base client.Client) client.Client {
	return newInvocationBudgetClientWithCalculator(base, defaultInvocationCostCalculator)
}

func newInvocationBudgetClientWithCalculator(
	base client.Client,
	calculator invocationCostCalculator,
) *invocationBudgetClient {
	return &invocationBudgetClient{
		base:       base,
		calculator: calculator,
		allTracked: true,
	}
}

func defaultInvocationCostCalculator(
	provider, model string,
	inputTokens, outputTokens, cacheReadTokens int,
) (float64, bool) {
	if strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return 0, true
	}
	if !appcontext.HasKnownPricing(model) {
		return 0, false
	}
	counter := appcontext.NewTokenCounter(nil, model, nil)
	return counter.CalculateCostWithCache(
		max(inputTokens, 0),
		max(outputTokens, 0),
		max(cacheReadTokens, 0),
	), true
}

func (c *invocationBudgetClient) identity() (provider, model string) {
	if c == nil || c.base == nil {
		return "", ""
	}
	model = c.base.GetModel()
	if identified, ok := c.base.(client.ProviderIdentity); ok {
		provider = identified.GetProvider()
	}
	return provider, model
}

func (c *invocationBudgetClient) preflight(ctx context.Context) (*tools.InvocationBudgetLedger, error) {
	ledger, budgeted := tools.InvocationBudgetLedgerFromContext(ctx)
	if !budgeted {
		return nil, nil
	}
	provider, model := c.identity()
	if c.calculator == nil {
		return nil, ledger.MarkCostUnavailable(provider, model)
	}
	if _, tracked := c.calculator(provider, model, 0, 0, 0); !tracked {
		return nil, ledger.MarkCostUnavailable(provider, model)
	}
	if err := ledger.BeginRound(ctx); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (c *invocationBudgetClient) send(
	ctx context.Context,
	start func() (*client.StreamingResponse, error),
) (*client.StreamingResponse, error) {
	ledger, err := c.preflight(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := start()
	if err != nil {
		if ledger != nil {
			ledger.EndRound()
		}
		return nil, err
	}
	if ledger == nil {
		return stream, nil
	}
	if stream == nil {
		ledger.EndRound()
		return nil, nil
	}
	return c.wrapStream(ctx, ledger, stream), nil
}

func (c *invocationBudgetClient) wrapStream(
	ctx context.Context,
	ledger *tools.InvocationBudgetLedger,
	stream *client.StreamingResponse,
) *client.StreamingResponse {
	chunks := make(chan client.ResponseChunk, 64)
	done := make(chan struct{})

	go func() {
		defer close(chunks)
		defer close(done)

		inputTokens := 0
		outputTokens := 0
		cacheCreationTokens := 0
		cacheReadTokens := 0
		sawFunctionCalls := false
		leaseHeld := true
		finalized := false

		observe := func(chunk client.ResponseChunk) {
			if chunk.InputTokens > 0 {
				inputTokens = chunk.InputTokens
			}
			if chunk.OutputTokens > 0 {
				outputTokens = chunk.OutputTokens
			}
			if chunk.CacheCreationInputTokens > 0 {
				cacheCreationTokens = chunk.CacheCreationInputTokens
			}
			if chunk.CacheReadInputTokens > 0 {
				cacheReadTokens = chunk.CacheReadInputTokens
			}
			if len(chunk.FunctionCalls) > 0 {
				sawFunctionCalls = true
			}
		}
		finalize := func() error {
			if finalized {
				return nil
			}
			finalized = true
			provider, model := c.identity()
			cost, tracked := 0.0, false
			if c.calculator != nil {
				cost, tracked = c.calculator(
					provider, model,
					max(inputTokens, 0)+max(cacheCreationTokens, 0)+max(cacheReadTokens, 0),
					outputTokens,
					cacheReadTokens)
			}

			c.mu.Lock()
			c.localRounds++
			c.allTracked = c.allTracked && tracked
			if tracked && cost > 0 {
				c.localCost += cost
			}
			c.mu.Unlock()

			var terminal error
			if !tracked {
				terminal = ledger.MarkCostUnavailable(provider, model)
			} else {
				limit, spent := ledger.AddSpend(max(cost, 0))
				if spent > limit || (spent >= limit && sawFunctionCalls) {
					terminal = &tools.BudgetExceededError{
						LimitUSD: limit,
						SpentUSD: spent,
					}
				}
			}
			if leaseHeld {
				ledger.EndRound()
				leaseHeld = false
			}
			return terminal
		}
		defer func() {
			if leaseHeld {
				// A cancelled/broken stream still records any usage metadata
				// observed before it released the shared provider-round lease.
				_ = finalize()
			}
		}()

		emit := func(chunk client.ResponseChunk) bool {
			select {
			case chunks <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		hasPartialPayload := func(chunk client.ResponseChunk) bool {
			return chunk.Text != "" ||
				chunk.Thinking != "" ||
				len(chunk.FunctionCalls) > 0 ||
				len(chunk.Parts) > 0 ||
				chunk.InputTokens > 0 ||
				chunk.OutputTokens > 0 ||
				chunk.CacheCreationInputTokens > 0 ||
				chunk.CacheReadInputTokens > 0 ||
				chunk.RateLimit != nil
		}

		for chunk := range stream.Chunks {
			observe(chunk)
			if !chunk.Done && chunk.Error == nil {
				if !emit(chunk) {
					return
				}
				continue
			}

			budgetErr := finalize()
			if budgetErr == nil {
				_ = emit(chunk)
				return
			}

			// Preserve content and usage received in the provider's terminal
			// chunk before reporting the local hard-budget error. ProcessStream
			// returns as soon as it sees Error, so these must be separate chunks.
			partial := chunk
			partial.Done = false
			partial.Error = nil
			if hasPartialPayload(partial) && !emit(partial) {
				return
			}
			_ = emit(client.ResponseChunk{Error: budgetErr})
			return
		}

		// Some test/custom clients close Chunks without a terminal Done chunk.
		if budgetErr := finalize(); budgetErr != nil {
			_ = emit(client.ResponseChunk{Error: budgetErr})
		}
	}()

	return &client.StreamingResponse{Chunks: chunks, Done: done}
}

func (c *invocationBudgetClient) SendMessage(
	ctx context.Context,
	message string,
) (*client.StreamingResponse, error) {
	return c.send(ctx, func() (*client.StreamingResponse, error) {
		return c.base.SendMessage(ctx, message)
	})
}

func (c *invocationBudgetClient) SendMessageWithHistory(
	ctx context.Context,
	history []*genai.Content,
	message string,
) (*client.StreamingResponse, error) {
	return c.send(ctx, func() (*client.StreamingResponse, error) {
		return c.base.SendMessageWithHistory(ctx, history, message)
	})
}

func (c *invocationBudgetClient) SendFunctionResponse(
	ctx context.Context,
	history []*genai.Content,
	results []*genai.FunctionResponse,
) (*client.StreamingResponse, error) {
	return c.send(ctx, func() (*client.StreamingResponse, error) {
		return c.base.SendFunctionResponse(ctx, history, results)
	})
}

func (c *invocationBudgetClient) SetTools(value []*genai.Tool) {
	c.base.SetTools(value)
}

func (c *invocationBudgetClient) SetRateLimiter(value any) {
	c.base.SetRateLimiter(value)
}

func (c *invocationBudgetClient) CountTokens(
	ctx context.Context,
	contents []*genai.Content,
) (*genai.CountTokensResponse, error) {
	return c.base.CountTokens(ctx, contents)
}

func (c *invocationBudgetClient) CountTokensWithAccuracy(
	ctx context.Context,
	contents []*genai.Content,
) (*genai.CountTokensResponse, bool, error) {
	if detailed, ok := c.base.(client.TokenCountWithAccuracy); ok {
		return detailed.CountTokensWithAccuracy(ctx, contents)
	}
	response, err := c.base.CountTokens(ctx, contents)
	accuracy, ok := c.base.(client.TokenCountAccuracy)
	return response, ok && accuracy.TokenCountIsEstimate(), err
}

func (c *invocationBudgetClient) TokenCountIsEstimate() bool {
	accuracy, ok := c.base.(client.TokenCountAccuracy)
	return ok && accuracy.TokenCountIsEstimate()
}

func (c *invocationBudgetClient) TokenCountCacheKey() string {
	if keyer, ok := c.base.(client.TokenCountCacheKey); ok {
		return keyer.TokenCountCacheKey()
	}
	return c.base.GetModel()
}

func (c *invocationBudgetClient) GetProvider() string {
	provider, _ := c.identity()
	return provider
}

func (c *invocationBudgetClient) NeedsToolCallFallback() bool {
	fallback, ok := c.base.(interface{ NeedsToolCallFallback() bool })
	return ok && fallback.NeedsToolCallFallback()
}

func (c *invocationBudgetClient) GetModel() string {
	return c.base.GetModel()
}

func (c *invocationBudgetClient) SetModel(model string) {
	c.base.SetModel(model)
}

func (c *invocationBudgetClient) WithModel(model string) client.Client {
	return newInvocationBudgetClientWithCalculator(c.base.WithModel(model), c.calculator)
}

func (c *invocationBudgetClient) GetRawClient() any {
	return c.base.GetRawClient()
}

func (c *invocationBudgetClient) SetSystemInstruction(instruction string) {
	c.base.SetSystemInstruction(instruction)
}

func (c *invocationBudgetClient) SetTurnContext(value string) {
	c.base.SetTurnContext(value)
}

func (c *invocationBudgetClient) SetThinkingBudget(value int32) {
	c.base.SetThinkingBudget(value)
}

func (c *invocationBudgetClient) Close() error {
	return c.base.Close()
}

func invocationBudgetTerminalError(ctx context.Context) error {
	ledger, ok := tools.InvocationBudgetLedgerFromContext(ctx)
	if !ok {
		return nil
	}
	return ledger.TerminalError()
}

// finishInvocationBudgetFailure closes any pending tool-use protocol pair and
// records an honest local terminal model message. The provider has already
// billed the response, but none of its requested tools are allowed to start.
func (a *Agent) finishInvocationBudgetFailure(
	resp *client.Response,
	output *strings.Builder,
	cause error,
) ([]*genai.Content, string, error) {
	if output == nil {
		output = &strings.Builder{}
	}

	var additions []*genai.Content
	if resp != nil {
		if resp.Text != "" {
			output.WriteString(resp.Text)
		}
		if parts := a.buildResponseParts(resp); len(parts) > 0 {
			additions = append(additions, &genai.Content{
				Role:  genai.RoleModel,
				Parts: parts,
			})
		}
		if len(resp.FunctionCalls) > 0 {
			parts := make([]*genai.Part, 0, len(resp.FunctionCalls))
			for _, call := range resp.FunctionCalls {
				if call == nil {
					continue
				}
				part := genai.NewPartFromFunctionResponse(
					call.Name,
					tools.NewErrorResult(
						"not executed: the shared invocation cost budget was reached before this tool call could run",
					).ToMap(),
				)
				part.FunctionResponse.ID = call.ID
				parts = append(parts, part)
			}
			if len(parts) > 0 {
				additions = append(additions, &genai.Content{
					Role:  genai.RoleUser,
					Parts: parts,
				})
			}
		}
	}

	marker := ""
	switch {
	case errors.Is(cause, tools.ErrCostUnavailable):
		marker = "\n\n⚠ " + cause.Error() +
			" — no further model or tool work was performed."
	default:
		var exceeded *tools.BudgetExceededError
		if errors.As(cause, &exceeded) {
			marker = fmt.Sprintf(
				"\n\n⚠ Reached the shared invocation cost budget ($%.6f limit; $%.6f recorded) — this work is INCOMPLETE.",
				exceeded.LimitUSD,
				exceeded.SpentUSD,
			)
		} else {
			marker = "\n\n⚠ Reached the shared invocation cost budget — this work is INCOMPLETE."
		}
	}
	output.WriteString(marker)
	additions = append(additions, genai.NewContentFromText(
		strings.TrimSpace(marker), genai.RoleModel))

	a.stateMu.Lock()
	a.history = append(a.history, additions...)
	history := append([]*genai.Content(nil), a.history...)
	a.stateMu.Unlock()
	a.safeOnText(marker)
	return history, output.String(), cause
}

func budgetSkippedToolCallResult(call *genai.FunctionCall, cause error) toolCallResult {
	reason := "not executed: the shared invocation cost budget is exhausted"
	if errors.Is(cause, tools.ErrCostUnavailable) {
		reason = "not executed: provider pricing is unavailable, so the shared invocation cost budget cannot be enforced safely"
	}
	return toolCallResult{
		Response: &genai.FunctionResponse{
			ID:       call.ID,
			Name:     call.Name,
			Response: tools.NewErrorResult(reason).ToMap(),
		},
	}
}
