package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"

	"gopkg.in/yaml.v3"
)

var configSaveMu sync.Mutex

const (
	maxConfigFileBytes     = MaxConfigFileBytes
	maxExpandedConfigBytes = 8 << 20

	// legacyDefaultModelRoundTimeout was serialized into full user configs by
	// releases whose five-minute hard cap regularly killed healthy, actively
	// streaming reasoning rounds. The runtime default is now deliberately more
	// generous, but YAML overlays otherwise keep resurrecting this old default
	// forever. Migrate only the exact historical user-level value; project
	// config is loaded afterwards and remains an explicit, respected override.
	legacyDefaultModelRoundTimeout = 5 * time.Minute
	// Old generated full configs serialized the generic Anthropic constructor
	// defaults here, unintentionally overriding the newer provider-specific
	// watchdogs forever (GLM/Kimi/DeepSeek/MiniMax need more patient values).
	// Treat only the exact historical pair as generated legacy state; either
	// value customized independently remains an explicit user choice.
	legacyDefaultHTTPTimeout       = 120 * time.Second
	legacyDefaultStreamIdleTimeout = 30 * time.Second
	// Old generated configs serialized the one-minute planning watchdog. That
	// value is shorter than the model-round cap and therefore kills healthy
	// planning responses first. Zero now means "follow model-round timeout".
	legacyDefaultPlanningTimeout = 60 * time.Second
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
		cfg.savePath = configPath
	}
	migrateLegacyModelRoundTimeout(cfg)
	migrateLegacyProviderTimeouts(cfg)
	migrateLegacyPlanningTimeout(cfg)

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

// LoadFrom loads an explicit global config file, then applies the same
// environment and per-project overlays as Load. Unlike the optional default
// file, an explicit path must exist and parse successfully. Subsequent Save
// calls (including on Clone results) write back to this same file.
func LoadFrom(path string) (*Config, error) {
	path = strings.TrimSpace(expandTilde(path))
	if path == "" {
		return nil, fmt.Errorf("config path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %q: %w", path, err)
	}

	cfg := DefaultConfig()
	if err := loadFromFile(cfg, absolute); err != nil {
		return nil, fmt.Errorf("load config %q: %w", absolute, err)
	}
	cfg.savePath = absolute
	migrateLegacyModelRoundTimeout(cfg)
	migrateLegacyProviderTimeouts(cfg)
	migrateLegacyPlanningTimeout(cfg)
	loadFromEnv(cfg)
	loadProjectConfig(cfg)
	migrateLegacyKimiModelName(cfg)
	return cfg, nil
}

// migrateLegacyModelRoundTimeout upgrades the old generated five-minute user
// default in memory. It intentionally runs after the global/--config file and
// before the project overlay: repositories may still choose a tighter timeout,
// while users who simply carried forward a generated legacy config receive the
// fixed default without having to discover and edit YAML by hand.
func migrateLegacyModelRoundTimeout(cfg *Config) {
	if cfg == nil || cfg.Tools.ModelRoundTimeout != legacyDefaultModelRoundTimeout {
		return
	}
	cfg.Tools.ModelRoundTimeout = DefaultModelRoundTimeout
	logging.Info("upgraded legacy model round timeout",
		"from", legacyDefaultModelRoundTimeout,
		"to", DefaultModelRoundTimeout)
}

// migrateLegacyProviderTimeouts releases the exact old generated global pair
// back to provider-specific defaults. It runs before project overlays, so a
// repository that deliberately selects these values still keeps them.
func migrateLegacyProviderTimeouts(cfg *Config) {
	if cfg == nil ||
		cfg.API.Retry.HTTPTimeout != legacyDefaultHTTPTimeout ||
		cfg.API.Retry.StreamIdleTimeout != legacyDefaultStreamIdleTimeout {
		return
	}
	cfg.API.Retry.HTTPTimeout = 0
	cfg.API.Retry.StreamIdleTimeout = 0
	logging.Info("released legacy global provider timeouts to provider defaults",
		"http_timeout", legacyDefaultHTTPTimeout,
		"stream_idle_timeout", legacyDefaultStreamIdleTimeout)
}

// migrateLegacyPlanningTimeout releases only the exact historical generated
// global default. It runs before the project overlay, so a repository that
// deliberately requests a one-minute planning cap keeps that override.
func migrateLegacyPlanningTimeout(cfg *Config) {
	if cfg == nil || cfg.Plan.PlanningTimeout != legacyDefaultPlanningTimeout {
		return
	}
	cfg.Plan.PlanningTimeout = 0
	logging.Info("released legacy planning timeout to model-round default",
		"from", legacyDefaultPlanningTimeout)
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
	// A repository-controlled config must not opt itself into reading the
	// user's cross-project memory. Treat the user-level decision loaded by Load
	// as a capability ceiling: project config may narrow it, never widen it.
	userAllowsGlobal := cfg.Memory.AllowGlobal
	if err := loadFromFile(cfg, projectConfigPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load project config: %w", err)
		}
	} else if configDefinesModelRoundTimeout(projectConfigPath) {
		cfg.modelRoundTimeoutProjectPath = projectConfigPath
	}
	cfg.Memory.AllowGlobal = userAllowsGlobal && cfg.Memory.AllowGlobal

	return cfg, nil
}

