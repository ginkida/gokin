package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/config"
)

// LoginCommand sets the API key.
type LoginCommand struct{}

func (c *LoginCommand) Name() string        { return "login" }
func (c *LoginCommand) Description() string { return "Set API key for a provider" }
func (c *LoginCommand) Usage() string {
	return `/login                        - Show current status
/login glm <api_key>          - Set GLM API key
/login minimax <api_key>      - Set MiniMax API key
/login kimi <api_key>         - Set Kimi API key
/login deepseek <api_key>     - Set DeepSeek API key
`
}
func (c *LoginCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "key",
		Priority: 0,
		HasArgs:  true,
		ArgHint:  "glm|minimax|kimi|deepseek <key>",
	}
}

func (c *LoginCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	// No args - show current status and usage
	if len(args) == 0 {
		return c.showStatus(cfg), nil
	}

	// Parse: /login <provider> <key>
	provider := strings.ToLower(args[0])

	// Validate provider via registry
	p := config.GetProvider(provider)
	if p == nil {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Unknown provider: %s\n\nUsage:\n", provider)
		for _, kp := range config.Providers {
			if !kp.KeyOptional {
				fmt.Fprintf(&sb, "  /login %s <api_key>    - Set %s API key\n", kp.Name, kp.DisplayName)
			}
		}
		return sb.String(), nil
	}
	// OAuth-only providers — redirect to /oauth-login
	if p.KeyOptional && p.HasOAuth {
		return fmt.Sprintf("%s uses OAuth authentication.\n\nUse: /oauth-login %s", p.DisplayName, p.Name), nil
	}
	// Local-only providers (ollama)
	if p.KeyOptional {
		return fmt.Sprintf("%s does not require an API key.\n\nUse: /provider %s to switch.", p.DisplayName, p.Name), nil
	}

	// Check for API key — users who typed just `/login <provider>` expect
	// clear, actionable instructions, not a one-liner.
	if len(args) < 2 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "To set your %s API key:\n\n", p.DisplayName)
		if p.SetupKeyURL != "" {
			fmt.Fprintf(&sb, "  1. Open %s and copy your key\n", p.SetupKeyURL)
			fmt.Fprintf(&sb, "  2. Run:  /login %s <your-key>\n\n", p.Name)
		} else {
			fmt.Fprintf(&sb, "  Run:  /login %s <your-key>\n\n", p.Name)
		}
		fmt.Fprintf(&sb, "Keys are saved to %s.", config.GetConfigPath())
		if p.HasOAuth {
			fmt.Fprintf(&sb, "\n\nAlternatively, use /oauth-login %s to sign in via OAuth.", p.Name)
		}
		return sb.String(), nil
	}

	// Multi-word keys are always paste errors — real API keys never contain
	// whitespace. If the user accidentally pasted two values, catch it here
	// so we don't silently save half of a split key.
	if len(args) > 2 {
		return fmt.Sprintf("Key looks wrong: %d whitespace-separated parts detected.\n\n"+
			"Real API keys don't contain spaces. Check what you pasted — "+
			"did you include a comment or accidentally paste twice?\n\n"+
			"If your key really does contain spaces, quote it: /login %s \"<key>\"",
			len(args)-1, provider), nil
	}

	// Users paste keys from browser dashboards that often carry a trailing
	// newline, surrounding quotes, or stray whitespace. Strip those before
	// validating length — otherwise a perfectly good key fails silently and
	// the user gets 401 on first request.
	apiKey := sanitizePastedKey(args[1])

	// Common paste errors — surface them explicitly so users don't save junk.
	if strings.HasPrefix(apiKey, "http://") || strings.HasPrefix(apiKey, "https://") {
		return "That looks like a URL, not an API key. Open the URL in your browser to find the key, then paste just the key.", nil
	}
	if len(apiKey) < 10 {
		return "Invalid API key format (too short).", nil
	}

	// Detect provider-switch BEFORE we overwrite cfg.API.ActiveProvider.
	// When the active provider changes, the in-memory session history
	// was built against the old provider's wire format (thinking-block
	// signatures, tool-use ID formats, cache-control shapes). Sending
	// that history to a new provider usually 400s — DeepSeek in
	// particular rejects with "content[].thinking must be passed back"
	// because it requires thinking blocks from prior assistant turns
	// but Kimi/GLM stores them differently.
	//
	// Read the RAW field (not GetActiveProvider) so first-time setup
	// (ActiveProvider=="" and Backend=="") doesn't masquerade as a
	// switch from the "glm" default — there's no real history on a
	// fresh install and a spurious clear would confuse the user.
	//
	// Clearing on switch is the same thing users would have had to do
	// manually via /clear. We do it automatically and surface it in
	// the success message so nothing happens silently.
	prevProvider := strings.TrimSpace(cfg.API.ActiveProvider)
	if prevProvider == "" {
		prevProvider = strings.TrimSpace(cfg.API.Backend)
	}
	providerSwitched := prevProvider != "" && prevProvider != provider

	// Set the key for the provider
	cfg.API.SetProviderKey(provider, apiKey)

	// Set as active provider
	cfg.API.ActiveProvider = provider

	// Set default model for the provider
	cfg.Model.Provider = provider
	cfg.Model.Name = p.DefaultModel

	// Save config
	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}

	switchNote := ""
	if providerSwitched {
		app.ClearConversation()
		switchNote = fmt.Sprintf("\n  Session cleared: switched from %s (history format differs across providers).", prevProvider)
	}

	return fmt.Sprintf(`✓ %s API key saved (%s)

  Active provider: %s
  Model:           %s
  Config:          %s%s

Try sending a message to verify it works, or use /provider to switch.`,
		p.DisplayName, maskKey(apiKey), p.DisplayName, cfg.Model.Name, config.GetConfigPath(), switchNote), nil
}

