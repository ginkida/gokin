package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	poolMu     sync.Mutex
)

// defaultGLMThinkingBudget is used when the user hasn't configured a budget
// explicitly. 8192 is a middle-ground — enough for multi-step reasoning without
// inflating the per-turn cost. The old default (2048) truncated chain-of-thought
// on moderately complex tasks. Users can override via cfg.Model.ThinkingBudget.
const defaultGLMThinkingBudget = 8192

// thinkingBudgetMin / thinkingBudgetMax are the API-enforced bounds for
// Anthropic-compat Extended Thinking. Requests outside this range 400 at
// the provider with a cryptic error. We normalize to the default rather
// than boundary-clamp: a user who hand-edited config.yaml to `100` almost
// certainly typo'd, and clamping to 1024 would mask the slip.
const (
	thinkingBudgetMin int32 = 1024
	thinkingBudgetMax int32 = 65536
)

// normalizeThinkingBudget repairs a configured budget for a provider call.
// Used by GLM / DeepSeek factories so a user's hand-edited typo in config.yaml
// doesn't produce a runtime 400 on the first message. 0 means "unset — use
// the auto-default"; any other out-of-range value falls back to default.
//
// Mirrors commands/thinking.go clampThinkingBudget. Duplicated because the
// client package mustn't depend on commands. Keep bounds + fallback policy
// in sync if you change either.
func normalizeThinkingBudget(budget int32, autoDefault int32) int32 {
	if budget == 0 {
		return autoDefault
	}
	if budget < thinkingBudgetMin || budget > thinkingBudgetMax {
		return autoDefault
	}
	return budget
}

// GetPool returns the global client connection pool, creating it if necessary.
func GetPool(cfg *config.Config) *ClientPool {
	poolMu.Lock()
	defer poolMu.Unlock()
	if globalPool == nil {
		maxSize := cfg.Model.MaxPoolSize
		if maxSize <= 0 {
			maxSize = DefaultMaxPoolSize
		}
		globalPool = NewClientPool(maxSize)
	}
	return globalPool
}

// ClosePool closes the global client connection pool.
func ClosePool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	if globalPool != nil {
		globalPool.Close()
		globalPool = nil
	}
}

// NewClient creates a client based on the configuration and model provider.
// This is the main entry point for client creation.
// If FallbackProviders are configured, returns a FallbackClient wrapping
// clients for the primary provider and each fallback provider.
// Uses the connection pool to reuse existing clients when possible.
func NewClient(ctx context.Context, cfg *config.Config, modelID string) (Client, error) {
	useConfiguredModel := modelID == "" || modelID == cfg.Model.Name
	// Migrate configuration to new format
	config.MigrateConfig(cfg)

	// Normalize configuration
	if err := config.NormalizeConfig(cfg); err != nil {
		return nil, err
	}

	// Follow migration/normalization when the caller passed the configured model.
	if useConfiguredModel {
		modelID = cfg.Model.Name
	}

	logging.Debug("creating client",
		"provider", cfg.Model.Provider,
		"modelID", modelID,
		"preset", cfg.Model.Preset)

	// Determine the primary provider. Model.Provider is authoritative when
	// set, otherwise infer from the model name before falling back to the
	// active API provider. This keeps the pool key aligned with auto-detected
	// clients such as deepseek-v4-pro.
	provider := strings.ToLower(strings.TrimSpace(cfg.Model.Provider))
	if provider == "" || provider == "auto" {
		provider = config.DetectKnownProviderFromModel(modelID)
	}
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(cfg.API.GetActiveProvider()))
	}
	resolvedModel, err := config.ResolveModelAlias(provider, cfg.Model.Name, modelID)
	if err != nil {
		return nil, err
	}
	modelID = resolvedModel

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
	return newFallbackClientFromConfigWithFactory(ctx, cfg, primaryProvider, modelID, createClientForProvider)
}

type providerClientFactory func(context.Context, *config.Config, string, string) (Client, error)

