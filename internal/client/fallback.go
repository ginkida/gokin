package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"gokin/internal/config"
	"gokin/internal/logging"

	"google.golang.org/genai"
)

// FallbackClient wraps multiple Client instances and tries each in order
// on failure, providing automatic failover between providers.
type FallbackClient struct {
	clients   []Client
	providers []string
	current   int
	mu        sync.RWMutex
}

// NewFallbackClient creates a new FallbackClient with the given clients.
// At least one client must be provided.
func NewFallbackClient(clients []Client, providers []string) (*FallbackClient, error) {
	if len(clients) == 0 {
		return nil, fmt.Errorf("fallback client requires at least one client")
	}
	ownedClients := make([]Client, len(clients))
	for i, client := range clients {
		if isNilClient(client) {
			return nil, fmt.Errorf("fallback client at index %d is nil", i)
		}
		ownedClients[i] = client
	}
	normalizedProviders := make([]string, len(clients))
	if len(providers) != len(clients) {
		providers = nil
	}
	for i := range normalizedProviders {
		if i < len(providers) {
			normalizedProviders[i] = strings.ToLower(strings.TrimSpace(providers[i]))
		}
	}
	return &FallbackClient{
		clients:   ownedClients,
		providers: normalizedProviders,
		current:   0,
	}, nil
}

func (fc *FallbackClient) providerAt(index int) string {
	if index < 0 || index >= len(fc.providers) {
		return ""
	}
	return fc.providers[index]
}

// getCurrent returns the current active client index.
func (fc *FallbackClient) getCurrent() int {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.current
}

// resetCurrent resets back to the first client.
func (fc *FallbackClient) resetCurrent() {
	fc.setCurrent(0)
}

func (fc *FallbackClient) setCurrent(index int) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.current = index
}

// ResetFallbackPosition resets the fallback chain to start from the first provider.
// This allows retrying the full chain after a delay (e.g., after stream-level retries exhaust).
func (fc *FallbackClient) ResetFallbackPosition() {
	fc.resetCurrent()
}

// SendMessage sends a message, trying fallback clients on error.
func (fc *FallbackClient) SendMessage(ctx context.Context, message string) (*StreamingResponse, error) {
	return fc.sendWithFallback(ctx, "SendMessage", func(c Client) (*StreamingResponse, error) {
		return c.SendMessage(ctx, message)
	})
}

// SendMessageWithHistory sends a message with history, trying fallback clients on error.
func (fc *FallbackClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*StreamingResponse, error) {
	return fc.sendWithFallback(ctx, "SendMessageWithHistory", func(c Client) (*StreamingResponse, error) {
		return c.SendMessageWithHistory(ctx, history, message)
	})
}

// SendFunctionResponse sends function results, trying fallback clients on error.
func (fc *FallbackClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*StreamingResponse, error) {
	return fc.sendWithFallback(ctx, "SendFunctionResponse", func(c Client) (*StreamingResponse, error) {
		return c.SendFunctionResponse(ctx, history, results)
	})
}

type fallbackSend func(Client) (*StreamingResponse, error)

// sendWithFallback handles both request-time failures and errors delivered by
// a provider after the HTTP request has already upgraded to an SSE stream.
// A stream is only committed once meaningful assistant content has been
// forwarded; before that point it is safe to transparently switch providers.
func (fc *FallbackClient) sendWithFallback(ctx context.Context, operation string, send fallbackSend) (*StreamingResponse, error) {
	startIdx := fc.getCurrent()
	order := fallbackClientOrder(startIdx, len(fc.clients))
	var lastErr error
	for position, i := range order {
		if ctx.Err() != nil {
			return nil, ContextErr(ctx)
		}
		resp, err := send(fc.clients[i])
		if err == nil && resp == nil {
			err = fmt.Errorf("provider returned a nil streaming response")
		}
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ContextErr(ctx)
			}
			fc.recordFailure(operation, i, err)
			if !shouldFallbackToNextProvider(err) {
				return nil, err
			}
			continue
		}

		fc.setCurrent(i)
		return fc.proxyStream(ctx, operation, order, position, resp, send), nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("all fallback clients failed, last error: %w", lastErr)
	}
	return nil, fmt.Errorf("all fallback clients exhausted")
}

// fallbackClientOrder returns every client exactly once, starting at the
// current provider and wrapping around. Without the wrap, selecting a provider
// at index N permanently discarded the healthy providers before it.
func fallbackClientOrder(start, count int) []int {
	if count <= 0 {
		return nil
	}
	if start < 0 || start >= count {
		start = 0
	}
	order := make([]int, count)
	for offset := range count {
		order[offset] = (start + offset) % count
	}
	return order
}