func (c *LoginCommand) showStatus(cfg *config.Config) string {
	var sb strings.Builder

	sb.WriteString("API Key Status:\n\n")

	activeProvider := cfg.API.GetActiveProvider()

	for _, p := range config.Providers {
		if p.KeyOptional && !p.HasOAuth {
			continue // Skip ollama in login status (but show OAuth-only providers like openai)
		}
		marker := "  "
		if activeProvider == p.Name {
			marker = "> "
		}
		status := "not configured"
		if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
			status = "OAuth (configured)"
		} else if key := p.GetKey(&cfg.API); key != "" {
			status = "configured " + maskKey(key)
		} else if p.UsesLegacyKey && cfg.API.APIKey != "" && activeProvider == p.Name {
			status = "configured " + maskKey(cfg.API.APIKey)
		}
		fmt.Fprintf(&sb, "%s%s: %s\n", marker, p.DisplayName, status)
	}

	fmt.Fprintf(&sb, "\nActive: %s\n", activeProvider)
	fmt.Fprintf(&sb, "Model:  %s\n", cfg.Model.Name)

	sb.WriteString("\nCommands:\n")
	for _, p := range config.Providers {
		if !p.KeyOptional && !p.HasOAuth {
			fmt.Fprintf(&sb, "  /login %s <key>%s- Set %s API key\n", p.Name, padSpaces(12-len(p.Name)), p.DisplayName)
		}
	}
	for _, p := range config.Providers {
		if p.HasOAuth {
			fmt.Fprintf(&sb, "  /oauth-login %s%s- Login via %s\n", p.Name, padSpaces(9-len(p.Name)), p.DisplayName)
		}
	}
	sb.WriteString("  /provider              - Switch provider\n")
	sb.WriteString("  /model                 - Switch model\n")

	return sb.String()
}

// maskKey masks an API key for display (shows first 4 and last 4 chars).
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// LogoutCommand removes saved credentials.
type LogoutCommand struct{}

func (c *LogoutCommand) Name() string { return "logout" }
func (c *LogoutCommand) Description() string {
	return "Remove API key"
}
func (c *LogoutCommand) Usage() string {
	var sb strings.Builder
	sb.WriteString("/logout            - Remove active provider credentials\n")
	for _, p := range config.Providers {
		fmt.Fprintf(&sb, "/logout %-10s - Remove %s credentials\n", p.Name, p.DisplayName)
	}
	sb.WriteString("/logout all        - Remove all credentials")
	return sb.String()
}
func (c *LogoutCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "logout",
		Priority: 10,
		HasArgs:  true,
		ArgHint:  "[" + strings.Join(config.AllProviderNames(), "|") + "]",
	}
}

