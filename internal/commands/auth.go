package commands

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"gokin/internal/auth"
	"gokin/internal/config"
)

// LoginCommand sets the API key.
type LoginCommand struct{}

func (c *LoginCommand) Name() string        { return "login" }
func (c *LoginCommand) Description() string { return "Set API key for a provider" }
func (c *LoginCommand) Usage() string {
	return `/login                        - Show current status
/login gemini <api_key>       - Set Gemini API key
/login anthropic <api_key>    - Set Anthropic API key
/login deepseek <api_key>     - Set DeepSeek API key
/login glm <api_key>          - Set GLM API key
/login minimax <api_key>      - Set MiniMax API key
/login kimi <api_key>         - Set Kimi API key

Tip: Use /oauth-login gemini or /oauth-login openai for OAuth authentication`
}
func (c *LoginCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "key",
		Priority: 0,
		HasArgs:  true,
		ArgHint:  "gemini|glm|deepseek|anthropic <key>",
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
		sb.WriteString(fmt.Sprintf("Unknown provider: %s\n\nUsage:\n", provider))
		for _, kp := range config.Providers {
			if !kp.KeyOptional {
				sb.WriteString(fmt.Sprintf("  /login %s <api_key>    - Set %s API key\n", kp.Name, kp.DisplayName))
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

	// Check for API key
	if len(args) < 2 {
		msg := fmt.Sprintf("Usage: /login %s <api_key>\n", provider)
		if p.SetupKeyURL != "" {
			msg += fmt.Sprintf("\nGet your %s API key at: %s", p.DisplayName, p.SetupKeyURL)
		}
		if p.HasOAuth {
			msg += fmt.Sprintf("\n\nAlternatively, use /oauth-login %s to sign in via OAuth.", p.Name)
		}
		return msg, nil
	}

	apiKey := args[1]

	// Validate key format
	if len(apiKey) < 10 {
		return "Invalid API key format (too short).", nil
	}

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

	return fmt.Sprintf(`%s API key saved!

Active provider: %s
Model: %s

Use /provider to switch providers
Use /model to switch models`, p.DisplayName, p.DisplayName, cfg.Model.Name), nil
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
			status = c.getOAuthStatus(cfg, p.Name)
		} else if key := p.GetKey(&cfg.API); key != "" {
			status = "configured " + maskKey(key)
		} else if p.UsesLegacyKey && cfg.API.APIKey != "" && activeProvider == p.Name {
			status = "configured " + maskKey(cfg.API.APIKey)
		}
		sb.WriteString(fmt.Sprintf("%s%s: %s\n", marker, p.DisplayName, status))
	}

	sb.WriteString(fmt.Sprintf("\nActive: %s\n", activeProvider))
	sb.WriteString(fmt.Sprintf("Model:  %s\n", cfg.Model.Name))

	sb.WriteString("\nCommands:\n")
	for _, p := range config.Providers {
		if !p.KeyOptional && !p.HasOAuth {
			sb.WriteString(fmt.Sprintf("  /login %s <key>%s- Set %s API key\n", p.Name, padSpaces(12-len(p.Name)), p.DisplayName))
		}
	}
	for _, p := range config.Providers {
		if p.HasOAuth {
			sb.WriteString(fmt.Sprintf("  /oauth-login %s%s- Login via %s\n", p.Name, padSpaces(9-len(p.Name)), p.DisplayName))
		}
	}
	sb.WriteString("  /provider              - Switch provider\n")
	sb.WriteString("  /model                 - Switch model\n")

	return sb.String()
}

