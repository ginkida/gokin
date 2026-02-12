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
/login glm <api_key>          - Set GLM API key
/login deepseek <api_key>     - Set DeepSeek API key
/login anthropic <api_key>    - Set Anthropic API key

Tip: Use /oauth-login for Google account authentication (uses your Gemini subscription)`
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

	// Validate provider via registry (only key-based providers for /login)
	p := config.GetProvider(provider)
	if p == nil || p.KeyOptional {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Unknown provider: %s\n\nUsage:\n", provider))
		for _, kp := range config.Providers {
			if !kp.KeyOptional {
				sb.WriteString(fmt.Sprintf("  /login %s <api_key>    - Set %s API key\n", kp.Name, kp.DisplayName))
			}
		}
		return sb.String(), nil
	}

	// Check for API key
	if len(args) < 2 {
		msg := fmt.Sprintf("Usage: /login %s <api_key>\n", provider)
		if p.SetupKeyURL != "" {
			msg += fmt.Sprintf("\nGet your %s API key at: %s", p.DisplayName, p.SetupKeyURL)
		}
		if p.HasOAuth {
			msg += "\n\nAlternatively, use /oauth-login to sign in with your Google account\n(this uses your Gemini subscription instead of API credits)."
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
		if p.KeyOptional {
			continue // Skip ollama in login status
		}
		marker := "  "
		if activeProvider == p.Name {
			marker = "> "
		}
		status := "not configured"
		if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
			status = fmt.Sprintf("OAuth (%s)", cfg.API.GeminiOAuth.Email)
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
		if !p.KeyOptional {
			sb.WriteString(fmt.Sprintf("  /login %s <key>%s- Set %s API key\n", p.Name, padSpaces(12-len(p.Name)), p.DisplayName))
		}
	}
	sb.WriteString("  /oauth-login           - Login via Google account\n")
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
	return `/logout            - Remove active provider key
/logout gemini     - Remove Gemini key
/logout anthropic  - Remove Anthropic key
/logout glm        - Remove GLM key
/logout deepseek   - Remove DeepSeek key
/logout ollama     - Remove Ollama key
/logout all        - Remove all keys`
}
func (c *LogoutCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "logout",
		Priority: 10,
		HasArgs:  true,
		ArgHint:  "[gemini|anthropic|glm|deepseek|ollama|all]",
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
		cfg.API.APIKey = ""
	} else if p := config.GetProvider(target); p != nil {
		p.SetKey(&cfg.API, "")
		if p.HasOAuth {
			cfg.API.GeminiOAuth = nil
		}
		if currentProvider == target {
			cfg.API.APIKey = ""
		}
	} else {
		return fmt.Sprintf("Unknown provider: %s\n\nUsage: /logout [%s]", target, strings.Join(config.AllProviderNames(), "|")), nil
	}

	// Collect available providers with keys
	var availableProviders []string
	for _, p := range config.Providers {
		if p.GetKey(&cfg.API) != "" {
			availableProviders = append(availableProviders, p.Name)
		} else if p.KeyOptional && cfg.API.OllamaBaseURL != "" {
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
	result.WriteString(fmt.Sprintf("✓ %s API key removed.\n", strings.Title(target)))
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
	return `/provider            - Show current provider
/provider gemini     - Switch to Gemini
/provider anthropic  - Switch to Anthropic (Claude)
/provider deepseek   - Switch to DeepSeek
/provider glm        - Switch to GLM
/provider ollama     - Switch to Ollama (local)`
}
func (c *ProviderCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "provider",
		Priority: 20,
		HasArgs:  true,
		ArgHint:  "[gemini|anthropic|deepseek|glm|ollama]",
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

	// Check if provider has a key (ollama doesn't require key for local)
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
		if p.KeyOptional {
			continue
		}
		status := "not set"
		if p.HasOAuth && cfg.API.HasOAuthToken(p.Name) {
			status = fmt.Sprintf("OAuth (%s)", cfg.API.GeminiOAuth.Email)
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
	return "Login to Gemini using Google account (OAuth)"
}
func (c *OAuthLoginCommand) Usage() string {
	return `/oauth-login - Login to Gemini using your Google account

This uses your Gemini subscription (not API credits).
A browser window will open for Google authentication.`
}
func (c *OAuthLoginCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "google",
		Priority: 5,
	}
}

func (c *OAuthLoginCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	// Check if already logged in via OAuth
	if cfg.API.HasOAuthToken("gemini") {
		return fmt.Sprintf("Already logged in via OAuth as %s.\n\nUse /oauth-logout to sign out first.", cfg.API.GeminiOAuth.Email), nil
	}

	// Create OAuth manager and callback server with port fallback.
	// 8085 remains preferred for compatibility; neighboring ports are used if occupied.
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

	// Try to open browser
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

	// Note: We can't actually show this message before waiting since Execute blocks.
	// The user will see the result after the flow completes.

	// Wait for callback
	code, err := server.WaitForCode(auth.OAuthCallbackTimeout)
	if err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	// Exchange code for tokens
	token, err := manager.ExchangeCode(ctx, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code: %w", err)
	}

	// Save to config
	cfg.API.GeminiOAuth = &config.OAuthTokenConfig{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt.Unix(),
		Email:        token.Email,
	}
	cfg.API.ActiveProvider = "gemini"
	cfg.Model.Provider = "gemini"

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
Use /oauth-logout to sign out.`, email, cfg.Model.Name, expiresIn), nil
}

// OAuthLogoutCommand handles /oauth-logout
type OAuthLogoutCommand struct{}

func (c *OAuthLogoutCommand) Name() string        { return "oauth-logout" }
func (c *OAuthLogoutCommand) Description() string { return "Remove OAuth credentials" }
func (c *OAuthLogoutCommand) Usage() string {
	return `/oauth-logout - Remove Google OAuth credentials

This will sign you out of your Google account for Gemini.
You can use /login gemini <key> to use an API key instead.`
}
func (c *OAuthLogoutCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "logout",
		Priority: 15,
	}
}

func (c *OAuthLogoutCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	if !cfg.API.HasOAuthToken("gemini") {
		return "No OAuth credentials found.", nil
	}

	email := cfg.API.GeminiOAuth.Email

	// Remove OAuth token
	cfg.API.GeminiOAuth = nil

	// Apply config to reinitialize the client (drops the OAuth client from memory)
	applyFailed := false
	if err := app.ApplyConfig(cfg); err != nil {
		applyFailed = true
		// ApplyConfig may fail if no other credentials are available.
		// Still save the config to disk so OAuth is removed on restart.
		if saveErr := cfg.Save(); saveErr != nil {
			return "", fmt.Errorf("failed to save config: %w", saveErr)
		}
	}

	var sb strings.Builder
	if email != "" {
		sb.WriteString(fmt.Sprintf("Logged out from %s\n\n", email))
	} else {
		sb.WriteString("OAuth credentials removed.\n\n")
	}
	if applyFailed {
		sb.WriteString("⚠ Client could not be re-initialized (no valid credentials remain).\n")
	}

	// Check if API key is available
	if cfg.API.GeminiKey != "" {
		sb.WriteString("Falling back to API key authentication.\n")
	} else {
		sb.WriteString("No API key configured.\n")
		sb.WriteString("Use /login gemini <key> to set an API key.\n")
		sb.WriteString("Or use /oauth-login to sign in again.")
	}

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