func (c *LogoutCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	target := ""
	if len(args) > 0 {
		target = strings.ToLower(args[0])
	}

	if target == "" {
		// Remove active provider key
		target = cfg.API.GetActiveProvider()
	}

	currentProvider := cfg.API.GetActiveProvider()

	if target == "all" {
		for _, p := range config.Providers {
			p.SetKey(&cfg.API, "")
		}
		cfg.API.APIKey = ""
	} else if p := config.GetProvider(target); p != nil {
		p.SetKey(&cfg.API, "")
		if currentProvider == target {
			cfg.API.APIKey = ""
		}
	} else {
		return fmt.Sprintf("Unknown provider: %s\n\nUsage: /logout [%s]", target, strings.Join(config.AllProviderNames(), "|")), nil
	}

	// Collect available providers with keys or OAuth tokens
	var availableProviders []string
	for _, p := range config.Providers {
		if p.GetKey(&cfg.API) != "" {
			availableProviders = append(availableProviders, p.Name)
		} else if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
			availableProviders = append(availableProviders, p.Name)
		} else if p.Name == "ollama" && cfg.API.OllamaBaseURL != "" {
			availableProviders = append(availableProviders, p.Name)
		}
	}

	// Apply config to reinitialize the client (drops the old client from memory)
	applyFailed := false
	if err := app.ApplyConfig(cfg); err != nil {
		applyFailed = true
		// ApplyConfig may fail if no credentials remain.
		// Still save to disk so credentials are removed on restart.
		if saveErr := cfg.Save(); saveErr != nil {
			return fmt.Sprintf("Failed to save: %v", saveErr), nil
		}
	}

	var result strings.Builder
	targetP := config.GetProvider(target)
	displayTarget := target
	if targetP != nil {
		displayTarget = targetP.DisplayName
	}
	if target == "all" {
		result.WriteString("✓ All credentials removed.\n")
	} else if targetP != nil && targetP.HasOAuth {
		result.WriteString(fmt.Sprintf("✓ %s credentials removed.\n", displayTarget))
	} else {
		result.WriteString(fmt.Sprintf("✓ %s API key removed.\n", displayTarget))
	}
	if applyFailed {
		result.WriteString("⚠ Client could not be re-initialized (no valid credentials remain).\n")
	}

	// Build response based on available providers
	if len(availableProviders) == 0 {
		result.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		result.WriteString("No API keys configured.\n\n")
		result.WriteString("Choose AI provider:\n")
		for _, lp := range config.Providers {
			if lp.KeyOptional {
				result.WriteString(fmt.Sprintf("  /login %s%s- %s\n", lp.Name, padSpaces(12-len(lp.Name)), lp.DisplayName))
			} else {
				result.WriteString(fmt.Sprintf("  /login %s <key>%s- %s\n", lp.Name, padSpaces(6-len(lp.Name)), lp.DisplayName))
			}
		}
	} else if target == currentProvider || target == "all" {
		// Active provider was removed, need to switch
		result.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		result.WriteString("Choose AI provider:\n\n")

		for i, provider := range availableProviders {
			marker := "  "
			if i == 0 {
				marker = "→ "
			}
			result.WriteString(fmt.Sprintf("%s/provider %s\n", marker, provider))
		}

		// Auto-switch to first available
		if len(availableProviders) > 0 {
			newProvider := availableProviders[0]
			cfg.API.ActiveProvider = newProvider
			cfg.Model.Provider = newProvider
			if np := config.GetProvider(newProvider); np != nil {
				cfg.Model.Name = np.DefaultModel
			}
			if err := app.ApplyConfig(cfg); err != nil {
				// ApplyConfig may fail if no credentials remain.
				// Still save to disk so the provider switch persists on restart.
				if saveErr := cfg.Save(); saveErr != nil {
					return fmt.Sprintf("Failed to save: %v", saveErr), nil
				}
			}

			// Clear session history — the history was built against the
			// just-removed provider's wire format (thinking signatures,
			// tool_use IDs, cache_control shapes), and a different
			// provider (e.g., DeepSeek strict) will 400 on replay.
			// Same class of bug as /login and /provider handle.
			app.ClearConversation()

			result.WriteString(fmt.Sprintf("\n✓ Auto-switched to %s — session cleared (history format differs across providers)\n", newProvider))
		}
	}

	return result.String(), nil
}

// ProviderCommand switches between providers.
type ProviderCommand struct{}

func (c *ProviderCommand) Name() string        { return "provider" }
func (c *ProviderCommand) Description() string { return "Switch AI provider" }
func (c *ProviderCommand) Usage() string {
	var sb strings.Builder
	sb.WriteString("/provider            - Show current provider\n")
	for _, p := range config.Providers {
		fmt.Fprintf(&sb, "/provider %-10s - Switch to %s\n", p.Name, p.DisplayName)
	}
	return sb.String()
}
func (c *ProviderCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "provider",
		Priority: 20,
		HasArgs:  true,
		ArgHint:  "[" + strings.Join(config.ProviderNames(), "|") + "]",
	}
}