func (fc *FallbackClient) recordFailure(operation string, index int, err error) {
	// Request-shape/safety failures describe this payload, not backend health;
	// cancellation describes caller intent. Penalizing either would persistently
	// demote a healthy provider for future unrelated sessions. Provider-local
	// auth/quota/plan failures and transient infrastructure failures remain real
	// provider failures and are scored normally.
	if shouldFallbackToNextProvider(err) {
		recordProviderFailure(fc.providerAt(index), IsRetryableError(err))
	}
	logging.Warn("client failed in "+operation,
		"index", index,
		"provider", fc.providerAt(index),
		"model", fc.clients[index].GetModel(),
		"error", err.Error())
}

// shouldFallbackToNextProvider separates failures that another configured
// backend can reasonably recover from from failures in the request itself.
//
// Provider-local account/auth/quota/model availability errors may be terminal
// for one backend but are exactly what an opt-in fallback chain is for. In
// contrast, malformed request parameters and safety/content refusals describe
// the payload being sent; silently resubmitting that payload to another backend
// can hide an actionable bug or bypass a provider safety decision.
func shouldFallbackToNextProvider(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}

	var terminal *TerminalProviderError
	if errors.As(err, &terminal) {
		if terminal == nil {
			return false
		}
		switch strings.TrimSpace(terminal.Code) {
		case "1210", "1211", "1212", "1213", "1214", "1215", "1301":
			return false
		default:
			// Auth, account, quota, subscription, and plan failures are local
			// to this provider and remain eligible for an explicit fallback.
			return true
		}
	}

	var httpErr *HTTPError
	hasHTTPError := errors.As(err, &httpErr)
	if hasHTTPError && httpErr == nil {
		return false
	}
	var apiErr *APIError
	hasAPIError := errors.As(err, &apiErr)
	if hasAPIError && apiErr == nil {
		return false
	}

	// Preserve explicitly typed API exceptions before applying the generic 4xx
	// request-shape rule. MiniMax, for example, can return a temporary
	// model_not_found as HTTP 400; the API status classifier knows that narrow
	// exception and a healthy fallback provider may serve it immediately. Do not
	// use the broader IsRetryableError here: its last-resort string heuristics
	// (such as "EOF") must not override a typed bad-request status.
	if IsRetryableAPIError(err) {
		return true
	}

	if hasHTTPError && isRequestShapeHTTPStatus(httpErr.StatusCode) {
		return false
	}
	if hasAPIError && isRequestShapeHTTPStatus(apiErr.StatusCode) {
		return false
	}
	return true
}

func isRequestShapeHTTPStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func (fc *FallbackClient) proxyStream(
	ctx context.Context,
	operation string,
	order []int,
	startPosition int,
	first *StreamingResponse,
	send fallbackSend,
) *StreamingResponse {
	chunks := make(chan ResponseChunk, 16)
	done := make(chan struct{})

	go func() {
		defer close(chunks)
		defer close(done)

		position := startPosition
		index := order[position]
		stream := first
		for {
			committed, err := forwardFallbackCandidate(ctx, stream, chunks)
			if err == nil {
				fc.setCurrent(index)
				recordProviderSuccess(fc.providerAt(index))
				return
			}

			if ctx.Err() != nil {
				emitFallbackError(ctx, chunks, ContextErr(ctx))
				return
			}
			fc.recordFailure(operation+" stream", index, err)
			if committed || !shouldFallbackToNextProvider(err) {
				emitFallbackError(ctx, chunks, err)
				return
			}

			var next *StreamingResponse
			for position++; position < len(order); position++ {
				index = order[position]
				if ctx.Err() != nil {
					emitFallbackError(ctx, chunks, ContextErr(ctx))
					return
				}
				candidate, sendErr := send(fc.clients[index])
				if sendErr == nil && candidate == nil {
					sendErr = fmt.Errorf("provider returned a nil streaming response")
				}
				if sendErr != nil {
					if ctx.Err() != nil {
						emitFallbackError(ctx, chunks, ContextErr(ctx))
						return
					}
					fc.recordFailure(operation, index, sendErr)
					err = sendErr
					if !shouldFallbackToNextProvider(sendErr) {
						emitFallbackError(ctx, chunks, sendErr)
						return
					}
					continue
				}
				next = candidate
				fc.setCurrent(index)
				break
			}

			if next == nil {
				emitFallbackError(ctx, chunks, fmt.Errorf("all fallback clients failed, last error: %w", err))
				return
			}
			stream = next
		}
	}()

	return &StreamingResponse{Chunks: chunks, Done: done}
}

