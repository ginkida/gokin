package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/security"
)

// globalPool is the shared client connection pool.
var (
	globalPool *ClientPool
	poolOnce   sync.Once
	poolMu     sync.Mutex
)

// GetPool returns the global client connection pool, creating it if necessary.
func GetPool(cfg *config.Config) *ClientPool {
	poolOnce.Do(func() {
		maxSize := cfg.Model.MaxPoolSize
		if maxSize <= 0 {
			maxSize = DefaultMaxPoolSize
		}
		globalPool = NewClientPool(maxSize)
	})
	return globalPool
}

// ClosePool closes the global client connection pool.
func ClosePool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	if globalPool != nil {
		globalPool.Close()
		globalPool = nil
		poolOnce = sync.Once{} // Allow re-initialization
	}
}

// NewClient creates a client based on the configuration and model provider.
// This is the main entry point for client creation.
// If FallbackProviders are configured, returns a FallbackClient wrapping
// clients for the primary provider and each fallback provider.
// Uses the connection pool to reuse existing clients when possible.
func NewClient(ctx context.Context, cfg *config.Config, modelID string) (Client, error) {
	// Migrate configuration to new format
	config.MigrateConfig(cfg)

	// Normalize configuration
	if err := config.NormalizeConfig(cfg); err != nil {
		return nil, err
	}

	// If modelID is not specified, use default from config
	if modelID == "" {
		modelID = cfg.Model.Name
	}

	logging.Debug("creating client",
		"provider", cfg.Model.Provider,
		"modelID", modelID,
		"preset", cfg.Model.Preset)

	// Determine the primary provider
	provider := cfg.Model.Provider
	if provider == "" {
		provider = cfg.API.Backend
	}

	// If fallback providers are configured, build a FallbackClient
	if len(cfg.Model.FallbackProviders) > 0 {
		return newFallbackClientFromConfig(ctx, cfg, provider, modelID)
	}

	// Single client creation with pool support
	return getOrCreateClient(ctx, cfg, provider, modelID)
}

// newFallbackClientFromConfig creates a FallbackClient with the primary provider
// and each configured fallback provider.
func newFallbackClientFromConfig(ctx context.Context, cfg *config.Config, primaryProvider, modelID string) (Client, error) {
	var clients []Client
	var clientProviders []string

	// Build candidate provider list (primary + configured fallbacks), then
	// reorder by dynamic health score so unhealthy providers are de-prioritized.
	candidateProviders := []string{}
	addProvider := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, existing := range candidateProviders {
			if existing == p {
				return
			}
		}
		candidateProviders = append(candidateProviders, p)
	}
	addProvider(primaryProvider)
	for _, fbProvider := range cfg.Model.FallbackProviders {
		addProvider(fbProvider)
	}

	orderedProviders := reorderProvidersByHealth(candidateProviders)

	// Create clients in health-prioritized order.
	for _, provider := range orderedProviders {
		c, err := getOrCreateClient(ctx, cfg, provider, modelID)
		if err != nil {
			logging.Warn("failed to create fallback chain client",
				"provider", provider,
				"error", err.Error())
			continue
		}
		clients = append(clients, c)
		clientProviders = append(clientProviders, provider)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("failed to create any client: primary provider %q and all fallback providers failed", primaryProvider)
	}

	return NewFallbackClient(clients, clientProviders)
}

// getOrCreateClient retrieves a client from the pool or creates a new one.
func getOrCreateClient(ctx context.Context, cfg *config.Config, provider, modelID string) (Client, error) {
	pool := GetPool(cfg)

	// Check pool first
	if c, ok := pool.Get(provider, modelID); ok {
		return c, nil
	}

	// Create new client
	c, err := createClientForProvider(ctx, cfg, provider, modelID)
	if err != nil {
		return nil, err
	}

	// Store in pool for reuse
	pool.Put(provider, modelID, c)

	return c, nil
}

// createClientForProvider creates a new client for the given provider.
func createClientForProvider(ctx context.Context, cfg *config.Config, provider, modelID string) (Client, error) {
	switch provider {
	case "glm":
		return newGLMClient(cfg, modelID)
	case "deepseek":
		return newDeepSeekClient(cfg, modelID)
	case "minimax":
		return newMiniMaxClient(cfg, modelID)
	case "kimi":
		return newKimiClient(cfg, modelID)
	case "gemini":
		// Check OAuth first
		if cfg.API.HasOAuthToken("gemini") {
			logging.Debug("using Gemini OAuth client", "email", cfg.API.GeminiOAuth.Email)
			oauthClient, err := NewGeminiOAuthClient(ctx, cfg)
			if err == nil {
				return oauthClient, nil
			}

			logging.Warn("failed to initialize Gemini OAuth client, falling back to API key if available", "error", err)

			// Graceful fallback: if OAuth is stale/broken but API key exists, continue with API key client.
			apiClient, keyErr := NewGeminiClient(ctx, cfg)
			if keyErr == nil {
				return apiClient, nil
			}

			return nil, fmt.Errorf("gemini auth failed (oauth error: %v; api-key fallback error: %v)", err, keyErr)
		}
		return NewGeminiClient(ctx, cfg)
	case "anthropic":
		return newAnthropicNativeClient(cfg, modelID)
	case "ollama":
		return newOllamaClient(cfg, modelID)
	default:
		// Fallback to auto-detection from model name
		return autoDetectClient(ctx, cfg, modelID)
	}
}

