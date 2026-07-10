package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── expandSafeEnvVars ───────────────────────────────────────────────────────

func TestExpandSafeEnvVars_Whitelisted(t *testing.T) {
	t.Setenv("HOME", "/test/home")
	t.Setenv("SECRET_KEY", "super-secret")

	got := expandSafeEnvVars("path: ${HOME}/data, secret: ${SECRET_KEY}")
	// HOME is whitelisted → expanded; SECRET_KEY is not → left as-is.
	wantSubstr := "path: /test/home/data"
	if !contains(got, wantSubstr) {
		t.Errorf("HOME not expanded: got %q, want to contain %q", got, wantSubstr)
	}
	if !contains(got, "${SECRET_KEY}") {
		t.Errorf("non-whitelisted var should be preserved: got %q", got)
	}
}

func TestExpandSafeEnvVars_NoVars(t *testing.T) {
	got := expandSafeEnvVars("plain text with no vars")
	if got != "plain text with no vars" {
		t.Errorf("plain text changed: got %q", got)
	}
}

func TestExpandSafeEnvVars_MultipleWhitelisted(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("SHELL", "/bin/zsh")
	got := expandSafeEnvVars("user=${USER} shell=${SHELL}")
	if !contains(got, "user=alice") || !contains(got, "shell=/bin/zsh") {
		t.Errorf("multiple whitelisted not expanded: %q", got)
	}
}

// ─── expandTilde ─────────────────────────────────────────────────────────────

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("cannot determine $HOME")
	}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/projects", filepath.Join(home, "projects")},
		{"no tilde", "/abs/path", "/abs/path"},
		{"empty", "", ""},
		{"relative", "rel/path", "rel/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandTilde(tc.input)
			if got != tc.want {
				t.Errorf("expandTilde(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ─── IsWorkDirAllowed ────────────────────────────────────────────────────────

func TestIsWorkDirAllowed(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Tools.AllowedDirs = []string{dir}

	cases := []struct {
		name    string
		workDir string
		want    bool
	}{
		{"exact match", dir, true},
		{"subdirectory", subdir, true},
		{"sibling", filepath.Dir(dir), false},
		{"unrelated", "/tmp/other", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.IsWorkDirAllowed(tc.workDir)
			if got != tc.want {
				t.Errorf("IsWorkDirAllowed(%q) = %v, want %v", tc.workDir, got, tc.want)
			}
		})
	}
}

func TestIsWorkDirAllowed_EmptyList(t *testing.T) {
	cfg := &Config{}
	if cfg.IsWorkDirAllowed("/anywhere") {
		t.Error("empty allowed-dirs list should reject everything")
	}
}

// ─── AddAllowedDir ───────────────────────────────────────────────────────────

func TestAddAllowedDir_New(t *testing.T) {
	cfg := &Config{}
	added := cfg.AddAllowedDir("/new/dir")
	if !added {
		t.Error("AddAllowedDir should return true for a new directory")
	}
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Errorf("expected 1 dir, got %d", len(cfg.Tools.AllowedDirs))
	}
}

func TestAddAllowedDir_Duplicate(t *testing.T) {
	cfg := &Config{}
	cfg.Tools.AllowedDirs = []string{"/existing"}
	added := cfg.AddAllowedDir("/existing")
	if added {
		t.Error("AddAllowedDir should return false for a duplicate")
	}
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Errorf("duplicate should not grow the list, got %d", len(cfg.Tools.AllowedDirs))
	}
}

func TestAddAllowedDir_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("cannot determine $HOME")
	}
	cfg := &Config{}
	cfg.AddAllowedDir("~/projects")
	// Should have been expanded to an absolute path.
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(cfg.Tools.AllowedDirs))
	}
	want := filepath.Join(home, "projects")
	if cfg.Tools.AllowedDirs[0] != want {
		t.Errorf("tilde not expanded: got %q, want %q", cfg.Tools.AllowedDirs[0], want)
	}
}

// ─── Validate ────────────────────────────────────────────────────────────────