// loadProjectConfig attempts to find and load .gokin/config.yaml from the current directory upward.
func loadProjectConfig(cfg *Config) {
	if cfg != nil && !cfg.modelRoundTimeoutGlobalTracked {
		cfg.modelRoundTimeoutGlobalValue = cfg.Tools.ModelRoundTimeout
		cfg.modelRoundTimeoutGlobalTracked = true
	}
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
			userAllowsGlobal := cfg.Memory.AllowGlobal
			userTrustedWorkspaces := append([]string(nil), cfg.Hooks.TrustedWorkspaces...)
			if err := loadFromFile(cfg, projectConfig); err != nil {
				slog.Warn("failed to load project config", "path", projectConfig, "error", err)
			} else if configDefinesModelRoundTimeout(projectConfig) {
				cfg.modelRoundTimeoutProjectPath = projectConfig
			}
			// allow_global is a user trust decision, not a capability a repository
			// may grant itself. A project may explicitly disable it, but cannot
			// widen a user-level false into cross-project prompt injection.
			cfg.Memory.AllowGlobal = userAllowsGlobal && cfg.Memory.AllowGlobal
			// Workspace trust is also user-owned authority. A repository may
			// configure hook behavior after the user trusts it, but it may not
			// add its own path to the trust ledger and thereby activate
			// executable hooks or SKILL.md allowed-tools grants.
			cfg.Hooks.TrustedWorkspaces = userTrustedWorkspaces
			return
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}
}

// explicitConfigPath pins the process to the file the launcher was given with
// --config. LoadFrom already routes cfg.Save() back to that file, but the
// wizard and the user-facing "saved to <path>" messages resolve the path on
// their own — without this they would name the DEFAULT location, so a run that
// hit the first-run wizard wrote the API key to a file the run never reads and
// then failed again with the same missing-credentials error.
var (
	explicitConfigPath   string
	explicitConfigPathMu sync.RWMutex
)

// SetExplicitConfigPath binds every default-path resolution in this process to
// an operator-supplied config file. An empty value restores the default lookup.
func SetExplicitConfigPath(path string) {
	path = strings.TrimSpace(expandTilde(path))
	if path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
	}
	explicitConfigPathMu.Lock()
	explicitConfigPath = path
	explicitConfigPathMu.Unlock()
}

func configuredExplicitPath() string {
	explicitConfigPathMu.RLock()
	defer explicitConfigPathMu.RUnlock()
	return explicitConfigPath
}

