package client

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"google.golang.org/genai"
)

// Default base URLs for API providers.
// DefaultAnthropicBaseURL is the fallback endpoint inside AnthropicClient when
// a provider doesn't set its own BaseURL. The anthropic provider itself has
// been removed from Providers/factory in v0.65.0, but GLM/Kimi/MiniMax still
// route through AnthropicClient in compat mode with explicit BaseURL values
// that override this constant, so keeping it here avoids accidental requests
// against api.anthropic.com.
const (
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultGLMBaseURL       = "https://api.z.ai/api/anthropic"
	DefaultMiniMaxBaseURL   = "https://api.minimax.io/anthropic"
	// DefaultKimiBaseURL points at Kimi Coding Plan's Anthropic-compatible
	// endpoint — the subscription tier required for kimi-k2.6 and coding-
	// optimized inference. Keys for this endpoint are `sk-kimi-...` (distinct
	// from the Moonshot Developer Platform keys). Users who explicitly want
	// the Moonshot Developer API can override via `model.custom_base_url:
	// https://api.moonshot.ai/anthropic` with their Moonshot key.
	DefaultKimiBaseURL = "https://api.kimi.com/coding"
	// DefaultMoonshotBaseURL is the alternative non-subscription endpoint
	// (Developer Platform). Retained for reference and for users who opt in
	// via custom_base_url.
	DefaultMoonshotBaseURL = "https://api.moonshot.ai/anthropic"
	// DefaultDeepSeekBaseURL points at DeepSeek's Anthropic-compatible
	// endpoint. The root https://api.deepseek.com serves the OpenAI-compat
	// API (chat/completions); `/anthropic` returns /v1/messages with the
	// Claude wire format our AnthropicClient understands. DeepSeek V4
	// (flash + pro) is served here alongside the legacy deepseek-chat
	// and deepseek-reasoner endpoints — the latter two deprecate
	// 2026-07-24 but remain callable until then.
	DefaultDeepSeekBaseURL = "https://api.deepseek.com/anthropic"
)

// ModelInfo contains information about an available model.
type ModelInfo struct {
	ID          string // Model identifier (e.g., "glm-5.2", "deepseek-v4-pro")
	Name        string // Human-readable name
	Description string // Short description
	Provider    string // Provider: "glm", "kimi", "minimax", "deepseek", or "ollama"
	BaseURL     string // Custom base URL for Anthropic-compatible APIs
}

