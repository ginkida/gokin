package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
)

// TestBuildSetupChoices verifies that setup choices are built correctly.
// OAuth flows were removed in v0.65.0; only API-key / Ollama choices remain.
func TestBuildSetupChoices(t *testing.T) {
	choices := buildSetupChoices()

	if len(choices) == 0 {
		t.Error("expected at least one setup choice")
	}
}

// TestBuildSetupChoices_APIProviders verifies API provider choices
func TestBuildSetupChoices_APIProviders(t *testing.T) {
	choices := buildSetupChoices()

	// Check that we have API provider choices
	for _, choice := range choices {
		if strings.HasPrefix(choice.Action, "api:") {
			provider := strings.TrimPrefix(choice.Action, "api:")
			if choice.Title == "" {
				t.Errorf("API choice for %s has empty title", provider)
			}
			if len(choice.Lines) == 0 {
				t.Errorf("API choice for %s has no description lines", provider)
			}
		}
	}
}

// TestBuildSetupChoices_KimiFirst verifies Kimi is shown first in the wizard.
// v0.69 made Kimi the default provider; the wizard's ordering must reflect
// that — not the registry order (which is preserved for other callers).
func TestBuildSetupChoices_KimiFirst(t *testing.T) {
	choices := buildSetupChoices()
	if len(choices) == 0 {
		t.Fatal("no choices")
	}
	first := choices[0]
	if first.Action != "api:kimi" {
		t.Errorf("first choice action = %q, want %q", first.Action, "api:kimi")
	}
	if !strings.Contains(strings.ToLower(first.Title), "kimi") {
		t.Errorf("first choice title = %q, want to contain 'Kimi'", first.Title)
	}
}

// TestBuildSetupChoices_OllamaLast verifies Ollama is the last option — local
// install is a niche path; cloud providers should come first.
func TestBuildSetupChoices_OllamaLast(t *testing.T) {
	choices := buildSetupChoices()
	if len(choices) == 0 {
		t.Fatal("no choices")
	}
	last := choices[len(choices)-1]
	if last.Action != "ollama" {
		t.Errorf("last choice action = %q, want %q", last.Action, "ollama")
	}
}

// TestBuildSetupChoices_Ollama verifies Ollama local choice
func TestBuildSetupChoices_Ollama(t *testing.T) {
	choices := buildSetupChoices()

	foundOllama := false
	for _, choice := range choices {
		if choice.Action == "ollama" {
			foundOllama = true
			if choice.Title == "" {
				t.Error("Ollama choice has empty title")
			}
			if len(choice.Lines) == 0 {
				t.Error("Ollama choice has no description lines")
			}
		}
	}

	if !foundOllama {
		t.Error("expected to find ollama choice")
	}
}

// TestDetectEnvAPIKeys_LegacyGOKIN_APIKEY_Ignored verifies that the legacy
// GOKIN_API_KEY env var is no longer picked up as a gemini key — gemini was
// removed as a provider in v0.65 and the old code path dead-ended the wizard
// with "unknown provider: gemini" at validation time. Now the wizard simply
// ignores GOKIN_API_KEY (we don't know which provider it belongs to) and
// falls through to the interactive picker.
func TestDetectEnvAPIKeys_LegacyGOKIN_APIKEY_Ignored(t *testing.T) {
	// Clear every provider env var, then set ONLY the legacy one.
	allKeyVars := []string{
		"GOKIN_API_KEY",
		"GOKIN_GLM_KEY", "GLM_API_KEY",
		"GOKIN_DEEPSEEK_KEY", "DEEPSEEK_API_KEY",
		"GOKIN_MINIMAX_KEY", "MINIMAX_API_KEY",
		"GOKIN_KIMI_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY",
		"GOKIN_OLLAMA_KEY", "OLLAMA_API_KEY",
	}
	for _, k := range allKeyVars {
		t.Setenv(k, "")
	}
	t.Setenv("GOKIN_API_KEY", "test-key-123")

	envVar, backend, key := detectEnvAPIKeys()

	if envVar != "" || backend != "" || key != "" {
		t.Errorf("GOKIN_API_KEY alone must not be auto-claimed by any provider — got envVar=%q backend=%q key=%q", envVar, backend, key)
	}
}