// getOAuthStatus returns the OAuth status display string for a provider.
func (c *LoginCommand) getOAuthStatus(cfg *config.Config, provider string) string {
	switch provider {
	case "gemini":
		if cfg.API.GeminiOAuth != nil {
			return fmt.Sprintf("OAuth (%s)", cfg.API.GeminiOAuth.Email)
		}
	case "openai":
		if cfg.API.OpenAIOAuth != nil {
			return fmt.Sprintf("OAuth (%s)", cfg.API.OpenAIOAuth.Email)
		}
	}
	return "OAuth (configured)"
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
		sb.WriteString(fmt.Sprintf("/logout %-10s - Remove %s credentials\n", p.Name, p.DisplayName))
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
		cfg.API.GeminiOAuth = nil
		cfg.API.OpenAIOAuth = nil
		cfg.API.APIKey = ""
	} else if p := config.GetProvider(target); p != nil {
		p.SetKey(&cfg.API, "")
		if p.HasOAuth {
			switch target {
			case "gemini":
				cfg.API.GeminiOAuth = nil
			case "openai":
				cfg.API.OpenAIOAuth = nil
			}
		}
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

			result.WriteString(fmt.Sprintf("\n✓ Auto-switched to %s\n", newProvider))
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
		sb.WriteString(fmt.Sprintf("/provider %-10s - Switch to %s\n", p.Name, p.DisplayName))
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
			if cfg.API.HasProvider(p.Name) {
				status = "✓ ready"
			}

			sb.WriteString(fmt.Sprintf("%s%-10s %-16s %s\n", marker, p.Name, p.DisplayName, status))
		}

		sb.WriteString(fmt.Sprintf("\nCurrent: %s (%s)\n", currentProvider, cfg.Model.Name))
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
		return fmt.Sprintf("Already using %s", newProvider), nil
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

	return fmt.Sprintf("✓ Switched to %s (%s)", newProvider, cfg.Model.Name), nil
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
	sb.WriteString(fmt.Sprintf("Provider: %s\n", provider))
	sb.WriteString(fmt.Sprintf("Model:    %s\n\n", cfg.Model.Name))

	// API Keys
	sb.WriteString("API Keys:\n")

	for _, p := range config.Providers {
		if p.KeyOptional && !p.HasOAuth {
			continue
		}
		status := "not set"
		if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
			switch p.Name {
			case "gemini":
				if cfg.API.GeminiOAuth != nil {
					status = fmt.Sprintf("OAuth (%s)", cfg.API.GeminiOAuth.Email)
				}
			case "openai":
				if cfg.API.OpenAIOAuth != nil {
					status = fmt.Sprintf("OAuth (%s)", cfg.API.OpenAIOAuth.Email)
				}
			default:
				status = "OAuth (configured)"
			}
		} else if key := p.GetKey(&cfg.API); key != "" {
			status = maskKey(key)
		} else if p.UsesLegacyKey && cfg.API.APIKey != "" && provider == p.Name {
			status = maskKey(cfg.API.APIKey)
		}
		sb.WriteString(fmt.Sprintf("  %s: %s\n", p.DisplayName, status))
	}

	// Config path
	sb.WriteString(fmt.Sprintf("\nConfig: %s\n", config.GetConfigPath()))

	return sb.String(), nil
}

// OAuthLoginCommand handles /oauth-login for Google Account authentication
type OAuthLoginCommand struct{}

func (c *OAuthLoginCommand) Name() string { return "oauth-login" }
func (c *OAuthLoginCommand) Description() string {
	return "Login via OAuth (Gemini or OpenAI)"
}
func (c *OAuthLoginCommand) Usage() string {
	return `/oauth-login           - Show available OAuth providers
/oauth-login gemini   - Login via Google account (Gemini subscription)
/oauth-login openai   - Login via ChatGPT account (ChatGPT subscription)`
}
func (c *OAuthLoginCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "key",
		Priority: 5,
		HasArgs:  true,
		ArgHint:  "[gemini|openai]",
	}
}

func (c *OAuthLoginCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	// Determine provider
	provider := ""
	if len(args) > 0 {
		provider = strings.ToLower(args[0])
	}

	switch provider {
	case "gemini":
		return c.executeGeminiOAuth(ctx, cfg, app)
	case "openai":
		return c.executeOpenAIOAuth(ctx, cfg, app)
	default:
		return "OAuth Login\n\nAvailable providers:\n" +
			"  /oauth-login gemini   - Login via Google account (Gemini subscription)\n" +
			"  /oauth-login openai   - Login via ChatGPT account (ChatGPT subscription)\n", nil
	}
}