// AvailableModels is the list of supported models across all providers.
//
// Gemini was removed in v0.65 — the provider isn't in config.Providers and
// factory.go has no "gemini" case, so any Gemini ID listed here would show
// up in /model output but fail to switch (autoDetectClient fallback can't
// route it). Five legacy entries were removed in v0.78.29.
var AvailableModels = []ModelInfo{
	// GLM models (via Anthropic-compatible API on Z.AI)
	{
		ID:          "glm-5.2",
		Name:        "GLM-5.2",
		Description: "Most capable GLM model — 1M context, 131K output (Coding Plan)",
		Provider:    "glm",
		BaseURL:     DefaultGLMBaseURL,
	},
	{
		ID:          "glm-5.1",
		Name:        "GLM-5.1",
		Description: "Previous flagship GLM model (Coding Plan)",
		Provider:    "glm",
		BaseURL:     DefaultGLMBaseURL,
	},
	{
		ID:          "glm-5",
		Name:        "GLM-5",
		Description: "Flagship GLM model (Coding Plan)",
		Provider:    "glm",
		BaseURL:     DefaultGLMBaseURL,
	},
	{
		ID:          "glm-5-turbo",
		Name:        "GLM-5 Turbo",
		Description: "Fast GLM-5 variant — lower latency (Coding Plan)",
		Provider:    "glm",
		BaseURL:     DefaultGLMBaseURL,
	},
	{
		ID:          "glm-4.7",
		Name:        "GLM-4.7",
		Description: "Coding assistant: 131K max output (Coding Plan)",
		Provider:    "glm",
		BaseURL:     DefaultGLMBaseURL,
	},
	{
		ID:          "glm-4.5",
		Name:        "GLM-4.5",
		Description: "Balanced GLM model — good quality/speed (Coding Plan)",
		Provider:    "glm",
		BaseURL:     DefaultGLMBaseURL,
	},
	// MiniMax models (via Anthropic-compatible API)
	{
		ID:          "MiniMax-M2.7",
		Name:        "MiniMax M2.7",
		Description: "Recursive self-improvement, top real-world engineering",
		Provider:    "minimax",
		BaseURL:     DefaultMiniMaxBaseURL,
	},
	{
		ID:          "MiniMax-M2.7-highspeed",
		Name:        "MiniMax M2.7 Highspeed",
		Description: "Same as M2.7, faster inference, low latency",
		Provider:    "minimax",
		BaseURL:     DefaultMiniMaxBaseURL,
	},
	{
		ID:          "MiniMax-M2.5",
		Name:        "MiniMax M2.5",
		Description: "Peak performance, 200K context (~60 tps)",
		Provider:    "minimax",
		BaseURL:     DefaultMiniMaxBaseURL,
	},
	{
		ID:          "MiniMax-M2.5-highspeed",
		Name:        "MiniMax M2.5 Highspeed",
		Description: "Same performance, faster (~100 tps)",
		Provider:    "minimax",
		BaseURL:     DefaultMiniMaxBaseURL,
	},
	// Kimi Coding Plan — exposes exactly one model, `kimi-for-coding`
	// (internal name: Kimi K2.6). 262K context, reasoning+vision+video.
	// No other model IDs are served by api.kimi.com/coding/v1/models;
	// previous kimi-k2.5 / kimi-k2-thinking-turbo / kimi-k2-turbo-preview
	// entries were dead (Moonshot Developer API only) and have been removed.
	{
		ID:          "kimi-for-coding",
		Name:        "Kimi K2.6 (Coding Plan)",
		Description: "Flagship — 262K context, reasoning + vision + video, coding-tuned",
		Provider:    "kimi",
		BaseURL:     DefaultKimiBaseURL,
	},
	// DeepSeek — V4 generation. flash = latency/cost-optimised, pro =
	// top-tier reasoning. deepseek-chat and deepseek-reasoner are kept
	// for users on pre-deprecation plans (retire 2026-07-24) but are
	// not the default.
	{
		ID:          "deepseek-v4-pro",
		Name:        "DeepSeek V4 Pro",
		Description: "Flagship reasoning — top SWE-bench, Anthropic-compat API",
		Provider:    "deepseek",
		BaseURL:     DefaultDeepSeekBaseURL,
	},
	{
		ID:          "deepseek-v4-flash",
		Name:        "DeepSeek V4 Flash",
		Description: "Fast & cheap V4 variant — lower latency, same wire format",
		Provider:    "deepseek",
		BaseURL:     DefaultDeepSeekBaseURL,
	},
	{
		ID:          "deepseek-chat",
		Name:        "DeepSeek Chat",
		Description: "Legacy chat model — deprecates 2026-07-24",
		Provider:    "deepseek",
		BaseURL:     DefaultDeepSeekBaseURL,
	},
	{
		ID:          "deepseek-reasoner",
		Name:        "DeepSeek Reasoner",
		Description: "Legacy reasoning model — deprecates 2026-07-24",
		Provider:    "deepseek",
		BaseURL:     DefaultDeepSeekBaseURL,
	},
	// Ollama (local models - use exact name from 'ollama list')
	{
		ID:          "ollama",
		Name:        "Ollama (Local)",
		Description: "Local LLM. Use --model <name> from 'ollama list'",
		Provider:    "ollama",
	},
}

// GetModelsForProvider returns models filtered by provider.
func GetModelsForProvider(provider string) []ModelInfo {
	var models []ModelInfo
	for _, m := range AvailableModels {
		if m.Provider == provider {
			models = append(models, m)
		}
	}
	return models
}

// IsValidModel checks if a model ID is valid.
func IsValidModel(modelID string) bool {
	for _, m := range AvailableModels {
		if m.ID == modelID {
			return true
		}
	}
	return false
}