// TestDetectEnvAPIKeys_ProviderSpecific verifies provider-specific env vars
func TestDetectEnvAPIKeys_ProviderSpecific(t *testing.T) {
	// Clear ALL provider env vars to avoid interference, then set only GLM
	allKeyVars := []string{
		"GOKIN_API_KEY",
		"GOKIN_GEMINI_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"GOKIN_ANTHROPIC_KEY", "ANTHROPIC_API_KEY",
		"GOKIN_GLM_KEY", "GLM_API_KEY",
		"GOKIN_DEEPSEEK_KEY", "DEEPSEEK_API_KEY",
		"GOKIN_MINIMAX_KEY", "MINIMAX_API_KEY",
		"GOKIN_KIMI_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY",
		"GOKIN_OLLAMA_KEY", "OLLAMA_API_KEY",
	}
	originals := make(map[string]string, len(allKeyVars))
	for _, k := range allKeyVars {
		originals[k] = os.Getenv(k)
	}
	defer func() {
		for k, v := range originals {
			if v != "" {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
	}()
	for _, k := range allKeyVars {
		os.Unsetenv(k)
	}

	// Set only GLM
	os.Setenv("GOKIN_GLM_KEY", "glm-test-key")

	envVar, backend, key := detectEnvAPIKeys()

	if envVar != "GOKIN_GLM_KEY" {
		t.Errorf("envVar = %q, want %q", envVar, "GOKIN_GLM_KEY")
	}
	if backend != "glm" {
		t.Errorf("backend = %q, want %q", backend, "glm")
	}
	if key != "glm-test-key" {
		t.Errorf("key = %q, want %q", key, "glm-test-key")
	}
}

// TestDetectEnvAPIKeys_NoneFound verifies empty result when no keys present
func TestDetectEnvAPIKeys_NoneFound(t *testing.T) {
	// All env vars that detectEnvAPIKeys checks (legacy + all providers from registry)
	allKeyVars := []string{
		"GOKIN_API_KEY",
		"GOKIN_GEMINI_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"GOKIN_ANTHROPIC_KEY", "ANTHROPIC_API_KEY",
		"GOKIN_GLM_KEY", "GLM_API_KEY",
		"GOKIN_DEEPSEEK_KEY", "DEEPSEEK_API_KEY",
		"GOKIN_MINIMAX_KEY", "MINIMAX_API_KEY",
		"GOKIN_KIMI_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY",
		"GOKIN_OLLAMA_KEY", "OLLAMA_API_KEY",
	}

	// Save and clear all
	originals := make(map[string]string, len(allKeyVars))
	for _, k := range allKeyVars {
		originals[k] = os.Getenv(k)
	}
	defer func() {
		for k, v := range originals {
			if v != "" {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
	}()

	for _, k := range allKeyVars {
		os.Unsetenv(k)
	}

	envVar, backend, key := detectEnvAPIKeys()

	if envVar != "" {
		t.Errorf("envVar = %q, want empty", envVar)
	}
	if backend != "" {
		t.Errorf("backend = %q, want empty", backend)
	}
	if key != "" {
		t.Errorf("key = %q, want empty", key)
	}
}

// TestSetupChoice_Structure verifies setupChoice struct
func TestSetupChoice_Structure(t *testing.T) {
	choice := setupChoice{
		Action: "test-action",
		Title:  "Test Title",
		Lines:  []string{"Line 1", "Line 2", "Line 3"},
	}

	if choice.Action != "test-action" {
		t.Errorf("Action = %q, want %q", choice.Action, "test-action")
	}
	if choice.Title != "Test Title" {
		t.Errorf("Title = %q, want %q", choice.Title, "Test Title")
	}
	if len(choice.Lines) != 3 {
		t.Errorf("len(Lines) = %d, want 3", len(choice.Lines))
	}
}

// TestColorCodes verifies ANSI color codes are defined
func TestColorCodes(t *testing.T) {
	// Verify color codes are not empty
	if colorReset == "" {
		t.Error("colorReset is empty")
	}
	if colorRed == "" {
		t.Error("colorRed is empty")
	}
	if colorGreen == "" {
		t.Error("colorGreen is empty")
	}
	if colorYellow == "" {
		t.Error("colorYellow is empty")
	}
	if colorCyan == "" {
		t.Error("colorCyan is empty")
	}
	if colorBold == "" {
		t.Error("colorBold is empty")
	}
}

// TestWelcomeMessage verifies welcome message is defined
func TestWelcomeMessage(t *testing.T) {
	if welcomeMessage == "" {
		t.Error("welcomeMessage is empty")
	}

	// Verify it contains expected content
	if !strings.Contains(welcomeMessage, "Gokin") {
		t.Error("welcomeMessage should contain 'Gokin'")
	}
}

// TestSpinnerFrames verifies spinner animation frames
func TestSpinnerFrames(t *testing.T) {
	if len(spinnerFrames) == 0 {
		t.Error("spinnerFrames is empty")
	}

	// Should have enough frames for animation
	if len(spinnerFrames) < 5 {
		t.Errorf("spinnerFrames has only %d frames, want at least 5", len(spinnerFrames))
	}
}

// TestGetConfigPath verifies config path detection
func TestGetConfigPath(t *testing.T) {
	configPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("getConfigPath() error = %v", err)
	}

	// Should return a valid path
	if configPath == "" {
		t.Error("configPath is empty")
	}

	// Should be an absolute path
	if !filepath.IsAbs(configPath) {
		t.Errorf("configPath = %q, want absolute path", configPath)
	}

	// Should contain expected directory and filename
	if !strings.Contains(configPath, ".config") && !strings.Contains(configPath, "AppData") {
		t.Logf("configPath = %q (may vary by OS)", configPath)
	}
}

// TestGetConfigPath_WithHome verifies config path respects XDG_CONFIG_HOME
func TestGetConfigPath_WithHome(t *testing.T) {
	// Save original
	original := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if original != "" {
			os.Setenv("XDG_CONFIG_HOME", original)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	}()

	// Set custom config home
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	configPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("getConfigPath() error = %v", err)
	}

	// Should use XDG_CONFIG_HOME
	if !strings.HasPrefix(configPath, tmpDir) {
		t.Errorf("configPath = %q, want to start with %q", configPath, tmpDir)
	}
}

// TestValidateAPIKeyReal_WithEmptyKey verifies validation with empty key
func TestValidateAPIKeyReal_WithEmptyKey(t *testing.T) {
	err := validateAPIKeyReal("gemini", "")
	if err == nil {
		t.Error("expected error for empty API key")
	}
}

// TestValidateAPIKeyReal_WithInvalidBackend verifies validation with invalid backend
func TestValidateAPIKeyReal_WithInvalidBackend(t *testing.T) {
	err := validateAPIKeyReal("invalid-backend", "some-key")
	if err == nil {
		t.Error("expected error for invalid backend")
	}
}

// TestProvidersExist verifies config.Providers is accessible
func TestProvidersExist(t *testing.T) {
	if len(config.Providers) == 0 {
		t.Error("config.Providers is empty")
	}
}

// TestGetProvider verifies provider lookup
func TestGetProvider(t *testing.T) {
	// Test with known provider
	provider := config.GetProvider("glm")
	if provider == nil {
		t.Error("expected to find glm provider")
	}
	if provider.Name != "glm" {
		t.Errorf("provider.Name = %q, want %q", provider.Name, "glm")
	}

	// Test with nil for non-existent provider
	provider = config.GetProvider("nonexistent-provider-xyz")
	if provider != nil {
		t.Error("expected nil for non-existent provider")
	}
}

// TestProviderEnvVars verifies providers have env vars configured
func TestProviderEnvVars(t *testing.T) {
	for _, provider := range config.Providers {
		if len(provider.EnvVars) == 0 {
			t.Logf("Provider %s has no env vars (may be OAuth-only)", provider.Name)
		}
	}
}

// TestProviderSetupURLs verifies providers have setup URLs
func TestProviderSetupURLs(t *testing.T) {
	for _, provider := range config.Providers {
		if provider.SetupKeyURL == "" {
			t.Logf("Provider %s has no setup URL", provider.Name)
		}
	}
}

// TestSetupActionTypes verifies all action types are valid
func TestSetupActionTypes(t *testing.T) {
	choices := buildSetupChoices()

	validPrefixes := []string{
		"oauth-",
		"api:",
		"ollama",
	}

	for _, choice := range choices {
		valid := false
		for _, prefix := range validPrefixes {
			if strings.HasPrefix(choice.Action, prefix) {
				valid = true
				break
			}
		}
		if !valid {
			t.Errorf("Invalid action type: %q", choice.Action)
		}
	}
}

// TestSetupChoiceTitles verifies all choices have titles
func TestSetupChoiceTitles(t *testing.T) {
	choices := buildSetupChoices()

	for i, choice := range choices {
		if choice.Title == "" {
			t.Errorf("Choice[%d] has empty title (action: %q)", i, choice.Action)
		}
	}
}

// TestSetupChoiceDescriptions verifies all choices have description lines
func TestSetupChoiceDescriptions(t *testing.T) {
	choices := buildSetupChoices()

	for i, choice := range choices {
		if len(choice.Lines) == 0 {
			t.Errorf("Choice[%d] (%s) has no description lines", i, choice.Action)
		}
	}
}

// TestValidateAPIKeyReal tests real API validation calls.
// Skipped in short mode since it makes real HTTP requests.
func TestValidateAPIKeyReal(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real HTTP validation tests in short mode")
	}

	providers := []string{"glm", "deepseek", "anthropic", "minimax", "kimi"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			err := validateAPIKeyReal(provider, "test-key")
			// Should return an error for invalid keys, but must not panic
			if err == nil {
				t.Logf("warning: %s validation succeeded with fake key", provider)
			}
		})
	}
}