func newFallbackClientFromConfigWithFactory(
	ctx context.Context,
	cfg *config.Config,
	primaryProvider, modelID string,
	create providerClientFactory,
) (Client, error) {
	if create == nil {
		return nil, fmt.Errorf("fallback client factory is nil")
	}
	var clients []Client
	var clientProviders []string

	// Build candidate provider list (primary + configured fallbacks), then
	// reorder by dynamic health score so unhealthy providers are de-prioritized.
	candidateProviders := fallbackProviderCandidates(primaryProvider, cfg.Model.FallbackProviders)

	orderedProviders := reorderProvidersByHealth(candidateProviders)

	// Create clients in health-prioritized order.
	for _, provider := range orderedProviders {
		providerModel := fallbackModelForProvider(primaryProvider, provider, modelID)
		providerCfg := fallbackConfigForProvider(cfg, primaryProvider, provider)
		// FallbackClient owns and closes every child in its chain. These clients
		// must therefore be fresh and unpooled: sharing one with another wrapper
		// or a single-client caller would let either owner close the other's
		// active transport.
		c, err := create(ctx, providerCfg, provider, providerModel)
		if err != nil {
			if !isNilClient(c) {
				_ = c.Close()
			}
			logging.Warn("failed to create fallback chain client",
				"provider", provider,
				"error", err.Error())
			continue
		}
		if isNilClient(c) {
			logging.Warn("fallback chain factory returned nil client",
				"provider", provider)
			continue
		}
		clients = append(clients, c)
		clientProviders = append(clientProviders, provider)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("failed to create any client: primary provider %q and all fallback providers failed", primaryProvider)
	}

	fallback, err := NewFallbackClient(clients, clientProviders)
	if err != nil {
		for _, client := range clients {
			_ = client.Close()
		}
		return nil, err
	}
	return fallback, nil
}

func fallbackProviderCandidates(primary string, fallbacks []string) []string {
	providers := make([]string, 0, len(fallbacks)+1)
	seen := make(map[string]struct{}, len(fallbacks)+1)
	add := func(provider string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			return
		}
		if _, exists := seen[provider]; exists {
			return
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	add(primary)
	for _, provider := range fallbacks {
		add(provider)
	}
	return providers
}

// fallbackConfigForProvider prevents the primary model's custom endpoint from
// leaking into unrelated provider fallbacks. A custom GLM gateway cannot be
// assumed to serve DeepSeek/GLM model namespaces.
func fallbackConfigForProvider(cfg *config.Config, primaryProvider, provider string) *config.Config {
	if cfg == nil || strings.EqualFold(strings.TrimSpace(primaryProvider), strings.TrimSpace(provider)) ||
		strings.TrimSpace(cfg.Model.CustomBaseURL) == "" {
		return cfg
	}
	clone := cfg.Clone()
	clone.Model.CustomBaseURL = ""
	return clone
}

// fallbackModelForProvider keeps the explicitly requested model on its primary
// provider and uses each fallback provider's registered default everywhere
// else. Provider APIs do not share model namespaces (for example, DeepSeek cannot
// serve "glm-5.2").
func fallbackModelForProvider(primaryProvider, provider, requestedModel string) string {
	if strings.EqualFold(strings.TrimSpace(primaryProvider), strings.TrimSpace(provider)) {
		return requestedModel
	}
	if def := config.GetProvider(strings.ToLower(strings.TrimSpace(provider))); def != nil && def.DefaultModel != "" {
		return def.DefaultModel
	}
	return requestedModel
}

// getOrCreateClient retrieves a client from the pool or creates a new one.
func getOrCreateClient(ctx context.Context, cfg *config.Config, provider, modelID string) (Client, error) {
	pool := GetPool(cfg)
	fingerprint := clientConfigFingerprint(cfg, provider, modelID)
	prototype, err := pool.GetOrCreate(provider, modelID, fingerprint, func() (Client, error) {
		return createClientForProvider(ctx, cfg, provider, modelID)
	})
	if err != nil {
		return nil, err
	}
	return cloneClientForSession(prototype)
}

// clientConfigFingerprint identifies every immutable input captured when a
// provider client is constructed. It deliberately hashes credentials rather
// than exposing them in the pool key or logs.
func clientConfigFingerprint(cfg *config.Config, provider, modelID string) string {
	if cfg == nil {
		return "nil"
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	if config.GetProvider(provider) == nil {
		provider = config.DetectProviderFromModel(modelID)
	}
	apiKey := ""
	if def := config.GetProvider(provider); def != nil {
		legacyKey := ""
		if def.UsesLegacyKey {
			legacyKey = cfg.API.APIKey
		}
		apiKey = security.GetProviderKey(def.EnvVars, def.GetKey(&cfg.API), legacyKey).Value
	}
	override := cfg.API.Retry.Providers[provider]
	identity := fmt.Sprintf(
		"provider=%q\x00model=%q\x00custom_base=%q\x00ollama_base=%q\x00key=%q\x00temp=%g\x00max=%d\x00thinking=%t/%d\x00effort=%q\x00retry=%d/%d/%d\x00provider_retry=%d/%d",
		provider,
		modelID,
		cfg.Model.CustomBaseURL,
		cfg.API.OllamaBaseURL,
		apiKey,
		cfg.Model.Temperature,
		cfg.Model.MaxOutputTokens,
		cfg.Model.EnableThinking,
		cfg.Model.ThinkingBudget,
		cfg.Model.ReasoningEffort,
		cfg.API.Retry.RetryDelay,
		cfg.API.Retry.HTTPTimeout,
		cfg.API.Retry.StreamIdleTimeout,
		override.HTTPTimeout,
		override.StreamIdleTimeout,
	)
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:12])
}