// GetModelInfo returns information about a specific model.
func GetModelInfo(modelID string) (ModelInfo, bool) {
	for _, m := range AvailableModels {
		if m.ID == modelID {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// Client defines the interface for AI model interactions.
type Client interface {
	// SendMessage sends a message and returns a streaming response.
	SendMessage(ctx context.Context, message string) (*StreamingResponse, error)

	// SendMessageWithHistory sends a message with conversation history.
	SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*StreamingResponse, error)

	// SendFunctionResponse sends function call results back to the model.
	SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*StreamingResponse, error)

	// SetTools sets the tools available for the model to use.
	SetTools(tools []*genai.Tool)

	// SetRateLimiter sets the rate limiter for API calls.
	SetRateLimiter(limiter any)

	// CountTokens counts tokens for the given contents.
	CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error)

	// GetModel returns the model name.
	GetModel() string

	// SetModel changes the model for this client.
	SetModel(modelName string)

	// WithModel returns a new client configured for the specified model.
	WithModel(modelName string) Client

	// GetRawClient returns the underlying client for direct API access.
	GetRawClient() any

	// SetSystemInstruction sets the system-level instruction for the model.
	// This is passed via the API's native system instruction parameter
	// rather than being injected as a user message in the conversation history.
	SetSystemInstruction(instruction string)

	// SetTurnContext sets the per-turn ephemeral context (e.g. working
	// memory). Unlike SetSystemInstruction it is delivered OUTSIDE the
	// cached prefix — appended at request-build time to the latest user
	// message and never persisted into history — so updating it every turn
	// does not invalidate prompt caching. Empty string clears it.
	SetTurnContext(turnContext string)

	// SetThinkingBudget configures the thinking/reasoning budget for the next request.
	// budget=0 disables thinking. Positive values set max thinking tokens.
	SetThinkingBudget(budget int32)

	// Close closes the client connection.
	Close() error
}

// isNilClient detects both a nil interface and an interface containing a typed
// nil pointer. The latter otherwise passes `client != nil` and panics only when
// a method is invoked later.
func isNilClient(client Client) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func sameClientInstance(left, right Client) bool {
	if isNilClient(left) || isNilClient(right) {
		return false
	}
	lv, rv := reflect.ValueOf(left), reflect.ValueOf(right)
	if lv.Type() != rv.Type() || lv.Kind() != reflect.Ptr {
		return false
	}
	return lv.Pointer() == rv.Pointer()
}

// sessionClientCloner creates an isolated mutable wrapper while retaining the
// provider transport owned by a pooled prototype. Client runtime state (tools,
// prompt, thinking budget, callbacks) must never be shared between App/session
// instances.
type sessionClientCloner interface {
	cloneForSession() Client
}

func cloneClientForSession(prototype Client) (Client, error) {
	if isNilClient(prototype) {
		return nil, fmt.Errorf("cannot clone nil client prototype")
	}
	cloner, ok := prototype.(sessionClientCloner)
	if !ok {
		return nil, fmt.Errorf("client type %T does not support isolated session clones", prototype)
	}
	clone := cloner.cloneForSession()
	if isNilClient(clone) {
		return nil, fmt.Errorf("client type %T returned a nil session clone", prototype)
	}
	return clone, nil
}

// TokenCountAccuracy is an optional capability for clients whose CountTokens
// implementation is an estimate rather than a provider-side tokenizer call.
// A nil error alone does not imply an exact count.
type TokenCountAccuracy interface {
	TokenCountIsEstimate() bool
}

// TokenCountWithAccuracy is an optional per-call variant used when accuracy
// can change at runtime (for example, native Anthropic falling back to a local
// estimate after its count endpoint fails).
type TokenCountWithAccuracy interface {
	CountTokensWithAccuracy(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, bool, error)
}

// TokenCountCacheKey is an optional capability that contributes client-side
// request state to token-count cache keys. System instructions and tool schemas
// consume context even though they are not present in contents.
type TokenCountCacheKey interface {
	TokenCountCacheKey() string
}

// ProviderIdentity is an optional capability for clients that know which
// backend served the current response. It is intentionally not part of Client
// so external/test implementations remain source-compatible.
type ProviderIdentity interface {
	GetProvider() string
}

// RateLimiter interface for rate limiting API calls (optional).
type RateLimiter interface {
	AcquireWithContext(ctx context.Context, tokens int64) error
	ReturnTokens(requests int, tokens int64)
	EstimateWaitTime(tokens int64) time.Duration
}

// RateLimitMetadata contains rate limit information from the API provider.
type RateLimitMetadata struct {
	RequestsLimit     int64
	RequestsRemaining int64
	RequestsReset     time.Duration
	TokensLimit       int64
	TokensRemaining   int64
	TokensReset       time.Duration
}

// StreamingResponse represents a streaming response from the model.
type StreamingResponse struct {
	// Chunks is a channel that receives response chunks.
	Chunks <-chan ResponseChunk

	// Done is closed when the response is complete.
	Done <-chan struct{}
}

