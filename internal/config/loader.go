package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gokin/internal/logging"

	"gopkg.in/yaml.v3"
)

// Load loads configuration from file and environment variables.
// It merges global config with per-project config (.gokin/config.yaml) if present.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from global config file
	configPath := getConfigPath()
	if configPath != "" {
		if err := loadFromFile(cfg, configPath); err != nil {
			// Config file is optional, don't fail if it doesn't exist
			if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}

	// Override with environment variables
	loadFromEnv(cfg)

	// Merge per-project config if it exists
	loadProjectConfig(cfg)

	// Migrate legacy Kimi model names (pre-v0.69 users had kimi-k2.5 /
	// kimi-k2-thinking-turbo / kimi-k2-turbo-preview pointing at Moonshot
	// Developer API). Coding Plan endpoint only serves kimi-for-coding;
	// rewrite silently so existing YAML configs don't error out.
	migrateLegacyKimiModelName(cfg)

	return cfg, nil
}

// migrateLegacyKimiModelName rewrites retired Kimi model IDs to the
// Coding Plan canonical name. Called on every Load so both global and
// project configs benefit; idempotent.
//
// Skipped when CustomBaseURL is set — users with an explicit Moonshot
// Developer API endpoint may still use legacy model IDs there, and we
// don't want to silently rewrite their model into one their endpoint
// doesn't serve.
func migrateLegacyKimiModelName(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Model.CustomBaseURL) != "" {
		return
	}
	legacy := map[string]bool{
		"kimi-k2.5":              true,
		"kimi-k2-thinking-turbo": true,
		"kimi-k2-turbo-preview":  true,
	}
	if legacy[cfg.Model.Name] {
		cfg.Model.Name = "kimi-for-coding"
	}
}

// LoadWithProjectDir loads configuration with a specific project directory.
func LoadWithProjectDir(projectDir string) (*Config, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}

	// Load project-specific config
	projectConfigPath := filepath.Join(projectDir, ".gokin", "config.yaml")
	if err := loadFromFile(cfg, projectConfigPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load project config: %w", err)
		}
	}

	return cfg, nil
}

// loadProjectConfig attempts to find and load .gokin/config.yaml from the current directory upward.
func loadProjectConfig(cfg *Config) {
	dir, err := os.Getwd()
	if err != nil {
		logging.Debug("failed to get working directory for project config", "error", err)
		return
	}

	// Walk up to find .gokin/config.yaml
	for {
		projectConfig := filepath.Join(dir, ".gokin", "config.yaml")
		if _, err := os.Stat(projectConfig); err == nil {
			// Found project config, merge it
			if err := loadFromFile(cfg, projectConfig); err != nil {
				slog.Warn("failed to load project config", "path", projectConfig, "error", err)
			}
			return
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}
}

// getConfigPath returns the path to the config file.
func getConfigPath() string {
	// Check XDG_CONFIG_HOME first
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "gokin", "config.yaml")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// For macOS, favor Library/Application Support/gokin if it exists or if we're on darwin
	if runtime.GOOS == "darwin" {
		appSupport := filepath.Join(homeDir, "Library", "Application Support", "gokin", "config.yaml")
		if _, err := os.Stat(appSupport); err == nil {
			return appSupport
		}
		// Fall back to .config if it already exists there
		dotConfig := filepath.Join(homeDir, ".config", "gokin", "config.yaml")
		if _, err := os.Stat(dotConfig); err == nil {
			return dotConfig
		}
		// Default to App Support for new installs on macOS
		return appSupport
	}

	// Default for other Unix-like systems
	return filepath.Join(homeDir, ".config", "gokin", "config.yaml")
}

// loadFromFile loads configuration from a YAML file.
func loadFromFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Warn if config file has overly permissive permissions
	if info, statErr := os.Stat(path); statErr == nil {
		mode := info.Mode().Perm()
		if mode&0077 != 0 {
			slog.Warn("config file has insecure permissions",
				"path", path,
				"mode", fmt.Sprintf("%04o", mode),
				"recommended", "0600")
		}
	}

	// Expand only safe environment variables in the config file
	expanded := expandSafeEnvVars(string(data))

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	return nil
}