// forwardFallbackCandidate forwards one provider stream. Non-content metadata
// is held until the first meaningful chunk, so an early SSE error can be
// discarded cleanly when the next provider is tried.
func forwardFallbackCandidate(ctx context.Context, stream *StreamingResponse, out chan<- ResponseChunk) (committed bool, err error) {
	if stream == nil {
		return false, fmt.Errorf("provider returned a nil streaming response")
	}
	if stream.Chunks == nil {
		return false, fmt.Errorf("provider returned a streaming response with nil chunks")
	}

	pending := make([]ResponseChunk, 0, 2)
	for {
		select {
		case <-ctx.Done():
			return committed, ContextErr(ctx)
		case chunk, ok := <-stream.Chunks:
			if !ok {
				if ctxErr := ContextErr(ctx); ctxErr != nil {
					return committed, ctxErr
				}
				if !committed {
					// Wrap the empty-response sentinel so a candidate that streams
					// nothing still counts as retryable if the caller falls
					// through the whole fallback chain (v0.100.90): a plain error
					// here silently defeated the executor/agent's dedicated
					// empty-response retry-with-backoff once every provider in
					// the chain exhausted this way — the failure mode already has
					// a graceful, well-tested recovery path when NO fallback is
					// configured; enabling fallback must not regress it.
					return false, fmt.Errorf("provider stream ended without assistant content: %w", ErrEmptyModelResponse)
				}
				return committed, nil
			}
			if chunk.Error != nil {
				return committed, chunk.Error
			}

			meaningful := chunk.Text != "" || chunk.Thinking != "" ||
				len(chunk.FunctionCalls) > 0 || len(chunk.Parts) > 0
			if chunk.Done && !committed && !meaningful {
				return false, fmt.Errorf("provider stream completed without assistant content: %w", ErrEmptyModelResponse)
			}
			if !committed && !meaningful && !chunk.Done {
				pending = append(pending, chunk)
				continue
			}
			if !committed {
				if err := forwardFallbackChunks(ctx, out, pending); err != nil {
					return false, err
				}
				pending = nil
				committed = meaningful
			}
			if err := forwardFallbackChunk(ctx, out, chunk); err != nil {
				return committed, err
			}
			if chunk.Done {
				return committed, nil
			}
		}
	}
}

func forwardFallbackChunks(ctx context.Context, out chan<- ResponseChunk, chunks []ResponseChunk) error {
	for _, chunk := range chunks {
		if err := forwardFallbackChunk(ctx, out, chunk); err != nil {
			return err
		}
	}
	return nil
}

func forwardFallbackChunk(ctx context.Context, out chan<- ResponseChunk, chunk ResponseChunk) error {
	select {
	case <-ctx.Done():
		return ContextErr(ctx)
	case out <- chunk:
		return nil
	}
}

func emitFallbackError(ctx context.Context, out chan<- ResponseChunk, err error) {
	if err == nil {
		return
	}
	select {
	case out <- ResponseChunk{Error: err, Done: true}:
	case <-ctx.Done():
	}
}

// SetSystemInstruction sets the system-level instruction on ALL clients in the fallback chain.
func (fc *FallbackClient) SetSystemInstruction(instruction string) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	for _, c := range fc.clients {
		c.SetSystemInstruction(instruction)
	}
}

// SetTurnContext sets the per-turn ephemeral context on ALL clients in the
// fallback chain (same fan-out contract as SetSystemInstruction — a failover
// mid-turn must not lose the working-memory snapshot).
func (fc *FallbackClient) SetTurnContext(turnContext string) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	for _, c := range fc.clients {
		c.SetTurnContext(turnContext)
	}
}

// SetThinkingBudget sets thinking budget on ALL clients in the fallback chain.
func (fc *FallbackClient) SetThinkingBudget(budget int32) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	for _, c := range fc.clients {
		c.SetThinkingBudget(budget)
	}
}

// SetTools sets tools on ALL clients in the fallback chain.
// Each client gets its own copy of the slice to prevent cross-client mutation.
func (fc *FallbackClient) SetTools(tools []*genai.Tool) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	for _, c := range fc.clients {
		clone := make([]*genai.Tool, len(tools))
		copy(clone, tools)
		c.SetTools(clone)
	}
}