func TestValidate_MissingAuth(t *testing.T) {
	cfg := DefaultConfig()
	// Clear any default keys.
	cfg.API = APIConfig{}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected ErrMissingAuth when no keys configured")
	}
}

func TestValidate_WithLegacyKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{APIKey: "test-key-12345"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("legacy API key should satisfy auth: %v", err)
	}
}

func TestValidate_OllamaNoKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{Backend: "ollama", ActiveProvider: "ollama"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("ollama should not require an API key: %v", err)
	}
}

func TestValidate_InvalidDoneGateMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{APIKey: "test-key"}
	cfg.DoneGate.Mode = "bogus"
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid done_gate.mode")
	}
}

func TestValidate_ValidDoneGateModes(t *testing.T) {
	for _, mode := range []string{"", "normal", "strict"} {
		cfg := DefaultConfig()
		cfg.API = APIConfig{APIKey: "test-key"}
		cfg.DoneGate.Mode = mode
		if err := cfg.Validate(); err != nil {
			t.Errorf("mode %q should be valid: %v", mode, err)
		}
	}
}

func TestValidate_NegativeAutoFixAttempts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{APIKey: "test-key"}
	cfg.DoneGate.AutoFixAttempts = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative auto_fix_attempts")
	}
}

func TestValidate_NegativeCheckTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{APIKey: "test-key"}
	cfg.DoneGate.CheckTimeout = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative check_timeout")
	}
}

func TestValidate_NegativeDeltaCheckTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{APIKey: "test-key"}
	cfg.Tools.DeltaCheck.Timeout = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative delta_check.timeout")
	}
}

func TestValidate_NegativeDeltaCheckMaxModules(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API = APIConfig{APIKey: "test-key"}
	cfg.Tools.DeltaCheck.MaxModules = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative delta_check.max_modules")
	}
}

// ─── validatePlanVerifyPolicy ────────────────────────────────────────────────

func TestValidatePlanVerifyPolicy_Disabled(t *testing.T) {
	// Disabled → always valid.
	if err := validatePlanVerifyPolicy(PlanVerifyPolicyConfig{Enabled: false}); err != nil {
		t.Errorf("disabled policy should be valid: %v", err)
	}
}

func TestValidatePlanVerifyPolicy_GlobalConflict(t *testing.T) {
	policy := PlanVerifyPolicyConfig{
		Enabled:       true,
		AllowContains: []string{"safe"},
		DenyContains:  []string{"safe"},
	}
	err := validatePlanVerifyPolicy(policy)
	if err == nil {
		t.Error("expected conflict error when same value in allow and deny")
	}
}

func TestValidatePlanVerifyPolicy_ProfileConflict(t *testing.T) {
	policy := PlanVerifyPolicyConfig{
		Enabled: true,
		Profiles: map[string]PlanVerifyPolicyProfileConfig{
			"myprofile": {
				AllowContains: []string{"fast"},
				DenyContains:  []string{"fast"},
			},
		},
	}
	err := validatePlanVerifyPolicy(policy)
	if err == nil {
		t.Error("expected profile conflict error")
	}
}

func TestValidatePlanVerifyPolicy_EmptyProfileKey(t *testing.T) {
	policy := PlanVerifyPolicyConfig{
		Enabled: true,
		Profiles: map[string]PlanVerifyPolicyProfileConfig{
			"  ": {},
		},
	}
	err := validatePlanVerifyPolicy(policy)
	if err == nil {
		t.Error("expected error for empty profile key")
	}
}

func TestValidatePlanVerifyPolicy_NoConflicts(t *testing.T) {
	policy := PlanVerifyPolicyConfig{
		Enabled:       true,
		AllowContains: []string{"safe"},
		DenyContains:  []string{"danger"},
		Profiles: map[string]PlanVerifyPolicyProfileConfig{
			"strict": {
				AllowContains: []string{"review"},
				DenyContains:  []string{"skip"},
			},
		},
	}
	if err := validatePlanVerifyPolicy(policy); err != nil {
		t.Errorf("valid policy should pass: %v", err)
	}
}