func (c *ProviderCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	currentProvider := cfg.API.GetActiveProvider()

	// No args - show current status
	if len(args) == 0 {
		var sb strings.Builder
		sb.WriteString("AI Providers:\n\n")

		for _, p := range config.Providers {
			marker := "  "
			if p.Name == currentProvider {
				marker = "→ "
			}

			status := "not configured"
			if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
				status = "OAuth (configured)"
			} else if key := p.GetKey(&cfg.API); key != "" {
				status = "✓ ready"
			} else if p.UsesLegacyKey && cfg.API.APIKey != "" && p.Name == currentProvider {
				status = "✓ ready"
			} else if p.KeyOptional && !p.HasOAuth {
				status = "available (no key required)"
			}

			fmt.Fprintf(&sb, "%s%-10s %-16s %s\n", marker, p.Name, p.DisplayName, status)
		}

		fmt.Fprintf(&sb, "\nCurrent: %s (%s)\n", currentProvider, cfg.Model.Name)
		sb.WriteString("\nUsage: /provider <name>")

		return sb.String(), nil
	}

	// Switch provider
	newProvider := strings.ToLower(args[0])

	p := config.GetProvider(newProvider)
	if p == nil {
		return fmt.Sprintf("Unknown provider: %s\n\nAvailable: %s", newProvider, strings.Join(config.ProviderNames(), ", ")), nil
	}

	if newProvider == currentProvider {
		// Dead-end "Already using X" surprised users who ran `/provider X`
		// expecting it to recover from a stuck session (a common mental
		// model: "switch TO X even though I'm on X, fresh start"). Surface
		// the escape hatches they actually want — /clear to reset, /provider
		// to inspect, /login <other> to change provider.
		return fmt.Sprintf("Already using %s (%s).\n\n"+
			"  /clear              — reset conversation (keeps config)\n"+
			"  /provider           — show all providers\n"+
			"  /login <other> <key> — switch to a different provider",
			newProvider, cfg.Model.Name), nil
	}

	// Check if provider has credentials
	if p.KeyOptional && p.HasOAuth && !cfg.API.HasOAuthToken(newProvider) {
		return fmt.Sprintf("%s requires OAuth login.\n\nUse: /oauth-login %s", p.DisplayName, newProvider), nil
	}
	if !p.KeyOptional && !cfg.API.HasProvider(newProvider) {
		return fmt.Sprintf("%s is not configured.\n\nUse: /login %s <api_key>", newProvider, newProvider), nil
	}

	// Switch provider
	cfg.API.ActiveProvider = newProvider
	cfg.Model.Provider = newProvider
	cfg.Model.Name = p.DefaultModel

	// Apply MaxOutputTokens from preset if available for the new provider
	if preset, ok := config.ModelPresets[newProvider]; ok {
		cfg.Model.MaxOutputTokens = preset.MaxOutputTokens
	}

	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}

	// Clear session history on provider switch — the old provider's
	// thinking signatures / tool_use ID formats / cache_control shapes
	// don't round-trip cleanly through a different provider. DeepSeek in
	// particular 400s on missing thinking blocks from prior Kimi turns.
	// See LoginCommand.Execute for the same handling on key-set path.
	app.ClearConversation()

	return fmt.Sprintf("✓ Switched to %s (%s) — session cleared", newProvider, cfg.Model.Name), nil
}

// StatusCommand shows current configuration status.
type StatusCommand struct{}

func (c *StatusCommand) Name() string        { return "status" }
func (c *StatusCommand) Description() string { return "Show configuration status" }
func (c *StatusCommand) Usage() string       { return "/status" }
func (c *StatusCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "status",
		Priority: 30,
	}
}

func (c *StatusCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	var sb strings.Builder

	sb.WriteString("Configuration Status\n")
	sb.WriteString("====================\n\n")

	// Provider & Model
	provider := cfg.API.GetActiveProvider()
	fmt.Fprintf(&sb, "Provider: %s\n", provider)
	fmt.Fprintf(&sb, "Model:    %s\n\n", cfg.Model.Name)

	// API Keys
	sb.WriteString("API Keys:\n")

	for _, p := range config.Providers {
		if p.KeyOptional && !p.HasOAuth {
			continue
		}
		status := "not set"
		if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
			status = "OAuth (configured)"
		} else if key := p.GetKey(&cfg.API); key != "" {
			status = maskKey(key)
		} else if p.UsesLegacyKey && cfg.API.APIKey != "" && provider == p.Name {
			status = maskKey(cfg.API.APIKey)
		}
		fmt.Fprintf(&sb, "  %s: %s\n", p.DisplayName, status)
	}

	// Working directory and version
	fmt.Fprintf(&sb, "\nWorkDir: %s\n", app.GetWorkDir())
	fmt.Fprintf(&sb, "Config:  %s\n", config.GetConfigPath())
	if v := app.GetVersion(); v != "" {
		fmt.Fprintf(&sb, "Version: %s\n", v)
	}

	return sb.String(), nil
}

func padSpaces(n int) string {
	if n <= 0 {
		return " "
	}
	return strings.Repeat(" ", n)
}

// sanitizePastedKey normalizes a key that a user pasted into the TUI. It
// removes leading/trailing whitespace (incl. \n, \r, \t) and a matching pair
// of wrapping single or double quotes — both common artifacts when copying
// keys from a browser console or a shell export statement like
// `export GOKIN_KIMI_KEY="sk-kimi-..."`.
//
// Conservative: only strips a matching pair of quotes, never partial ones.
func sanitizePastedKey(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}