func (c *OAuthLoginCommand) executeGeminiOAuth(ctx context.Context, cfg *config.Config, app AppInterface) (string, error) {
	if cfg.API.HasOAuthToken("gemini") {
		return fmt.Sprintf("Already logged in via OAuth as %s.\n\nUse /oauth-logout gemini to sign out first.", cfg.API.GeminiOAuth.Email), nil
	}

	candidatePorts := []int{
		auth.GeminiOAuthCallbackPort,
		auth.GeminiOAuthCallbackPort + 1,
		auth.GeminiOAuthCallbackPort + 2,
	}

	var manager *auth.OAuthManager
	var server *auth.CallbackServer
	var authURL string
	var callbackPort int
	var startErr error
	var err error

	for _, port := range candidatePorts {
		manager = auth.NewGeminiOAuthManagerWithPort(port)
		authURL, err = manager.GenerateAuthURL()
		if err != nil {
			return "", fmt.Errorf("failed to generate auth URL: %w", err)
		}

		server = auth.NewCallbackServer(port, manager.GetState())
		if startErr = server.Start(); startErr == nil {
			callbackPort = port
			break
		}
	}

	if startErr != nil {
		return "", fmt.Errorf("failed to start OAuth callback server: %w", startErr)
	}
	defer server.Stop()

	browserOpened := openBrowser(authURL)

	var sb strings.Builder
	sb.WriteString("Opening browser for Google authentication...\n\n")
	if callbackPort != auth.GeminiOAuthCallbackPort {
		sb.WriteString(fmt.Sprintf("Using fallback callback port: %d\n\n", callbackPort))
	}
	if !browserOpened {
		sb.WriteString("Could not open browser automatically.\n")
		sb.WriteString("Please open this URL in your browser:\n\n")
		sb.WriteString(authURL + "\n\n")
	}
	sb.WriteString("Waiting for authentication (timeout: 5 minutes)...")

	code, err := server.WaitForCode(auth.OAuthCallbackTimeout)
	if err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	token, err := manager.ExchangeCode(ctx, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code: %w", err)
	}

	cfg.API.GeminiOAuth = &config.OAuthTokenConfig{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt.Unix(),
		Email:        token.Email,
	}
	cfg.API.ActiveProvider = "gemini"
	cfg.Model.Provider = "gemini"
	if gp := config.GetProvider("gemini"); gp != nil {
		cfg.Model.Name = gp.DefaultModel
	}

	if err := app.ApplyConfig(cfg); err != nil {
		return "", fmt.Errorf("failed to save config: %w", err)
	}

	email := token.Email
	if email == "" {
		email = "Google Account"
	}

	expiresIn := time.Until(token.ExpiresAt).Round(time.Minute)

	return fmt.Sprintf(`Logged in as %s via OAuth

Provider: gemini (OAuth)
Model: %s
Token expires in: %v

Use /status to check your configuration.
Use /oauth-logout gemini to sign out.`, email, cfg.Model.Name, expiresIn), nil
}

func (c *OAuthLoginCommand) executeOpenAIOAuth(ctx context.Context, cfg *config.Config, app AppInterface) (string, error) {
	if cfg.API.HasOAuthToken("openai") {
		return fmt.Sprintf("Already logged in via OpenAI OAuth as %s.\n\nUse /oauth-logout openai to sign out first.", cfg.API.OpenAIOAuth.Email), nil
	}

	candidatePorts := []int{
		auth.OpenAIOAuthCallbackPort,
		auth.OpenAIOAuthCallbackPort + 1,
		auth.OpenAIOAuthCallbackPort + 2,
	}

	var manager *auth.OpenAIOAuthManager
	var server *auth.CallbackServer
	var authURL string
	var callbackPort int
	var startErr error
	var err error

	for _, port := range candidatePorts {
		manager = auth.NewOpenAIOAuthManagerWithPort(port)
		authURL, err = manager.GenerateAuthURL()
		if err != nil {
			return "", fmt.Errorf("failed to generate auth URL: %w", err)
		}

		server = auth.NewCallbackServerWithPath(port, manager.GetState(), auth.OpenAIOAuthCallbackPath)
		if startErr = server.Start(); startErr == nil {
			callbackPort = port
			break
		}
	}

	if startErr != nil {
		return "", fmt.Errorf("failed to start OAuth callback server: %w", startErr)
	}
	defer server.Stop()

	browserOpened := openBrowser(authURL)

	var sb strings.Builder
	sb.WriteString("Opening browser for OpenAI authentication...\n\n")
	if callbackPort != auth.OpenAIOAuthCallbackPort {
		sb.WriteString(fmt.Sprintf("Using fallback callback port: %d\n\n", callbackPort))
	}
	if !browserOpened {
		sb.WriteString("Could not open browser automatically.\n")
		sb.WriteString("Please open this URL in your browser:\n\n")
		sb.WriteString(authURL + "\n\n")
	}
	sb.WriteString("Waiting for authentication (timeout: 5 minutes)...")

	code, err := server.WaitForCode(auth.OAuthCallbackTimeout)
	if err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	token, err := manager.ExchangeCode(ctx, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code: %w", err)
	}

	// Extract account ID from JWT
	accountID := ""
	if id, extractErr := auth.ExtractOpenAIAccountID(token.AccessToken); extractErr == nil {
		accountID = id
	}

	cfg.API.OpenAIOAuth = &config.OAuthTokenConfig{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt.Unix(),
		Email:        token.Email,
		AccountID:    accountID,
	}
	cfg.API.ActiveProvider = "openai"
	cfg.Model.Provider = "openai"
	if op := config.GetProvider("openai"); op != nil {
		cfg.Model.Name = op.DefaultModel
	}

	if err := app.ApplyConfig(cfg); err != nil {
		return "", fmt.Errorf("failed to save config: %w", err)
	}

	email := token.Email
	if email == "" {
		email = "ChatGPT Account"
	}

	expiresIn := time.Until(token.ExpiresAt).Round(time.Minute)

	return fmt.Sprintf(`Logged in as %s via OpenAI OAuth

Provider: openai (OAuth)
Model: %s
Token expires in: %v

Use /status to check your configuration.
Use /oauth-logout openai to sign out.`, email, cfg.Model.Name, expiresIn), nil
}