// ─── ConfigError ─────────────────────────────────────────────────────────────

func TestConfigError_Error(t *testing.T) {
	ce := ConfigError("some message")
	if ce.Error() != "some message" {
		t.Errorf("Error() = %q, want %q", ce.Error(), "some message")
	}
}

func TestErrMissingAuth_IsConfigError(t *testing.T) {
	if ErrMissingAuth == "" {
		t.Error("ErrMissingAuth should not be empty")
	}
	if ErrMissingAuth.Error() == "" {
		t.Error("ErrMissingAuth.Error() should not be empty")
	}
}

// ─── GetConfigPath ───────────────────────────────────────────────────────────

func TestGetConfigPath_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	got := GetConfigPath()
	want := filepath.Join("/custom/xdg", "gokin", "config.yaml")
	if got != want {
		t.Errorf("GetConfigPath(XDG) = %q, want %q", got, want)
	}
}

// ─── Save ────────────────────────────────────────────────────────────────────

func TestSave_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := DefaultConfig()
	cfg.API.APIKey = "test-save-key-12345"
	cfg.Model.Name = "glm-test-model"

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists and can be loaded.
	configPath := GetConfigPath()
	if configPath == "" {
		t.Fatal("GetConfigPath returned empty after Save")
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Load and verify.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.API.APIKey != "test-save-key-12345" {
		t.Errorf("APIKey round-trip: got %q", loaded.API.APIKey)
	}
	if loaded.Model.Name != "glm-test-model" {
		t.Errorf("Model.Name round-trip: got %q", loaded.Model.Name)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := DefaultConfig()
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(GetConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		t.Errorf("config file has insecure permissions: %04o (expected 0600)", mode)
	}
}

// ─── Load ────────────────────────────────────────────────────────────────────

func TestLoad_NoConfigFile(t *testing.T) {
	// Point XDG at an empty dir — Load should fall back to defaults.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Clear env keys so loadFromEnv doesn't set anything.
	t.Setenv("GOKIN_API_KEY", "")
	t.Setenv("GOKIN_GLM_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GOKIN_MODEL", "")
	t.Setenv("GOKIN_BACKEND", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with no config file should not error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOKIN_API_KEY", "env-key-12345")
	t.Setenv("GOKIN_MODEL", "env-model")
	t.Setenv("GOKIN_BACKEND", "glm")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.API.APIKey != "env-key-12345" {
		t.Errorf("GOKIN_API_KEY not picked up: got %q", cfg.API.APIKey)
	}
	if cfg.Model.Name != "env-model" {
		t.Errorf("GOKIN_MODEL not picked up: got %q", cfg.Model.Name)
	}
	if cfg.API.Backend != "glm" {
		t.Errorf("GOKIN_BACKEND not picked up: got %q", cfg.API.Backend)
	}
}

func TestLoad_GLMKeyFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOKIN_API_KEY", "")
	t.Setenv("GOKIN_GLM_KEY", "glm-env-key")
	t.Setenv("GLM_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.API.GLMKey != "glm-env-key" {
		t.Errorf("GOKIN_GLM_KEY not picked up: got %q", cfg.API.GLMKey)
	}
	if cfg.API.Backend != "glm" {
		t.Errorf("Backend should default to glm: got %q", cfg.API.Backend)
	}
}

// ─── LoadWithProjectDir ──────────────────────────────────────────────────────

func TestLoadWithProjectDir_ProjectConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOKIN_API_KEY", "")
	t.Setenv("GOKIN_GLM_KEY", "")
	t.Setenv("GLM_API_KEY", "")

	projectDir := t.TempDir()
	gokinDir := filepath.Join(projectDir, ".gokin")
	if err := os.MkdirAll(gokinDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Write a project config that overrides the model.
	projectConfig := []byte("model:\n  name: project-specific-model\n")
	if err := os.WriteFile(filepath.Join(gokinDir, "config.yaml"), projectConfig, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithProjectDir(projectDir)
	if err != nil {
		t.Fatalf("LoadWithProjectDir: %v", err)
	}
	if cfg.Model.Name != "project-specific-model" {
		t.Errorf("project config not merged: got %q", cfg.Model.Name)
	}
}

func TestLoadWithProjectDir_NoProjectConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOKIN_API_KEY", "fallback-key")

	projectDir := t.TempDir()
	cfg, err := LoadWithProjectDir(projectDir)
	if err != nil {
		t.Fatalf("LoadWithProjectDir: %v", err)
	}
	if cfg == nil {
		t.Fatal("nil config")
	}
}

// ─── loadFromFile ────────────────────────────────────────────────────────────

func TestLoadFromFile_NotExist(t *testing.T) {
	cfg := DefaultConfig()
	err := loadFromFile(cfg, "/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %v", err)
	}
}

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	badYAML := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(badYAML, []byte("model:\n  name: [unterminated"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	err := loadFromFile(cfg, badYAML)
	if err == nil {
		t.Error("expected YAML parse error")
	}
}

func TestLoadFromFile_ValidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	goodYAML := filepath.Join(tmpDir, "good.yaml")
	content := []byte("model:\n  name: from-file\n  temperature: 0.5\n")
	if err := os.WriteFile(goodYAML, content, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	if err := loadFromFile(cfg, goodYAML); err != nil {
		t.Fatalf("loadFromFile: %v", err)
	}
	if cfg.Model.Name != "from-file" {
		t.Errorf("Model.Name = %q, want %q", cfg.Model.Name, "from-file")
	}
	if cfg.Model.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", cfg.Model.Temperature)
	}
}