// SetRateLimiter sets the rate limiter on ALL clients in the fallback chain.
func (fc *FallbackClient) SetRateLimiter(limiter any) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	for _, c := range fc.clients {
		c.SetRateLimiter(limiter)
	}
}

// CountTokens counts tokens using the current active client.
func (fc *FallbackClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	idx := fc.getCurrent()
	return fc.clients[idx].CountTokens(ctx, contents)
}

// CountTokensWithAccuracy forwards per-call accuracy when supported.
func (fc *FallbackClient) CountTokensWithAccuracy(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, bool, error) {
	idx := fc.getCurrent()
	active := fc.clients[idx]
	if detailed, ok := active.(TokenCountWithAccuracy); ok {
		return detailed.CountTokensWithAccuracy(ctx, contents)
	}
	resp, err := active.CountTokens(ctx, contents)
	accuracy, ok := active.(TokenCountAccuracy)
	return resp, ok && accuracy.TokenCountIsEstimate(), err
}

// TokenCountIsEstimate forwards the optional accuracy capability to the active
// client in the fallback chain.
func (fc *FallbackClient) TokenCountIsEstimate() bool {
	idx := fc.getCurrent()
	accuracy, ok := fc.clients[idx].(TokenCountAccuracy)
	return ok && accuracy.TokenCountIsEstimate()
}

// TokenCountCacheKey forwards request-prefix cache state from the active client.
func (fc *FallbackClient) TokenCountCacheKey() string {
	idx := fc.getCurrent()
	if keyer, ok := fc.clients[idx].(TokenCountCacheKey); ok {
		return keyer.TokenCountCacheKey()
	}
	return fc.clients[idx].GetModel()
}

// GetModel returns the current active client's model name.
func (fc *FallbackClient) GetModel() string {
	idx := fc.getCurrent()
	return fc.clients[idx].GetModel()
}

// GetProvider returns the backend that served the current successful request.
func (fc *FallbackClient) GetProvider() string {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.providerAt(fc.current)
}

// SetModel changes the model on the current active client.
func (fc *FallbackClient) SetModel(modelName string) {
	idx := fc.getCurrent()
	fc.clients[idx].SetModel(modelName)
}

// WithModel returns a new FallbackClient with the provider matching the model
// configured for it. Other providers retain their own model names; sending a
// GLM model ID to Kimi or MiniMax makes a nominal fallback fail deterministically.
func (fc *FallbackClient) WithModel(modelName string) Client {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	targetIndex := fc.current
	// WithModel(GetModel()) is used to clone a client for a new session. An
	// explicitly configured provider remains authoritative even when its custom
	// model name happens to look like another provider (for example qwen-* on a
	// GLM-compatible gateway).
	currentModel := fc.clients[fc.current].GetModel()
	if modelName != currentModel {
		targetProvider := config.DetectKnownProviderFromModel(modelName)
		for i := range fc.clients {
			if targetProvider != "" && fc.providerAt(i) == targetProvider {
				targetIndex = i
				break
			}
		}
	}
	newClients := make([]Client, len(fc.clients))
	for i, c := range fc.clients {
		model := c.GetModel()
		if i == targetIndex {
			model = modelName
		}
		newClients[i] = c.WithModel(model)
	}
	newProviders := make([]string, len(fc.providers))
	copy(newProviders, fc.providers)
	fb, err := NewFallbackClient(newClients, newProviders)
	if err != nil {
		logging.Debug("FallbackClient.WithModel: NewFallbackClient failed", "error", err)
		return fc.clients[fc.current].WithModel(modelName)
	}
	fb.setCurrent(targetIndex)
	return fb
}

// GetRawClient returns the current active client's raw client.
func (fc *FallbackClient) GetRawClient() any {
	idx := fc.getCurrent()
	return fc.clients[idx].GetRawClient()
}

// Close closes ALL clients in the fallback chain.
func (fc *FallbackClient) Close() error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	var lastErr error
	for _, c := range fc.clients {
		if err := c.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// SetStatusCallback sets status callbacks on all clients that support it.
func (fc *FallbackClient) SetStatusCallback(cb StatusCallback) {
	if cb == nil {
		return
	}

	fc.mu.RLock()
	defer fc.mu.RUnlock()
	for _, c := range fc.clients {
		if setter, ok := c.(interface{ SetStatusCallback(StatusCallback) }); ok {
			setter.SetStatusCallback(cb)
		}
	}
}