// autoDetectClient attempts to create a client by detecting the provider from the model name.
func autoDetectClient(ctx context.Context, cfg *config.Config, modelID string) (Client, error) {
	logging.Debug("unknown provider, auto-detecting from model name", "modelID", modelID)

	provider := config.DetectProviderFromModel(modelID)
	return createClientForProvider(ctx, cfg, provider, modelID)
}

func resolveProviderTimeouts(cfg *config.Config, provider string, defaultStreamIdle, defaultHTTP time.Duration) (time.Duration, time.Duration) {
	streamIdleTimeout := defaultStreamIdle
	if cfg.API.Retry.StreamIdleTimeout > 0 {
		streamIdleTimeout = cfg.API.Retry.StreamIdleTimeout
	}
	httpTimeout := defaultHTTP
	if cfg.API.Retry.HTTPTimeout > 0 {
		httpTimeout = cfg.API.Retry.HTTPTimeout
	}
	if provider != "" && len(cfg.API.Retry.Providers) > 0 {
		if override, ok := cfg.API.Retry.Providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			if override.StreamIdleTimeout > 0 {
				streamIdleTimeout = override.StreamIdleTimeout
			}
			if override.HTTPTimeout > 0 {
				httpTimeout = override.HTTPTimeout
			}
		}
	}
	return streamIdleTimeout, httpTimeout
}

// newGLMClient creates a GLM (GLM-4.7) client using Anthropic-compatible API.
func newGLMClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	p := config.GetProvider("glm")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for glm")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	// Log key source for debugging (without exposing the key)
	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	// Validate key format
	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	// Use custom base URL if provided, otherwise use default GLM endpoint
	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultGLMBaseURL
	}

	// GLM/Z.AI needs longer timeouts — server is slower than Anthropic.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "glm", 180*time.Second, 5*time.Minute)

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    cfg.Model.ThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: httpTimeout,
		Provider:    "glm",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newDeepSeekClient creates a DeepSeek client using Anthropic-compatible API.
func newDeepSeekClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	p := config.GetProvider("deepseek")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for deepseek")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	// Log key source for debugging (without exposing the key)
	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	// Validate key format
	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	// Use custom base URL if provided, otherwise use default DeepSeek endpoint
	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultDeepSeekBaseURL
	}

	// DeepSeek may have long silent reasoning/tool phases on complex prompts.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "deepseek", 120*time.Second, 5*time.Minute)

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    cfg.Model.ThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: httpTimeout,
		Provider:    "deepseek",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newMiniMaxClient creates a MiniMax client using Anthropic-compatible API.
func newMiniMaxClient(cfg *config.Config, modelID string) (Client, error) {
	p := config.GetProvider("minimax")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for minimax")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultMiniMaxBaseURL
	}

	// MiniMax may have long silent reasoning/tool phases.
	// Use relaxed defaults unless user explicitly configured stricter values.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "minimax", 120*time.Second, 5*time.Minute)

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    cfg.Model.ThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		MaxRetries:        0, // Request retries are orchestrated at App layer.
		RetryDelay:        cfg.API.Retry.RetryDelay,
		HTTPTimeout:       httpTimeout,
		Provider:          "minimax",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newKimiClient creates a Kimi Code client using Anthropic-compatible API.
func newKimiClient(cfg *config.Config, modelID string) (Client, error) {
	p := config.GetProvider("kimi")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for kimi")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultKimiBaseURL
	}

	// Kimi may pause longer between chunks on complex tool chains.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "kimi", 120*time.Second, 5*time.Minute)

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    cfg.Model.ThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		MaxRetries:        0, // Request retries are orchestrated at App layer.
		RetryDelay:        cfg.API.Retry.RetryDelay,
		HTTPTimeout:       httpTimeout,
		Provider:          "kimi",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newAnthropicNativeClient creates a native Anthropic client for Claude models.
func newAnthropicNativeClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	p := config.GetProvider("anthropic")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for anthropic")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	// Log key source for debugging (without exposing the key)
	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	// Validate key format
	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	// Use custom base URL if provided, otherwise use default Anthropic endpoint
	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultAnthropicBaseURL
	}

	// Anthropic can also produce long silent phases with extended thinking/tools.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "anthropic", 120*time.Second, 5*time.Minute)

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    cfg.Model.ThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: httpTimeout,
		Provider:    "anthropic",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newOllamaClient creates an Ollama client for local LLM inference.
func newOllamaClient(cfg *config.Config, modelID string) (Client, error) {
	// Load optional API key (for remote Ollama servers with auth)
	p := config.GetProvider("ollama")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for ollama")
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), "")

	// Log key source for debugging (without exposing the key)
	if loadedKey.IsSet() {
		logging.Debug("loaded Ollama API key",
			"source", loadedKey.Source,
			"model", modelID)
	}

	// Use custom base URL if provided, otherwise use default
	baseURL := cfg.API.OllamaBaseURL
	if baseURL == "" {
		baseURL = config.DefaultOllamaBaseURL
	}

	_, httpTimeout := resolveProviderTimeouts(cfg, "ollama", 0, config.DefaultHTTPTimeout)

	ollamaConfig := OllamaConfig{
		BaseURL:     baseURL,
		APIKey:      loadedKey.Value, // Optional
		Model:       modelID,
		Temperature: cfg.Model.Temperature,
		MaxTokens:   cfg.Model.MaxOutputTokens,
		HTTPTimeout: httpTimeout,
		MaxRetries:  0, // Request retries are orchestrated at App layer.
		RetryDelay:  cfg.API.Retry.RetryDelay,
	}

	return NewOllamaClient(ollamaConfig)
}