// ─── loadFromEnv ─────────────────────────────────────────────────────────────

func TestLoadFromEnv_LegacyGLMAPIKey(t *testing.T) {
	t.Setenv("GOKIN_API_KEY", "")
	t.Setenv("GOKIN_GLM_KEY", "")
	t.Setenv("GLM_API_KEY", "legacy-glm-key")

	cfg := DefaultConfig()
	loadFromEnv(cfg)

	if cfg.API.APIKey != "legacy-glm-key" {
		t.Errorf("GLM_API_KEY not picked up: got %q", cfg.API.APIKey)
	}
	if cfg.API.GLMKey != "legacy-glm-key" {
		t.Errorf("GLMKey not set: got %q", cfg.API.GLMKey)
	}
}

func TestLoadFromEnv_GOKINAPIKeyPriority(t *testing.T) {
	// GOKIN_API_KEY takes priority over GOKIN_GLM_KEY and GLM_API_KEY.
	t.Setenv("GOKIN_API_KEY", "priority-key")
	t.Setenv("GOKIN_GLM_KEY", "lower-priority")
	t.Setenv("GLM_API_KEY", "lowest")

	cfg := DefaultConfig()
	loadFromEnv(cfg)

	if cfg.API.APIKey != "priority-key" {
		t.Errorf("GOKIN_API_KEY should win: got %q", cfg.API.APIKey)
	}
}

// ─── MigrateConfig ───────────────────────────────────────────────────────────

func TestMigrateConfig_AutoDetectProvider(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Name = "glm-5.2"
	cfg.Model.Provider = ""

	MigrateConfig(cfg)

	if cfg.Model.Provider == "" {
		t.Error("provider should be auto-detected from model name")
	}
}

func TestMigrateConfig_PresetAlreadySet(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Provider = "glm"
	cfg.Model.Preset = "glm"

	MigrateConfig(cfg)
	// Should not error; provider stays set.
	if cfg.Model.Provider != "glm" {
		t.Errorf("provider changed: got %q", cfg.Model.Provider)
	}
}

func TestMigrateConfig_BackendAutoNormalized(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Provider = "kimi"
	cfg.API.Backend = "auto"

	MigrateConfig(cfg)

	if cfg.API.Backend != "kimi" {
		t.Errorf("backend 'auto' should normalize to provider: got %q", cfg.API.Backend)
	}
}

// ─── DetectProvider ──────────────────────────────────────────────────────────