// safeEnvVars is the whitelist of environment variables that can be expanded in config files.
// This prevents accidental exposure of sensitive variables like API keys, secrets, etc.
var safeEnvVars = map[string]bool{
	"HOME":             true,
	"USER":             true,
	"GOKIN_CONFIG_DIR": true,
	"XDG_CONFIG_HOME":  true,
	"XDG_DATA_HOME":    true,
	"XDG_CACHE_HOME":   true,
	"TMPDIR":           true,
	"TMP":              true,
	"TEMP":             true,
	"PWD":              true,
	"SHELL":            true,
	"LANG":             true,
	"LC_ALL":           true,
}

// expandSafeEnvVars expands only whitelisted environment variables.
// Non-whitelisted variables are left as-is (e.g., ${SECRET_KEY} stays as ${SECRET_KEY}).
func expandSafeEnvVars(data string) string {
	return os.Expand(data, func(key string) string {
		if safeEnvVars[key] {
			return os.Getenv(key)
		}
		// Return the original variable syntax for non-whitelisted vars
		return "${" + key + "}"
	})
}

// loadFromEnv loads configuration from environment variables.
func loadFromEnv(cfg *Config) {
	// Load provider-specific keys from environment via registry
	for _, p := range Providers {
		for _, envVar := range p.EnvVars {
			if key := os.Getenv(envVar); key != "" {
				p.SetKey(&cfg.API, key)
				break
			}
		}
	}

	// Legacy API key from environment (check multiple sources)
	// Priority: GOKIN_API_KEY > GOKIN_GLM_KEY > GLM_API_KEY > GEMINI_API_KEY
	if apiKey := os.Getenv("GOKIN_API_KEY"); apiKey != "" {
		cfg.API.APIKey = apiKey
	} else if apiKey := os.Getenv("GOKIN_GLM_KEY"); apiKey != "" {
		cfg.API.APIKey = apiKey
		cfg.API.GLMKey = apiKey
		if cfg.API.Backend == "" {
			cfg.API.Backend = "glm"
		}
	} else if apiKey := os.Getenv("GLM_API_KEY"); apiKey != "" {
		cfg.API.APIKey = apiKey
		cfg.API.GLMKey = apiKey
		if cfg.API.Backend == "" {
			cfg.API.Backend = "glm"
		}
	} else if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		cfg.API.APIKey = apiKey
	}

	if model := os.Getenv("GOKIN_MODEL"); model != "" {
		cfg.Model.Name = model
	}

	if backend := os.Getenv("GOKIN_BACKEND"); backend != "" {
		cfg.API.Backend = backend
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if err := ValidateRetryConfig(c); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(c.DoneGate.Mode))
	if mode != "" && mode != "normal" && mode != "strict" {
		return fmt.Errorf("invalid done_gate.mode %q: expected \"normal\" (verify build/tests pass) or \"strict\" (also verify via LLM review)", c.DoneGate.Mode)
	}
	if c.DoneGate.AutoFixAttempts < 0 {
		return fmt.Errorf("done_gate.auto_fix_attempts must be >= 0")
	}
	if c.DoneGate.CheckTimeout < 0 {
		return fmt.Errorf("done_gate.check_timeout must be >= 0")
	}
	if c.Tools.DeltaCheck.Timeout < 0 {
		return fmt.Errorf("tools.delta_check.timeout must be >= 0")
	}
	if c.Tools.DeltaCheck.MaxModules < 0 {
		return fmt.Errorf("tools.delta_check.max_modules must be >= 0")
	}
	if err := validatePlanVerifyPolicy(c.Plan.VerifyPolicy); err != nil {
		return err
	}

	// Check provider keys via registry. (Pre-v0.65 also checked an
	// OAuth-token-for-Gemini path here; that flow was removed and the
	// HasOAuthToken stub now always returns false. Keeping the OAuth
	// check made the code read like there was a parallel auth path
	// that no longer exists.)
	for _, p := range Providers {
		if p.GetKey(&c.API) != "" {
			return nil
		}
	}

	// Legacy API key
	if c.API.APIKey != "" {
		return nil
	}

	// Ollama doesn't require API key for local server
	if c.API.GetActiveProvider() == "ollama" {
		return nil
	}

	return ErrMissingAuth
}

