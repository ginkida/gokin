package app

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/logging"
)

// newClientForFailover wraps client.NewClient so tests can substitute a
// mock without calling real provider APIs. Assign a replacement in tests
// and restore it in a deferred cleanup. Never reassign from production code.
var newClientForFailover = func(ctx context.Context, cfg *config.Config, modelID string) (client.Client, error) {
	return client.NewClient(ctx, cfg, modelID)
}

// activateEmergencyFailoverClient enables a provider fallback chain at runtime
// and swaps the app client to it. Returns a human-readable failover summary.
//
// NOTE: As of v0.69.4, this is no longer called automatically from the
// message-processor retry loop. Auto-failover would quietly switch users
// away from their chosen provider and surface errors from unrelated
// providers ("GLM insufficient balance" while on kimi), which was more
// confusing than helpful. The function is retained for tests and for
// potential future explicit triggers (e.g. a `/failover` slash command).
// User-configured `model.fallback_providers` in config.yaml still works —
// that path builds a FallbackClient in client.NewClient directly.
func (a *App) activateEmergencyFailoverClient() (string, error) {
	if a == nil || a.config == nil {
		return "", fmt.Errorf("app configuration is not available")
	}

	cfgCopy := *a.config
	primaryProvider := detectPrimaryProvider(&cfgCopy)
	candidates := buildFailoverCandidates(&cfgCopy, primaryProvider)
	if len(candidates) < 2 {
		return "", fmt.Errorf("no alternative providers available for automatic failover")
	}

	orderedProviders := client.OrderProvidersByHealth(candidates)

	cfgCopy.Model.Provider = orderedProviders[0]
	cfgCopy.Model.FallbackProviders = append([]string(nil), orderedProviders[1:]...)
	cfgCopy.API.ActiveProvider = orderedProviders[0]
	cfgCopy.API.Backend = orderedProviders[0]

	newClient, err := newClientForFailover(a.ctx, &cfgCopy, cfgCopy.Model.Name)
	if err != nil {
		return "", err
	}
	attachStatusCallback(newClient, &appStatusCallback{app: a})

	a.mu.Lock()
	oldClient := a.client
	a.setClientLocked(newClient) // a.mu held; also guards clientMu for background readers

	// Persist fallback chain in memory for subsequent requests in this session.
	a.config.Model.Provider = cfgCopy.Model.Provider
	a.config.Model.FallbackProviders = append([]string(nil), cfgCopy.Model.FallbackProviders...)
	a.config.API.ActiveProvider = cfgCopy.API.ActiveProvider
	a.config.API.Backend = cfgCopy.API.Backend

	if a.executor != nil {
		a.executor.SetClient(newClient)
		if a.registry != nil {
			newClient.SetTools(a.toolsForCurrentMode())
		}
	}
	if a.agentRunner != nil {
		a.agentRunner.SetClient(newClient)
	}
	if a.contextManager != nil {
		a.contextManager.SetClient(newClient)
	}
	// Re-point the session-memory LLM summarizer too (it captured the failed
	// provider's client at boot) so post-failover extractions use the new client
	// instead of silently degrading to heuristic summaries.
	if a.sessionMemory != nil {
		a.sessionMemory.SetSummarizer(appcontext.NewClientSessionSummarizer(newClient))
	}

	// Carry over system instruction, turn context (working memory), and
	// thinking budget to the new client
	if a.session != nil {
		if si := a.session.GetSystemInstruction(); si != "" {
			newClient.SetSystemInstruction(si)
		}
	}
	if tc := a.turnContextContent(); tc != "" {
		newClient.SetTurnContext(tc)
	}
	// Carry thinking by MODE, not the stale static budget: off → 0; on → a
	// floored budget; auto → leave it for the next routed request to set
	// adaptively (only carry an explicit user-configured static budget).
	switch config.ResolveThinkingMode(a.config.Model.ThinkingMode) {
	case config.ThinkingModeOff:
		newClient.SetThinkingBudget(0)
	case config.ThinkingModeOn:
		b := int32(a.config.Model.ThinkingBudget)
		if b <= 0 {
			b = 4096
		}
		newClient.SetThinkingBudget(b)
	default:
		if a.config.Model.ThinkingBudget > 0 {
			newClient.SetThinkingBudget(int32(a.config.Model.ThinkingBudget))
		}
	}
	a.mu.Unlock()

	if oldClient != nil {
		a.safeGo("close-old-client-after-failover", func() { _ = oldClient.Close() })
	}

	summary := fmt.Sprintf("%s -> %s", orderedProviders[0], strings.Join(orderedProviders[1:], " -> "))
	logging.Warn("automatic provider failover activated",
		"model", cfgCopy.Model.Name,
		"chain", summary)

	return summary, nil
}

func detectPrimaryProvider(cfg *config.Config) string {
	if cfg == nil {
		return "glm"
	}

	if cfg.Model.Provider != "" {
		return cfg.Model.Provider
	}
	if cfg.Model.Name != "" {
		if provider := config.DetectKnownProviderFromModel(cfg.Model.Name); provider != "" {
			return provider
		}
	}
	// Read raw fields only after model-name detection so a stale
	// ActiveProvider does not override a known model family such as
	// deepseek-v4-pro. GetActiveProvider() always returns "glm" by default.
	if cfg.API.ActiveProvider != "" {
		return cfg.API.ActiveProvider
	}
	if cfg.API.Backend != "" {
		return cfg.API.Backend
	}
	return "glm"
}

func buildFailoverCandidates(cfg *config.Config, primary string) []string {
	if cfg == nil {
		return nil
	}

	seen := map[string]bool{}
	ordered := make([]string, 0, 6)
	add := func(p string) {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		ordered = append(ordered, p)
	}

	add(primary)
	for _, p := range cfg.Model.FallbackProviders {
		add(p)
	}

	// Add all configured providers with credentials as fallback candidates.
	for _, p := range config.ProviderNames() {
		def := config.GetProvider(p)
		// Do not auto-append key-optional providers (e.g. ollama) unless they are
		// primary or explicitly configured by user in fallback_providers.
		if def != nil && def.KeyOptional {
			continue
		}
		if cfg.API.HasProvider(p) {
			add(p)
		}
	}

	return ordered
}