func TestDetectProvider(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"glm-5", "glm"},
		{"deepseek-v4-pro", "deepseek"},
		{"kimi-for-coding", "kimi"},
		// Unknown models fall back to the default provider ("glm"), not "".
		// Pin this so a future change to the fallback default is caught.
		{"unknown-model", "glm"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := DetectProvider(tc.model)
			if got != tc.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

// ─── NormalizeConfig ─────────────────────────────────────────────────────────

func TestNormalizeConfig_SetsProvider(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Name = "glm-5"

	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Model.Provider != "glm" {
		t.Errorf("provider not set: got %q", cfg.Model.Provider)
	}
}

func TestNormalizeConfig_SetsBackend(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Provider = "kimi"
	cfg.API.Backend = ""

	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.API.Backend != "kimi" {
		t.Errorf("backend not synced: got %q", cfg.API.Backend)
	}
}

func TestNormalizeConfig_SyncsActiveProvider(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Provider = "deepseek"
	cfg.API.ActiveProvider = ""

	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.API.ActiveProvider != "deepseek" {
		t.Errorf("ActiveProvider not synced: got %q", cfg.API.ActiveProvider)
	}
}

func TestNormalizeConfig_BadPreset(t *testing.T) {
	cfg := &Config{}
	cfg.Model.Preset = "nonexistent-preset-xyz"

	err := NormalizeConfig(cfg)
	if err == nil {
		t.Error("expected error for unknown preset")
	}
}

func TestNormalizeConfig_NormalizesRetryProviderKeys(t *testing.T) {
	cfg := &Config{}
	cfg.API.Retry.Providers = map[string]ProviderRetryConfig{
		"  GLM ": {HTTPTimeout: 30_000_000_000},
		"kimi":   {StreamIdleTimeout: 60_000_000_000},
	}

	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if _, ok := cfg.API.Retry.Providers["glm"]; !ok {
		t.Error("retry provider key 'GLM' not normalized to 'glm'")
	}
	if _, ok := cfg.API.Retry.Providers["kimi"]; !ok {
		t.Error("retry provider key 'kimi' missing after normalization")
	}
}

// ─── GetEffectiveAPIKey ──────────────────────────────────────────────────────

func TestGetEffectiveAPIKey_WithKey(t *testing.T) {
	cfg := &Config{}
	cfg.API.GLMKey = "effective-test-key"

	key, err := cfg.GetEffectiveAPIKey()
	if err != nil {
		t.Fatalf("expected no error: %v", err)
	}
	if key == "" {
		t.Error("expected non-empty key")
	}
}

func TestGetEffectiveAPIKey_NoKey(t *testing.T) {
	cfg := &Config{}
	cfg.API = APIConfig{}

	_, err := cfg.GetEffectiveAPIKey()
	if err == nil {
		t.Error("expected error when no key configured")
	}
}

// ─── loadProjectConfig ───────────────────────────────────────────────────────

func TestLoadProjectConfig_FindsConfig(t *testing.T) {
	projectDir := t.TempDir()
	gokinDir := filepath.Join(projectDir, ".gokin")
	if err := os.MkdirAll(gokinDir, 0700); err != nil {
		t.Fatal(err)
	}
	projectConfig := []byte("model:\n  name: project-loaded\n")
	if err := os.WriteFile(filepath.Join(gokinDir, "config.yaml"), projectConfig, 0600); err != nil {
		t.Fatal(err)
	}

	// Change to the project dir so loadProjectConfig walks up to find it.
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	loadProjectConfig(cfg)

	if cfg.Model.Name != "project-loaded" {
		t.Errorf("project config not loaded: got %q", cfg.Model.Name)
	}
}

func TestLoadProjectConfig_NoConfig(t *testing.T) {
	// In a temp dir with no .gokin/config.yaml, loadProjectConfig should be a no-op.
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	originalModel := cfg.Model.Name
	loadProjectConfig(cfg)
	if cfg.Model.Name != originalModel {
		t.Errorf("model changed without project config: got %q, want %q", cfg.Model.Name, originalModel)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