// createClientForProvider creates a new client for the given provider.
func createClientForProvider(ctx context.Context, cfg *config.Config, provider, modelID string) (Client, error) {
	switch provider {
	case "glm":
		return newGLMClient(cfg, modelID)
	case "deepseek":
		return newDeepSeekClient(cfg, modelID)
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

// ProviderTimeouts describes the transport watchdogs that surround (but do not
// replace) the overall model-round deadline. ResponseHeaderTimeout applies only
// until the first HTTP headers arrive; StreamIdleTimeout applies between SSE
// fragments after that (zero means no application-level idle watchdog).
type ProviderTimeouts struct {
	ResponseHeaderTimeout time.Duration
	StreamIdleTimeout     time.Duration
}

// EffectiveProviderTimeouts resolves the exact defaults and global/provider
// overrides used when constructing the active client. Diagnostics call this
// same function so displayed limits cannot drift from runtime behavior.
func EffectiveProviderTimeouts(cfg *config.Config, provider string) ProviderTimeouts {
	provider = strings.ToLower(strings.TrimSpace(provider))
	defaultIdle, defaultHTTP := 30*time.Second, config.DefaultHTTPTimeout
	switch provider {
	case "glm":
		defaultIdle, defaultHTTP = 180*time.Second, 5*time.Minute
	case "deepseek":
		defaultIdle, defaultHTTP = 120*time.Second, 5*time.Minute
	case "ollama":
		defaultIdle, defaultHTTP = 0, config.DefaultHTTPTimeout
	}
	if cfg == nil {
		return ProviderTimeouts{
			ResponseHeaderTimeout: defaultHTTP,
			StreamIdleTimeout:     defaultIdle,
		}
	}
	streamIdle, responseHeader := resolveProviderTimeouts(cfg, provider, defaultIdle, defaultHTTP)
	return ProviderTimeouts{
		ResponseHeaderTimeout: responseHeader,
		StreamIdleTimeout:     streamIdle,
	}
}

// newGLMClient creates a GLM client using Z.AI's Anthropic-compatible API.
func newGLMClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	if err := config.ValidateReasoningEffort(cfg.Model.ReasoningEffort); err != nil {
		return nil, err
	}
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
	timeouts := EffectiveProviderTimeouts(cfg, "glm")

	// GLM 4.7+ supports extended thinking — enable by default if the user
	// hasn't explicitly configured it. This includes the default GLM-5.3-Flash.
	defaultBudget := int32(defaultGLMThinkingBudget)
	if config.GLMAlwaysOnReasoning(modelID) {
		defaultBudget = 16384 // max effort for coding by default
	}
	enableThinking := cfg.Model.EnableThinking
	thinkingBudget := cfg.Model.ThinkingBudget
	if !enableThinking && thinkingBudget == 0 && supportsGLMThinking(modelID) {
		enableThinking = true
		thinkingBudget = defaultBudget
	}
	// Repair out-of-range budgets (hand-edited config typos) when active.
	if enableThinking {
		thinkingBudget = normalizeThinkingBudget(thinkingBudget, defaultBudget)
	}

	if config.GLMAlwaysOnReasoning(modelID) {
		if config.ResolveThinkingMode(cfg.Model.ThinkingMode) == config.ThinkingModeOff {
			thinkingBudget = 0 // lowest effort; reasoning cannot be disabled
		}
		enableThinking = true
	}

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    enableThinking,
		ThinkingBudget:    thinkingBudget,
		ReasoningEffort:   cfg.Model.ReasoningEffort,
		StreamIdleTimeout: timeouts.StreamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: timeouts.ResponseHeaderTimeout,
		Provider:    "glm",
	}

	return NewAnthropicClient(anthropicConfig)
}