// getConfigPath returns the path to the config file.
func getConfigPath() string {
	if explicit := configuredExplicitPath(); explicit != "" {
		return explicit
	}

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
	data, err := ReadConfigFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return configNotExistError(path)
		}
		return fmt.Errorf("read config file %s: %w", path, err)
	}

	// Warn if config file has overly permissive permissions
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
		slog.Warn("config file has insecure permissions",
			"path", path,
			"mode", fmt.Sprintf("%04o", info.Mode().Perm()),
			"recommended", "0600")
	}

	// Expand only safe environment variables in the config file
	expanded := expandSafeEnvVars(string(data))
	if len(expanded) > maxExpandedConfigBytes {
		return fmt.Errorf("expanded config file %s exceeds %d-byte limit", path, maxExpandedConfigBytes)
	}

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
	// Priority: GOKIN_API_KEY > GOKIN_GLM_KEY > GLM_API_KEY
	//
	// Pre-v0.80.18 also pulled in GEMINI_API_KEY as a final fallback —
	// leftover from v0.65 when Gemini was removed. The Gemini key would
	// silently land in cfg.API.APIKey, then fail auth at runtime with
	// a cryptic "401" because no current provider accepts Gemini-format
	// keys. Better to fall through to "no key configured" so the user
	// gets a clear /doctor signal and runs /login with the right key.
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
	}

	if model := os.Getenv("GOKIN_MODEL"); model != "" {
		cfg.Model.Name = model
	}

	if backend := os.Getenv("GOKIN_BACKEND"); backend != "" {
		cfg.API.Backend = backend
	}
	if mode := strings.TrimSpace(os.Getenv("GOKIN_ENGINE_MODE")); mode != "" {
		cfg.Engine.Mode = mode
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if err := ValidateRetryConfig(c); err != nil {
		return err
	}
	engineMode := strings.ToLower(strings.TrimSpace(c.Engine.Mode))
	if engineMode != "auto" && engineMode != "tools" && engineMode != "hybrid" {
		return fmt.Errorf("invalid engine.mode %q: expected auto, tools, or hybrid", c.Engine.Mode)
	}
	if c.Engine.REPL.CellTimeout <= 0 {
		return fmt.Errorf("engine.repl.cell_timeout must be > 0")
	}
	if c.Engine.REPL.MaxCodeBytes < 1024 || c.Engine.REPL.MaxCodeBytes > 1024*1024 {
		return fmt.Errorf("engine.repl.max_code_bytes must be between 1024 and 1048576")
	}
	if c.Engine.REPL.MaxResponseBytes < 64*1024 || c.Engine.REPL.MaxResponseBytes > 16*1024*1024 {
		return fmt.Errorf("engine.repl.max_response_bytes must be between 65536 and 16777216")
	}
	if c.Engine.REPL.MaxMemoryBytes < 64*1024*1024 || c.Engine.REPL.MaxMemoryBytes > 2*1024*1024*1024 {
		return fmt.Errorf("engine.repl.max_memory_bytes must be between 67108864 and 2147483648")
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
	if c.Tools.ModelRoundTimeout < 0 {
		return fmt.Errorf("tools.model_round_timeout must be >= 0")
	}
	if c.Plan.PlanningTimeout < 0 {
		return fmt.Errorf("plan.planning_timeout must be >= 0")
	}
	if c.Plan.DefaultStepTimeout < 0 {
		return fmt.Errorf("plan.default_step_timeout must be >= 0")
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
	configPath := c.savePath
	if configPath == "" {
		configPath = getConfigPath()
	}
	if configPath == "" {
		return fmt.Errorf("could not determine config path")
	}

	// Marshal config to YAML with proper ordering. When a repository overlay
	// owns the timeout, retain the pre-project value in the user-wide file;
	// otherwise a harmless UI/settings save in one repository would leak its
	// local timeout into every other workspace.
	configToSave := c
	if c.modelRoundTimeoutProjectPath != "" && c.modelRoundTimeoutGlobalTracked {
		configToSave = c.Clone()
		configToSave.Tools.ModelRoundTimeout = c.modelRoundTimeoutGlobalValue
	}
	data, err := yaml.Marshal(configToSave)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := WriteConfigFile(configPath, data); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// SaveModelRoundTimeout persists the scalar into the configuration layer that
// supplied it. A project overlay wins over the global file at load time, so
// writing only the global file would falsely report success while the old
// project value returned on restart.
func (c *Config) SaveModelRoundTimeout(timeout time.Duration) error {
	if c == nil {
		return fmt.Errorf("cannot save model round timeout on nil config")
	}
	c.Tools.ModelRoundTimeout = timeout
	if c.modelRoundTimeoutProjectPath == "" {
		c.modelRoundTimeoutGlobalValue = timeout
		c.modelRoundTimeoutGlobalTracked = true
	}
	path := c.ModelRoundTimeoutConfigPath()
	if path == "" {
		return fmt.Errorf("could not determine config path")
	}

	return UpdateConfigFile(path, func(existing []byte) ([]byte, error) {
		var document yaml.Node
		if len(existing) > 0 {
			if err := yaml.Unmarshal(existing, &document); err != nil {
				return nil, fmt.Errorf("parse project config %q: %w", path, err)
			}
		}
		root, err := ensureYAMLMappingDocument(&document)
		if err != nil {
			return nil, fmt.Errorf("update project config %q: %w", path, err)
		}
		toolsNode, err := ensureYAMLMappingValue(root, "tools")
		if err != nil {
			return nil, fmt.Errorf("update project config %q: %w", path, err)
		}
		setYAMLScalar(toolsNode, "model_round_timeout", timeout.String())
		data, err := yaml.Marshal(&document)
		if err != nil {
			return nil, fmt.Errorf("marshal project config %q: %w", path, err)
		}
		return data, nil
	})
}

// ModelRoundTimeoutConfigPath returns the layer that owns the effective
// timeout and therefore receives runtime /timeout updates.
func (c *Config) ModelRoundTimeoutConfigPath() string {
	if c == nil {
		return ""
	}
	if c.modelRoundTimeoutProjectPath != "" {
		return c.modelRoundTimeoutProjectPath
	}
	if c.savePath != "" {
		return c.savePath
	}
	return getConfigPath()
}

func configDefinesModelRoundTimeout(path string) bool {
	data, err := ReadConfigFile(path)
	if err != nil {
		return false
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) == 0 {
		return false
	}
	root := document.Content[0]
	toolsNode := yamlMappingValue(root, "tools")
	return yamlMappingValue(toolsNode, "model_round_timeout") != nil
}

func ensureYAMLMappingDocument(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, fmt.Errorf("root must be one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("root must be a mapping")
	}
	return root, nil
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func ensureYAMLMappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	if existing := yamlMappingValue(mapping, key); existing != nil {
		if existing.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s must be a mapping", key)
		}
		return existing, nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return valueNode, nil
}

func setYAMLScalar(mapping *yaml.Node, key, value string) {
	if existing := yamlMappingValue(mapping, key); existing != nil {
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		existing.Content = nil
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
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