// OAuthLogoutCommand handles /oauth-logout
type OAuthLogoutCommand struct{}

func (c *OAuthLogoutCommand) Name() string        { return "oauth-logout" }
func (c *OAuthLogoutCommand) Description() string { return "Remove OAuth credentials" }
func (c *OAuthLogoutCommand) Usage() string {
	return `/oauth-logout           - Show OAuth logout options
/oauth-logout gemini   - Remove Gemini OAuth credentials
/oauth-logout openai   - Remove OpenAI OAuth credentials`
}
func (c *OAuthLogoutCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "logout",
		Priority: 15,
		HasArgs:  true,
		ArgHint:  "[gemini|openai]",
	}
}

func (c *OAuthLogoutCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	provider := ""
	if len(args) > 0 {
		provider = strings.ToLower(args[0])
	}

	switch provider {
	case "gemini":
		return c.logoutGemini(cfg, app)
	case "openai":
		return c.logoutOpenAI(cfg, app)
	default:
		var sb strings.Builder
		sb.WriteString("OAuth Logout\n\n")
		if cfg.API.HasOAuthToken("gemini") {
			email := cfg.API.GeminiOAuth.Email
			if email == "" {
				email = "Google Account"
			}
			sb.WriteString(fmt.Sprintf("  /oauth-logout gemini  - Sign out from %s\n", email))
		}
		if cfg.API.HasOAuthToken("openai") {
			email := cfg.API.OpenAIOAuth.Email
			if email == "" {
				email = "ChatGPT Account"
			}
			sb.WriteString(fmt.Sprintf("  /oauth-logout openai  - Sign out from %s\n", email))
		}
		if !cfg.API.HasOAuthToken("gemini") && !cfg.API.HasOAuthToken("openai") {
			sb.WriteString("No OAuth credentials found.\n")
		}
		return sb.String(), nil
	}
}

func (c *OAuthLogoutCommand) logoutGemini(cfg *config.Config, app AppInterface) (string, error) {
	if !cfg.API.HasOAuthToken("gemini") {
		return "No Gemini OAuth credentials found.", nil
	}

	email := cfg.API.GeminiOAuth.Email
	cfg.API.GeminiOAuth = nil

	applyFailed := false
	if err := app.ApplyConfig(cfg); err != nil {
		applyFailed = true
		if saveErr := cfg.Save(); saveErr != nil {
			return "", fmt.Errorf("failed to save config: %w", saveErr)
		}
	}

	var sb strings.Builder
	if email != "" {
		sb.WriteString(fmt.Sprintf("Logged out from %s (Gemini)\n\n", email))
	} else {
		sb.WriteString("Gemini OAuth credentials removed.\n\n")
	}
	if applyFailed {
		sb.WriteString("⚠ Client could not be re-initialized (no valid credentials remain).\n")
	}

	if cfg.API.GeminiKey != "" {
		sb.WriteString("Falling back to API key authentication.\n")
	} else {
		sb.WriteString("No API key configured.\n")
		sb.WriteString("Use /login gemini <key> to set an API key.\n")
		sb.WriteString("Or use /oauth-login gemini to sign in again.")
	}

	return sb.String(), nil
}

func (c *OAuthLogoutCommand) logoutOpenAI(cfg *config.Config, app AppInterface) (string, error) {
	if !cfg.API.HasOAuthToken("openai") {
		return "No OpenAI OAuth credentials found.", nil
	}

	email := cfg.API.OpenAIOAuth.Email
	cfg.API.OpenAIOAuth = nil

	applyFailed := false
	if err := app.ApplyConfig(cfg); err != nil {
		applyFailed = true
		if saveErr := cfg.Save(); saveErr != nil {
			return "", fmt.Errorf("failed to save config: %w", saveErr)
		}
	}

	var sb strings.Builder
	if email != "" {
		sb.WriteString(fmt.Sprintf("Logged out from %s (OpenAI)\n\n", email))
	} else {
		sb.WriteString("OpenAI OAuth credentials removed.\n\n")
	}
	if applyFailed {
		sb.WriteString("⚠ Client could not be re-initialized (no valid credentials remain).\n")
	}

	sb.WriteString("Use /oauth-login openai to sign in again.")

	return sb.String(), nil
}

// openBrowser opens a URL in the default browser
func openBrowser(url string) bool {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		// Try xdg-open first, then common browsers
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("google-chrome"); err == nil {
			cmd = exec.Command("google-chrome", url)
		} else if _, err := exec.LookPath("firefox"); err == nil {
			cmd = exec.Command("firefox", url)
		} else {
			return false
		}
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return false
	}

	return cmd.Start() == nil
}

func padSpaces(n int) string {
	if n <= 0 {
		return " "
	}
	return strings.Repeat(" ", n)
}
