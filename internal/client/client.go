package client

import (
	"context"
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
	DefaultKimiBaseURL      = "https://api.moonshot.ai/anthropic"
)

// ModelInfo contains information about an available model.
type ModelInfo struct {
	ID          string // Model identifier (e.g., "gemini-2.5-flash", "glm-4.7")
	Name        string // Human-readable name
	Description string // Short description
	Provider    string // Provider: "gemini" or "anthropic"
	BaseURL     string // Custom base URL for Anthropic-compatible APIs (e.g., GLM-4.7)
}

// AvailableModels is the list of supported models across all providers.
var AvailableModels = []ModelInfo{
	// Gemini 3.1 models (API key)
	{
		ID:          "gemini-3.1-pro-preview",
		Name:        "Gemini 3.1 Pro",
		Description: "Top reasoning: ARC-AGI-2 77%, 1M context",
		Provider:    "gemini",
	},
	// Gemini 3 models (API key)
	{
		ID:          "gemini-3-flash-preview",
		Name:        "Gemini 3 Flash",
		Description: "Fast & cheap: $0.50/$3 per 1M tokens",
		Provider:    "gemini",
	},
	{
		ID:          "gemini-3-pro-preview",
		Name:        "Gemini 3 Pro",
		Description: "Advanced reasoning: $2/$12 per 1M tokens",
		Provider:    "gemini",
	},
	// Gemini models (OAuth / Code Assist API)
	{
		ID:          "gemini-2.5-flash",
		Name:        "Gemini 2.5 Flash",
		Description: "Fast model (Code Assist, retiring Mar 2026)",
		Provider:    "gemini",
	},
	{
		ID:          "gemini-2.5-pro",
		Name:        "Gemini 2.5 Pro",
		Description: "Advanced model (Code Assist, retiring Mar 2026)",
		Provider:    "gemini",
	},
	// GLM models (via Anthropic-compatible API on Z.AI)
	{
		ID:          "glm-5.1",
		Name:        "GLM-5.1",
		Description: "Most capable GLM model — Max tier (Coding Plan)",
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
	// Kimi models (via Anthropic-compatible API)
	{
		ID:          "kimi-k2.5",
		Name:        "Kimi K2.5",
		Description: "Latest multimodal model from Moonshot AI",
		Provider:    "kimi",
		BaseURL:     DefaultKimiBaseURL,
	},
	{
		ID:          "kimi-k2-thinking-turbo",
		Name:        "Kimi K2 Thinking Turbo",
		Description: "Extended reasoning, fast output (60-100 tok/s)",
		Provider:    "kimi",
		BaseURL:     DefaultKimiBaseURL,
	},
	{
		ID:          "kimi-k2-turbo-preview",
		Name:        "Kimi K2 Turbo",
		Description: "Fast coding model, 256K context",
		Provider:    "kimi",
		BaseURL:     DefaultKimiBaseURL,
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
	SetRateLimiter(limiter interface{})

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

	// SetThinkingBudget configures the thinking/reasoning budget for the next request.
	// budget=0 disables thinking. Positive values set max thinking tokens.
	SetThinkingBudget(budget int32)

	// Close closes the client connection.
	Close() error
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

// Collect collects all chunks from a streaming response into a single Response.
func (sr *StreamingResponse) Collect() (*Response, error) {
	resp := &Response{}

	for chunk := range sr.Chunks {
		if chunk.Error != nil {
			// Return accumulated partial response alongside the error
			return resp, chunk.Error
		}

		resp.Text += chunk.Text
		resp.Thinking += chunk.Thinking
		resp.FunctionCalls = append(resp.FunctionCalls, chunk.FunctionCalls...)
		resp.Parts = append(resp.Parts, chunk.Parts...)

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