// supportsGLMThinking returns true for GLM models that support extended thinking.
func supportsGLMThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	return strings.HasPrefix(m, "glm-5") || strings.HasPrefix(m, "glm-4.7")
}

// defaultDeepSeekThinkingBudget selects high effort through the shared
// adaptive-budget mapping. DeepSeek ignores numeric budget_tokens.
const defaultDeepSeekThinkingBudget = 8192

// newDeepSeekClient creates a DeepSeek client using the Anthropic-compat
// endpoint (/anthropic), using the shared Anthropic wire-format client.
func newDeepSeekClient(cfg *config.Config, modelID string) (Client, error) {
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

	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultDeepSeekBaseURL
	}

	// DeepSeek V4 reasoning tool chains can pause 30-60s between chunks
	// on deep analytical calls. Wider idle tolerance than GLM's default
	// matches real-world behaviour.
	timeouts := EffectiveProviderTimeouts(cfg, "deepseek")

	// Auto-enable thinking for supported model IDs. Saved retired IDs are
	// resolved to current Flash before this factory runs.
	enableThinking := cfg.Model.EnableThinking
	thinkingBudget := cfg.Model.ThinkingBudget
	if !enableThinking && thinkingBudget == 0 && supportsDeepSeekThinking(modelID) {
		enableThinking = true
		thinkingBudget = defaultDeepSeekThinkingBudget
	}
	if config.ResolveThinkingMode(cfg.Model.ThinkingMode) == config.ThinkingModeOff {
		enableThinking = false
	}
	if enableThinking {
		thinkingBudget = normalizeThinkingBudget(thinkingBudget, defaultDeepSeekThinkingBudget)
	}

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    enableThinking,
		ThinkingBudget:    thinkingBudget,
		ReasoningEffort:   cfg.Model.ReasoningEffort,
		StreamIdleTimeout: timeouts.StreamIdleTimeout,
		MaxRetries:        0, // Request retries are orchestrated at App layer.
		RetryDelay:        cfg.API.Retry.RetryDelay,
		HTTPTimeout:       timeouts.ResponseHeaderTimeout,
		Provider:          "deepseek",
	}

	return NewAnthropicClient(anthropicConfig)
}

// supportsDeepSeekThinking covers current IDs and legacy reasoning aliases.
// Retired chat/reasoner config selections resolve to Flash before construction.
func supportsDeepSeekThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	if m == "deepseek-chat" {
		return false
	}
	return m == "deepseek-flash" || strings.HasPrefix(m, "deepseek-v4") ||
		strings.HasPrefix(m, "deepseek-reasoner") ||
		m == "deepseek"
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

	// The run-scoped --base-url override is stored in Model.CustomBaseURL for
	// every provider. Prefer it here as well so Ollama follows the same CLI
	// contract as hosted providers, then fall back to its persistent setting.
	baseURL := strings.TrimSpace(cfg.Model.CustomBaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.API.OllamaBaseURL)
	}
	if baseURL == "" {
		baseURL = config.DefaultOllamaBaseURL
	}

	timeouts := EffectiveProviderTimeouts(cfg, "ollama")

	ollamaConfig := OllamaConfig{
		BaseURL:     baseURL,
		APIKey:      loadedKey.Value, // Optional
		Model:       modelID,
		Temperature: cfg.Model.Temperature,
		MaxTokens:   cfg.Model.MaxOutputTokens,
		HTTPTimeout: timeouts.ResponseHeaderTimeout,
		MaxRetries:  0, // Request retries are orchestrated at App layer.
		RetryDelay:  cfg.API.Retry.RetryDelay,
	}

	return NewOllamaClient(ollamaConfig)
}