func validatePlanVerifyPolicy(policy PlanVerifyPolicyConfig) error {
	if !policy.Enabled {
		return nil
	}

	normalize := func(values []string) []string {
		out := make([]string, 0, len(values))
		for _, v := range values {
			v = strings.TrimSpace(strings.ToLower(v))
			if v == "" {
				continue
			}
			out = append(out, v)
		}
		return out
	}

	globalAllow := normalize(policy.AllowContains)
	globalDeny := normalize(policy.DenyContains)
	denySet := make(map[string]bool, len(globalDeny))
	for _, d := range globalDeny {
		denySet[d] = true
	}
	for _, a := range globalAllow {
		if denySet[a] {
			return fmt.Errorf("plan.verify_policy conflict: %q is in both allow_contains and deny_contains", a)
		}
	}

	for profile, cfg := range policy.Profiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return fmt.Errorf("plan.verify_policy.profiles contains an empty profile key")
		}
		pAllow := normalize(cfg.AllowContains)
		pDeny := normalize(cfg.DenyContains)
		pDenySet := make(map[string]bool, len(pDeny))
		for _, d := range pDeny {
			pDenySet[d] = true
		}
		for _, a := range pAllow {
			if pDenySet[a] {
				return fmt.Errorf("plan.verify_policy.profiles.%s conflict: %q is in both allow_contains and deny_contains", profile, a)
			}
		}
	}

	return nil
}

// Error types for configuration validation.
type ConfigError string

func (e ConfigError) Error() string {
	return string(e)
}

// ErrMissingAuth is built dynamically from the provider registry.
var ErrMissingAuth = newMissingAuthError()

func newMissingAuthError() ConfigError {
	var envVars []string
	for _, p := range Providers {
		if !p.KeyOptional && len(p.EnvVars) > 0 {
			envVars = append(envVars, p.EnvVars[0])
		}
	}
	return ConfigError(fmt.Sprintf(
		"missing authentication: set %s, or use /login <provider> <api_key>",
		strings.Join(envVars, ", ")))
}

// GetConfigPath returns the path to the config file (exported for external use).
func GetConfigPath() string {
	return getConfigPath()
}

// Save saves the configuration to the config file.
func (c *Config) Save() error {
	configPath := getConfigPath()
	if configPath == "" {
		return fmt.Errorf("could not determine config path")
	}

	// Ensure config directory exists (0700 for security - only owner can access)
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal config to YAML with proper ordering
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file atomically (write to temp file then rename)
	// Use 0600 permissions for security - config may contain API keys
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	// Rename temp file to actual config file (atomic on POSIX systems)
	if err := os.Rename(tmpPath, configPath); err != nil {
		// If rename fails, try direct write (Windows filesystem)
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			return fmt.Errorf("failed to write config file: %w", err)
		}
		// Clean up the .tmp file that rename couldn't move.
		os.Remove(tmpPath)
	}

	return nil
}

// expandTilde replaces a leading "~" with the user's home directory.
// filepath.Abs does NOT expand tildes, so paths like "~/projects" would
// resolve relative to cwd instead of HOME without this pre-pass.
func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(homeDir, path[1:])
	}
	return path
}

// IsWorkDirAllowed checks if a working directory is in the allowed list.
func (c *Config) IsWorkDirAllowed(workDir string) bool {
	// Clean and resolve the path
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return false
	}
	absWorkDir = filepath.Clean(absWorkDir)

	for _, dir := range c.Tools.AllowedDirs {
		absDir, err := filepath.Abs(expandTilde(dir))
		if err != nil {
			continue
		}
		absDir = filepath.Clean(absDir)

		// Check if workDir is within this allowed dir
		if absWorkDir == absDir || strings.HasPrefix(absWorkDir, absDir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AddAllowedDir adds a directory to the allowed list if not already present.
func (c *Config) AddAllowedDir(dir string) bool {
	absDir, err := filepath.Abs(expandTilde(dir))
	if err != nil {
		return false
	}
	absDir = filepath.Clean(absDir)

	// Check if already in list
	for _, existing := range c.Tools.AllowedDirs {
		absExisting, err := filepath.Abs(expandTilde(existing))
		if err != nil {
			continue
		}
		if filepath.Clean(absExisting) == absDir {
			return false // Already exists
		}
	}

	c.Tools.AllowedDirs = append(c.Tools.AllowedDirs, absDir)
	return true
}