// ResponseChunk represents a single chunk in a streaming response.
type ResponseChunk struct {
	// Text contains any text content in this chunk.
	Text string

	// Thinking contains extended thinking content (Anthropic API).
	Thinking string

	// FunctionCalls contains any function calls in this chunk.
	FunctionCalls []*genai.FunctionCall

	// Parts contains the original parts from the response (with ThoughtSignature).
	Parts []*genai.Part

	// Error contains any error that occurred.
	Error error

	// Done indicates if this is the final chunk.
	Done bool

	// FinishReason indicates why the response finished.
	FinishReason genai.FinishReason

	// InputTokens from API usage metadata (if available).
	InputTokens int

	// OutputTokens from API usage metadata (if available).
	OutputTokens int

	// CacheCreationInputTokens from prompt caching (Anthropic/MiniMax).
	CacheCreationInputTokens int

	// CacheReadInputTokens from prompt caching (Anthropic/MiniMax).
	CacheReadInputTokens int

	// RateLimit from provider (if available).
	RateLimit *RateLimitMetadata
}

// Response represents a complete response from the model.
type Response struct {
	// Text is the accumulated text response.
	Text string

	// Thinking is the accumulated extended thinking content (Anthropic API).
	Thinking string

	// FunctionCalls contains all function calls from the response.
	FunctionCalls []*genai.FunctionCall

	// Parts contains all original parts from the response (with ThoughtSignature).
	Parts []*genai.Part

	// FinishReason indicates why the response finished.
	FinishReason genai.FinishReason

	// InputTokens from API usage metadata (prompt tokens, if available).
	InputTokens int

	// OutputTokens from API usage metadata (completion tokens, if available).
	OutputTokens int

	// CacheCreationInputTokens from prompt caching (Anthropic/MiniMax).
	CacheCreationInputTokens int

	// CacheReadInputTokens from prompt caching (Anthropic/MiniMax).
	CacheReadInputTokens int

	// RateLimit from provider (if available).
	RateLimit *RateLimitMetadata
}

// Collect collects all chunks from a streaming response into a single
// Response. Parallels ProcessStream's accumulator — including the
// FunctionCall→Parts back-fill so callers can pass Response.Parts to
// subsequent buildAssistantMessage calls for tool-result round-trips.
// Without the back-fill, Parts would lose tool_use IDs and the model's
// next turn would fail with "tool_use_id not found".
func (sr *StreamingResponse) Collect() (*Response, error) {
	resp := &Response{}
	fcInParts := make(map[*genai.FunctionCall]bool)

	for chunk := range sr.Chunks {
		if chunk.Error != nil {
			// Return accumulated partial response alongside the error
			return resp, chunk.Error
		}

		resp.Text += chunk.Text
		resp.Thinking += chunk.Thinking
		resp.FunctionCalls = append(resp.FunctionCalls, chunk.FunctionCalls...)

		// Accumulate original parts first; track which FunctionCalls they
		// already carry so we don't duplicate below.
		for _, part := range chunk.Parts {
			if part != nil {
				resp.Parts = append(resp.Parts, part)
				if part.FunctionCall != nil {
					fcInParts[part.FunctionCall] = true
				}
			}
		}

		// Back-fill Parts from FunctionCalls that weren't already emitted
		// as structured parts. Anthropic-compatible providers (GLM,
		// MiniMax, Kimi) stream tool calls as FunctionCall-only, without
		// Part wrappers; this keeps Response.Parts usable for history
		// reconstruction in SendFunctionResponse.
		for _, fc := range chunk.FunctionCalls {
			if !fcInParts[fc] {
				resp.Parts = append(resp.Parts, &genai.Part{FunctionCall: fc})
			}
		}

		if chunk.Done {
			resp.FinishReason = chunk.FinishReason
		}

		// Keep the latest non-zero usage metadata (typically from the final chunk).
		// All providers (Gemini, Anthropic, OpenAI) report cumulative totals,
		// not per-chunk deltas, so use assignment (=) not addition (+=).
		if chunk.InputTokens > 0 {
			resp.InputTokens = chunk.InputTokens
		}
		if chunk.OutputTokens > 0 {
			resp.OutputTokens = chunk.OutputTokens
		}
		if chunk.CacheCreationInputTokens > 0 {
			resp.CacheCreationInputTokens = chunk.CacheCreationInputTokens
		}
		if chunk.CacheReadInputTokens > 0 {
			resp.CacheReadInputTokens = chunk.CacheReadInputTokens
		}
		if chunk.RateLimit != nil {
			resp.RateLimit = chunk.RateLimit
		}
	}

	return resp, nil
}
