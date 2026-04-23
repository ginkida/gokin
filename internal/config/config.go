package config

import "time"

// Config represents the main application configuration.
type Config struct {
	API           APIConfig           `yaml:"api"`
	Model         ModelConfig         `yaml:"model"`
	Tools         ToolsConfig         `yaml:"tools"`
	UI            UIConfig            `yaml:"ui"`
	Context       ContextConfig       `yaml:"context"`
	Permission    PermissionConfig    `yaml:"permission"`
	Plan          PlanConfig          `yaml:"plan"`
	DoneGate      DoneGateConfig      `yaml:"done_gate"`
	Hooks         HooksConfig         `yaml:"hooks"`
	Web           WebConfig           `yaml:"web"`
	Session       SessionConfig       `yaml:"session"`
	Memory        MemoryConfig        `yaml:"memory"`
	Logging       LoggingConfig       `yaml:"logging"`
	Audit         AuditConfig         `yaml:"audit"`
	RateLimit     RateLimitConfig     `yaml:"rate_limit"`
	Cache         CacheConfig         `yaml:"cache"`
	Watcher       WatcherConfig       `yaml:"watcher"`
	DiffPreview   DiffPreviewConfig   `yaml:"diff_preview"`
	MCP           MCPConfig           `yaml:"mcp"`
	Update        UpdateConfig        `yaml:"update"`
	SessionMemory SessionMemoryConfig `yaml:"session_memory"`
	Completion    CompletionConfig    `yaml:"completion"`

	// Runtime version information
	Version string `yaml:"-"`
}

// CompletionConfig controls how the final response to the user is
// shaped after tool execution and gate checks complete.
type CompletionConfig struct {
	// EvidenceFooter appends a deterministic `Changed: X · Verified: Y`
	// block to the response when the turn touched files. Default: true.
	// Turn off only when the extra line is noisy (e.g. embedded in a
	// piped CLI workflow that parses raw model output).
	EvidenceFooter bool `yaml:"evidence_footer"`
}

// APIConfig holds API-related settings.
type APIConfig struct {
	// Legacy field - for backwards compatibility
	APIKey string `yaml:"api_key,omitempty"`

	// Separate keys for each provider
	GLMKey     string `yaml:"glm_key,omitempty"`
	MiniMaxKey string `yaml:"minimax_key,omitempty"`
	KimiKey    string `yaml:"kimi_key,omitempty"`
	OllamaKey  string `yaml:"ollama_key,omitempty"` // Optional, for remote Ollama servers with auth

	// Ollama server URL (default: http://localhost:11434)
	OllamaBaseURL string `yaml:"ollama_base_url,omitempty"`

	// Active provider: glm, minimax, kimi, ollama (default: glm)
	ActiveProvider string `yaml:"active_provider"`

	// Backend: legacy alias for ActiveProvider
	Backend string `yaml:"backend,omitempty"`

	// Retry configuration for API calls
	Retry RetryConfig `yaml:"retry"`
}

// HasOAuthToken is retained as a stub: OAuth flows were removed in v0.65.0,
// so no provider currently has OAuth credentials. Kept so call sites in the
// provider registry still compile.
func (c *APIConfig) HasOAuthToken(provider string) bool {
	return false
}

// GetActiveKey returns the API key for the active provider.
func (c *APIConfig) GetActiveKey() string {
	provider := c.GetActiveProvider()
	if p := GetProvider(provider); p != nil {
		if key := p.GetKey(c); key != "" {
			return key
		}
		if p.UsesLegacyKey {
			return c.APIKey
		}
		return ""
	}
	// Fallback to legacy APIKey field
	return c.APIKey
}

// GetActiveProvider returns the active provider name.
func (c *APIConfig) GetActiveProvider() string {
	if c.ActiveProvider != "" {
		return c.ActiveProvider
	}
	if c.Backend != "" {
		return c.Backend
	}
	return "glm"
}

// HasProvider checks if a provider has an API key configured.
// Note: Ollama doesn't require an API key for local servers.
func (c *APIConfig) HasProvider(provider string) bool {
	p := GetProvider(provider)
	if p == nil {
		return false
	}
	if p.KeyOptional && !p.HasOAuth {
		return true // e.g. ollama — no key needed
	}
	if p.KeyOptional && p.HasOAuth {
		return c.HasOAuthToken(provider)
	}
	if p.GetKey(c) != "" {
		return true
	}
	if p.HasOAuth && c.HasOAuthToken(provider) {
		return true
	}
	if p.UsesLegacyKey && c.APIKey != "" && c.GetActiveProvider() == provider {
		return true
	}
	return false
}

// SetProviderKey sets the API key for a specific provider.
func (c *APIConfig) SetProviderKey(provider, key string) {
	if p := GetProvider(provider); p != nil {
		p.SetKey(c, key)
	}
}

// RetryConfig holds retry settings for API calls.
type RetryConfig struct {
	MaxRetries        int                            `yaml:"max_retries"`         // Maximum number of retry attempts (default: 3)
	RetryDelay        time.Duration                  `yaml:"retry_delay"`         // Initial delay between retries (default: 1s)
	HTTPTimeout       time.Duration                  `yaml:"http_timeout"`        // HTTP request timeout (default: 120s)
	StreamIdleTimeout time.Duration                  `yaml:"stream_idle_timeout"` // Max pause between SSE chunks (0 = provider default)
	Providers         map[string]ProviderRetryConfig `yaml:"providers,omitempty"` // Per-provider timeout overrides (e.g. glm/minimax/kimi).
}

// ProviderRetryConfig holds retry timeout overrides for a specific provider.
type ProviderRetryConfig struct {
	HTTPTimeout       time.Duration `yaml:"http_timeout,omitempty"`        // Provider-specific HTTP timeout
	StreamIdleTimeout time.Duration `yaml:"stream_idle_timeout,omitempty"` // Provider-specific stream idle timeout
}

// ModelConfig holds model-related settings.
type ModelConfig struct {
	// New fields for simplified configuration
	Preset   string `yaml:"preset"`   // Model preset: coding, fast, balanced, creative
	Provider string `yaml:"provider"` // Model provider: gemini, glm, auto (default: auto)

	// Existing fields (manual configuration)
	Name            string  `yaml:"name"`
	Temperature     float32 `yaml:"temperature"`
	MaxOutputTokens int32   `yaml:"max_output_tokens"`
	// Custom API endpoint override (optional)
	// If set, overrides the default BaseURL for the model
	CustomBaseURL string `yaml:"custom_base_url"`

	// Extended Thinking (Anthropic API feature)
	EnableThinking bool  `yaml:"enable_thinking"` // Enable extended thinking mode
	ThinkingBudget int32 `yaml:"thinking_budget"` // Max tokens for thinking (0 = disabled)

	// Fallback providers to try when the primary provider fails
	FallbackProviders []string `yaml:"fallback_providers"`

	// Maximum number of clients kept in the connection pool (default: 5)
	MaxPoolSize int `yaml:"max_pool_size"`

	// ForceWeakOptimizations applies weak-tier agent behavior (lower self-review
	// threshold, weak-model guidance injection, re-injection after compaction)
	// regardless of the model's inferred capability tier. Useful for Strong-tier
	// models that still benefit from extra safeguards, e.g. GLM 5.x in complex
	// multi-step tasks where stronger self-review reduces silent errors.
	ForceWeakOptimizations bool `yaml:"force_weak_optimizations,omitempty"`
}

// ToolsConfig holds tool-related settings.
type ToolsConfig struct {
	Timeout           time.Duration          `yaml:"timeout"`
	ModelRoundTimeout time.Duration          `yaml:"model_round_timeout"` // Hard timeout for a single model round.
	Bash              BashConfig             `yaml:"bash"`
	DeltaCheck        DeltaCheckConfig       `yaml:"delta_check"`
	SmartValidation   SmartValidationConfig  `yaml:"smart_validation"`
	AllowedDirs       []string               `yaml:"allowed_dirs"`       // Additional allowed directories (besides workDir)
	Formatters        map[string]string      `yaml:"formatters"`         // ext → command, e.g. {".py": "black"}
	ProactiveContext  ProactiveContextConfig `yaml:"proactive_context"`  // Auto-append related files to Read results.

	// KimiToolBudget caps the number of tool calls Kimi can issue per user
	// turn before the executor synthesizes "budget reached" responses and
	// forces the model to finalize. 0 disables the cap; values <10 are
	// clamped up by the executor. Applies only to kimi-family models —
	// GLM/MiniMax/Ollama are unaffected. Default: 40.
	KimiToolBudget int `yaml:"kimi_tool_budget"`

	// RequireReadBeforeEdit enforces the "model must Read before Edit"
	// invariant at tool level. When true (default), Edit returns an error
	// if the target file has no active read record — prevents blind edits
	// based on grep/glob snippets. Disable for workflows that programma-
	// tically edit without prior inspection.
	RequireReadBeforeEdit bool `yaml:"require_read_before_edit"`
}

// ProactiveContextConfig controls auto-inclusion of sibling/test files
// when the agent reads a source file. Mimics Claude Code's "already-knows-
// the-neighbourhood" feel without requiring explicit tool calls per file.
type ProactiveContextConfig struct {
	Enabled         bool `yaml:"enabled"`           // Append related-files block to Read results (default: true)
	MaxFiles        int  `yaml:"max_files"`         // How many sibling/related files to include (default: 3)
	MaxLinesPerFile int  `yaml:"max_lines_per_file"` // Preview length per related file (default: 40)
}

// BashConfig holds bash tool settings.
type BashConfig struct {
	Sandbox         bool     `yaml:"sandbox"`
	BlockedCommands []string `yaml:"blocked_commands"`
}

// DeltaCheckConfig controls lightweight post-edit verification.
type DeltaCheckConfig struct {
	Enabled    bool          `yaml:"enabled"`     // Enable/disable automatic delta-check after mutating tools
	Timeout    time.Duration `yaml:"timeout"`     // Per-check timeout
	WarnOnly   bool          `yaml:"warn_only"`   // If true, report failures but do not block further mutating operations
	MaxModules int           `yaml:"max_modules"` // Maximum distinct module roots to check per cycle
}

// SmartValidationConfig controls semantic validators, context enrichment, and self-review.
type SmartValidationConfig struct {
	Enabled             bool `yaml:"enabled"`               // Enable semantic validators and context enrichment (default: true)
	SelfReviewThreshold int  `yaml:"self_review_threshold"` // Mutated file count that triggers self-review (default: 4, 0=disabled)
}

// UIConfig holds UI-related settings.
type UIConfig struct {
	StreamOutput        bool   `yaml:"stream_output"`
	MarkdownRendering   bool   `yaml:"markdown_rendering"`
	ShowToolCalls       bool   `yaml:"show_tool_calls"`
	ShowTokenUsage      bool   `yaml:"show_token_usage"`
	Theme               string `yaml:"theme"`         // Theme name: dark, light, sepia, cyber, forest, ocean, monokai, dracula, high_contrast
	ShowWelcome         bool   `yaml:"show_welcome"`  // Show welcome message on first launch
	HintsEnabled        bool   `yaml:"hints_enabled"` // Show contextual hints for features
	CompactMode         bool   `yaml:"compact_mode"`
	Bell                bool   `yaml:"bell"`                 // Terminal bell on prompts (default: true)
	NativeNotifications bool   `yaml:"native_notifications"` // macOS Notification Center (default: false)
}

// ContextConfig holds context management settings.
type ContextConfig struct {
	MaxInputTokens       int     `yaml:"max_input_tokens"`       // 0 = use model default
	WarningThreshold     float64 `yaml:"warning_threshold"`      // 0.8 = warn at 80%
	SummarizationRatio   float64 `yaml:"summarization_ratio"`    // 0.5 = summarize to 50%
	ToolResultMaxChars   int     `yaml:"tool_result_max_chars"`  // Max chars for tool results
	AutoCompactThreshold float64 `yaml:"auto_compact_threshold"` // 0.75 = compact at 75% usage
	EnableAutoSummary    bool    `yaml:"enable_auto_summary"`    // Enable auto-summarization
}

// PermissionConfig holds permission system settings.
type PermissionConfig struct {
	Enabled       bool              `yaml:"enabled"`        // Enable/disable permission system
	DefaultPolicy string            `yaml:"default_policy"` // Default policy: "allow", "ask", "deny"
	Rules         map[string]string `yaml:"rules"`          // Per-tool rules
}

// PlanConfig holds plan mode settings.
type PlanConfig struct {
	Enabled                      bool                   `yaml:"enabled"`                         // Enable/disable plan mode
	RequireApproval              bool                   `yaml:"require_approval"`                // Require user approval before execution
	AutoDetect                   bool                   `yaml:"auto_detect"`                     // Auto-trigger planning for complex tasks
	ClearContext                 bool                   `yaml:"clear_context"`                   // Clear context before plan execution
	DelegateSteps                bool                   `yaml:"delegate_steps"`                  // Run each step in isolated sub-agent
	WorkspaceIsolation           bool                   `yaml:"workspace_isolation"`             // Use isolated workspaces for safe read-only sub-agents
	AbortOnStepFailure           bool                   `yaml:"abort_on_step_failure"`           // Stop plan on step failure
	PlanningTimeout              time.Duration          `yaml:"planning_timeout"`                // Timeout for LLM plan generation
	DefaultStepTimeout           time.Duration          `yaml:"default_step_timeout"`            // Default timeout per step (0 = 5min)
	UseLLMExpansion              bool                   `yaml:"use_llm_expansion"`               // Use LLM for dynamic plan expansion
	Algorithm                    string                 `yaml:"algorithm"`                       // Tree search algorithm: beam, mcts, astar
	RequireExpectedArtifactPaths bool                   `yaml:"require_expected_artifact_paths"` // Fail-closed completion when mutating step has no explicit artifact paths
	VerifyPolicy                 PlanVerifyPolicyConfig `yaml:"verify_policy"`                   // Verification command safety policy
}

// PlanVerifyPolicyConfig controls safety filtering for plan step verify_commands.
type PlanVerifyPolicyConfig struct {
	Enabled                   bool                                     `yaml:"enabled"`                     // Enable policy filtering
	RequireVerificationIntent bool                                     `yaml:"require_verification_intent"` // Require command to look like validation/test/build/lint/check
	AllowContains             []string                                 `yaml:"allow_contains"`              // Optional allowlist markers (substring match)
	DenyContains              []string                                 `yaml:"deny_contains"`               // Denylist markers (substring match)
	Profiles                  map[string]PlanVerifyPolicyProfileConfig `yaml:"profiles"`                    // Per-stack overrides (go,node,python,rust,java,cmake,bazel,make,php,default)
}

// PlanVerifyPolicyProfileConfig overrides policy markers for a stack profile.
type PlanVerifyPolicyProfileConfig struct {
	AllowContains []string `yaml:"allow_contains"` // Additional allowlist markers
	DenyContains  []string `yaml:"deny_contains"`  // Additional denylist markers
}

// DoneGateConfig holds hard finalization gate settings.
type DoneGateConfig struct {
	Enabled         bool          `yaml:"enabled"`           // Enable/disable done-gate enforcement
	Mode            string        `yaml:"mode"`              // Gate mode: normal or strict
	FailClosed      bool          `yaml:"fail_closed"`       // Block finalization if no checks can be prepared
	CheckTimeout    time.Duration `yaml:"check_timeout"`     // Timeout per verification check
	AutoFixAttempts int           `yaml:"auto_fix_attempts"` // Number of autonomous fix rounds before blocking
}

// HooksConfig holds hooks system settings.
type HooksConfig struct {
	Enabled bool         `yaml:"enabled"` // Enable/disable hooks
	Hooks   []HookConfig `yaml:"hooks"`   // List of configured hooks
}

// HookConfig represents a single hook configuration.
type HookConfig struct {
	Name        string `yaml:"name"`          // Human-readable name
	Type        string `yaml:"type"`          // Hook type: pre_tool, post_tool, on_error, on_start, on_exit
	ToolName    string `yaml:"tool_name"`     // Tool to trigger on (empty = all)
	Command     string `yaml:"command"`       // Shell command to execute
	Enabled     bool   `yaml:"enabled"`       // Whether hook is active
	Condition   string `yaml:"condition"`     // Condition: always, if_previous_success, if_previous_failure
	FailOnError bool   `yaml:"fail_on_error"` // When true and hook fails, cancel tool execution
	DependsOn   string `yaml:"depends_on"`    // Name of another hook that must complete first
}

// WebConfig holds web tool settings.
type WebConfig struct {
	SearchProvider string `yaml:"search_provider"` // Search provider: "serpapi", "google"
	SearchAPIKey   string `yaml:"search_api_key"`  // API key for search provider
	GoogleCX       string `yaml:"google_cx"`       // Google Custom Search Engine ID
}

// SessionConfig holds session persistence settings.
type SessionConfig struct {
	Enabled      bool          `yaml:"enabled"`       // Enable session persistence
	SaveInterval time.Duration `yaml:"save_interval"` // Auto-save interval (default: 2m)
	AutoLoad     bool          `yaml:"auto_load"`     // Auto-load last session on startup
}

// SessionMemoryConfig holds automatic session memory extraction settings.
type SessionMemoryConfig struct {
	Enabled                 bool `yaml:"enabled"`                    // Enable session memory extraction
	MinTokensToInit         int  `yaml:"min_tokens_to_init"`         // Tokens before first extraction (default: 10000)
	MinTokensBetweenUpdates int  `yaml:"min_tokens_between_updates"` // Token delta for re-extraction (default: 5000)
	ToolCallsBetweenUpdates int  `yaml:"tool_calls_between_updates"` // Tool calls before re-extraction (default: 3)
}

// MemoryConfig holds memory system settings.
type MemoryConfig struct {
	Enabled    bool `yaml:"enabled"`     // Enable/disable memory system
	MaxEntries int  `yaml:"max_entries"` // Maximum number of memory entries
	AutoInject bool `yaml:"auto_inject"` // Auto-inject memories into system prompt
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `yaml:"level"` // Logging level: debug, info, warn, error
}

// AuditConfig holds audit log settings.
type AuditConfig struct {
	Enabled       bool `yaml:"enabled"`        // Enable/disable audit logging
	MaxEntries    int  `yaml:"max_entries"`    // Maximum entries per session
	MaxResultLen  int  `yaml:"max_result_len"` // Maximum result length to store
	RetentionDays int  `yaml:"retention_days"` // Days to retain audit logs
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	Enabled           bool  `yaml:"enabled"`             // Enable/disable rate limiting
	RequestsPerMinute int   `yaml:"requests_per_minute"` // Max requests per minute
	TokensPerMinute   int64 `yaml:"tokens_per_minute"`   // Max tokens per minute
	BurstSize         int   `yaml:"burst_size"`          // Burst size for rate limiting
}

// CacheConfig holds search cache settings.
type CacheConfig struct {
	Enabled  bool          `yaml:"enabled"`  // Enable/disable caching
	Capacity int           `yaml:"capacity"` // Maximum cache entries
	TTL      time.Duration `yaml:"ttl"`      // Time to live for cache entries
}

// WatcherConfig holds file watcher settings.
type WatcherConfig struct {
	Enabled    bool `yaml:"enabled"`     // Enable/disable file watching
	DebounceMs int  `yaml:"debounce_ms"` // Debounce time in milliseconds
	MaxWatches int  `yaml:"max_watches"` // Maximum number of watched paths
}

// DiffPreviewConfig holds diff preview settings.
type DiffPreviewConfig struct {
	Enabled bool `yaml:"enabled"` // Enable/disable diff preview for write/edit operations
}

// MCPConfig holds MCP (Model Context Protocol) settings.
type MCPConfig struct {
	Enabled             bool              `yaml:"enabled"`               // Enable/disable MCP support
	Servers             []MCPServerConfig `yaml:"servers"`               // MCP server configurations
	HealthCheckInterval time.Duration     `yaml:"health_check_interval"` // Interval for health checks (0 = disabled)
}

// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
	Name        string            `yaml:"name"`                  // Unique identifier
	Transport   string            `yaml:"transport"`             // "stdio" or "http"
	Command     string            `yaml:"command,omitempty"`     // For stdio: command to run
	Args        []string          `yaml:"args,omitempty"`        // For stdio: command arguments
	Env         map[string]string `yaml:"env,omitempty"`         // Additional env vars (supports ${VAR})
	URL         string            `yaml:"url,omitempty"`         // For http: server URL
	Headers     map[string]string `yaml:"headers,omitempty"`     // For http: custom headers
	AutoConnect bool              `yaml:"auto_connect"`          // Connect on startup
	Timeout     time.Duration     `yaml:"timeout,omitempty"`     // Request timeout
	MaxRetries  int               `yaml:"max_retries,omitempty"` // Retry count
	RetryDelay  time.Duration     `yaml:"retry_delay,omitempty"` // Between retries
	ToolPrefix  string            `yaml:"tool_prefix,omitempty"` // Prefix for tool names

	// PermissionLevel is the risk level applied to every tool exposed by this
	// server: "low" (never asks), "medium" (asks in caution mode, default),
	// "high" (always asks). Use "high" for untrusted 3rd-party servers so the
	// user sees a prompt before any tool runs; "low" for servers you fully
	// trust (e.g., an internal RAG service).
	//
	// Backwards compatibility: configs written before Sprint 11 (v0.69) have
	// no `permission_level` field. They parse as empty string, and
	// permission.ParseRiskLevel("") returns RiskMedium (same as the built-in
	// default for unknown tools). This preserves existing behavior for old
	// configs while letting users opt into tighter permissions per server.
	PermissionLevel string `yaml:"permission_level,omitempty"`
}

// UpdateConfig holds self-update settings.
type UpdateConfig struct {
	Enabled           bool          `yaml:"enabled"`            // Enable/disable auto-update system
	AutoCheck         bool          `yaml:"auto_check"`         // Check for updates on startup
	CheckInterval     time.Duration `yaml:"check_interval"`     // Interval between automatic checks
	AutoDownload      bool          `yaml:"auto_download"`      // Auto-download updates (not install)
	IncludePrerelease bool          `yaml:"include_prerelease"` // Include beta/rc versions
	Channel           string        `yaml:"channel"`            // Update channel: stable, beta, nightly
	GitHubRepo        string        `yaml:"github_repo"`        // GitHub repo for updates
	MaxBackups        int           `yaml:"max_backups"`        // Max backup versions to keep
	VerifyChecksum    bool          `yaml:"verify_checksum"`    // Verify downloaded file checksums
	NotifyOnly        bool          `yaml:"notify_only"`        // Only notify, don't prompt to install
	Timeout           time.Duration `yaml:"timeout"`            // HTTP request timeout (default: 30s)
}

// DefaultConfig returns the default configuration. Kimi Coding Plan is the
// default provider — gokin's primary target is active coding, and the
// Coding Plan endpoint (api.kimi.com/coding) serves kimi-for-coding (K2.6)
// with a 262K context window. Users with GLM/MiniMax/Ollama credentials
// can switch via /model or /provider at any time.
func DefaultConfig() *Config {
	return &Config{
		API: APIConfig{
			Backend: "kimi",
			Retry: RetryConfig{
				MaxRetries: DefaultMaxRetries,
				RetryDelay: DefaultRetryDelay,
			},
		},
		Model: ModelConfig{
			Name:            "kimi-for-coding", // Matches default backend "kimi"
			Temperature:     0.6,
			MaxOutputTokens: 32768,
			EnableThinking:  false, // Disabled by default
			ThinkingBudget:  0,     // 0 = disabled
			MaxPoolSize:     5,     // Default pool size
		},
		Tools: ToolsConfig{
			Timeout:           2 * time.Minute,
			ModelRoundTimeout: DefaultModelRoundTimeout,
			Bash: BashConfig{
				Sandbox:         true,
				BlockedCommands: []string{"rm -rf /", "mkfs"},
			},
			DeltaCheck: DeltaCheckConfig{
				Enabled:    true,
				Timeout:    90 * time.Second,
				WarnOnly:   false,
				MaxModules: 8,
			},
			SmartValidation: SmartValidationConfig{
				Enabled:             true,
				SelfReviewThreshold: 4,
			},
			ProactiveContext: ProactiveContextConfig{
				Enabled:         true,
				MaxFiles:        3,
				MaxLinesPerFile: 40,
			},
			KimiToolBudget:        40,
			RequireReadBeforeEdit: true,
		},
		UI: UIConfig{
			StreamOutput:      true,
			MarkdownRendering: true,
			ShowToolCalls:     true,
			ShowTokenUsage:    true,
			Theme:             "dark",
			ShowWelcome:       true,
			HintsEnabled:      true,
			Bell:              true,
		},
		Context: ContextConfig{
			MaxInputTokens:     0,     // Use model default
			WarningThreshold:   0.8,   // Warn at 80%
			SummarizationRatio: 0.5,   // Summarize to 50%
			ToolResultMaxChars: 10000, // 10k chars max for tool results
			EnableAutoSummary:  true,  // Enable auto-summarization
		},
		Permission: PermissionConfig{
			Enabled:       true, // Enable permission system by default
			DefaultPolicy: "ask",
			Rules: map[string]string{
				"read":                 "allow",
				"glob":                 "allow",
				"grep":                 "allow",
				"tree":                 "allow",
				"diff":                 "allow",
				"env":                  "allow",
				"list_dir":             "allow",
				"todo":                 "allow",
				"task_output":          "allow",
				"web_fetch":            "allow",
				"web_search":           "allow",
				"ask_user":             "allow",
				"memory":               "allow",
				"kill_shell":           "allow",
				"enter_plan_mode":      "allow",
				"update_plan_progress": "allow",
				"get_plan_status":      "allow",
				"exit_plan_mode":       "allow",
				"write":                "ask",
				"edit":                 "ask",
				"bash":                 "ask",
				"ssh":                  "ask", // SSH requires approval
			},
		},
		Plan: PlanConfig{
			Enabled:                      true,  // Enabled by default
			RequireApproval:              true,  // Require approval when enabled
			AutoDetect:                   true,  // Auto-trigger planning for complex tasks
			ClearContext:                 true,  // Clear context before plan execution
			DelegateSteps:                true,  // Run each step in isolated sub-agent
			WorkspaceIsolation:           true,  // Isolate safe read-only sub-agents in temp workspaces
			AbortOnStepFailure:           false, // Continue by default on step failure
			PlanningTimeout:              60 * time.Second,
			UseLLMExpansion:              true,
			Algorithm:                    "beam", // Tree search algorithm: beam, mcts, astar
			RequireExpectedArtifactPaths: false,
			VerifyPolicy: PlanVerifyPolicyConfig{
				Enabled:                   true,
				RequireVerificationIntent: true,
				AllowContains:             []string{},
				DenyContains: []string{
					"rm -", "rm -rf", " mv ", " cp ", " chmod ", " chown ", "sudo ",
					"mkfs", " dd ", "git reset", "git clean", "git checkout --",
					"git commit", "git push", "git stash", "npm install", "npm i ",
					"pnpm add", "yarn add", "pip install", "go get ", "cargo add ",
					"brew install", "apt install", "apk add", "dnf install", "pacman -s",
					"curl |", "wget |",
				},
				Profiles: map[string]PlanVerifyPolicyProfileConfig{
					"default": {AllowContains: []string{"check", "verify", "test", "lint", "build", "validate", "git diff --check", "git status", "git rev-parse"}},
					"go":      {AllowContains: []string{"go test", "go vet", "go build", "golangci-lint"}},
					"node":    {AllowContains: []string{"npm test", "pnpm test", "yarn test", "bun test", "eslint", "tsc", "vitest", "jest"}},
					"python":  {AllowContains: []string{"pytest", "ruff", "mypy", "python -m"}},
					"rust":    {AllowContains: []string{"cargo test", "cargo check", "cargo clippy", "cargo build"}},
					"java":    {AllowContains: []string{"mvn test", "mvn verify", "gradle test", "gradle check"}},
					"cmake":   {AllowContains: []string{"cmake --build", "ctest"}},
					"bazel":   {AllowContains: []string{"bazel test", "bazel build"}},
					"make":    {AllowContains: []string{"make test", "make check", "make verify"}},
					"php":     {AllowContains: []string{"composer validate", "phpunit", "phpstan"}},
				},
			},
		},
		DoneGate: DoneGateConfig{
			Enabled:         true,            // Enabled by default
			Mode:            "strict",        // Strict by default: stronger checks and fail-closed behavior
			FailClosed:      true,            // Never finalize if checks cannot be prepared
			CheckTimeout:    3 * time.Minute, // Per-check timeout
			AutoFixAttempts: 2,               // Retry with autonomous fixes up to 2 times
		},
		Hooks: HooksConfig{
			Enabled: false, // Disabled by default
			Hooks:   []HookConfig{},
		},
		Web: WebConfig{
			SearchProvider: "serpapi", // Default to SerpAPI
		},
		Session: SessionConfig{
			Enabled:      true,            // Enabled by default
			SaveInterval: 2 * time.Minute, // Save every 2 minutes
			AutoLoad:     true,            // Auto-load last session on startup
		},
		Memory: MemoryConfig{
			Enabled:    true, // Enabled by default
			MaxEntries: 1000, // Max 1000 entries
			AutoInject: true, // Auto-inject into system prompt
		},
		Logging: LoggingConfig{
			Level: "warn", // Default to warn level
		},
		Audit: AuditConfig{
			Enabled:       true,  // Enabled by default
			MaxEntries:    10000, // Max 10k entries per session
			MaxResultLen:  1000,  // Truncate results to 1k chars
			RetentionDays: 30,    // Keep logs for 30 days
		},
		RateLimit: RateLimitConfig{
			Enabled:           true,    // Enabled by default
			RequestsPerMinute: 60,      // 60 requests/min
			TokensPerMinute:   1000000, // 1M tokens/min
			BurstSize:         10,      // Allow burst of 10 requests
		},
		Cache: CacheConfig{
			Enabled:  true,            // Enabled by default
			Capacity: 100,             // 100 entries
			TTL:      5 * time.Minute, // 5 minute TTL
		},
		Watcher: WatcherConfig{
			Enabled:    false, // Disabled by default (can be enabled if needed)
			DebounceMs: 500,   // 500ms debounce
			MaxWatches: 1000,  // Max 1000 watched paths
		},
		DiffPreview: DiffPreviewConfig{
			Enabled: true, // Enabled by default - show diff preview before write/edit
		},
		MCP: MCPConfig{
			Enabled:             false, // Disabled by default
			Servers:             []MCPServerConfig{},
			HealthCheckInterval: 30 * time.Second, // Check every 30s when MCP is enabled
		},
		Update: UpdateConfig{
			Enabled:           true,             // Enabled by default
			AutoCheck:         true,             // Check on startup
			CheckInterval:     24 * time.Hour,   // Check once per day
			AutoDownload:      false,            // Require manual download
			IncludePrerelease: false,            // Only stable releases
			Channel:           "stable",         // Stable channel
			GitHubRepo:        "user/gokin",     // Should be updated to actual repo
			MaxBackups:        3,                // Keep 3 backups
			VerifyChecksum:    true,             // Always verify checksums
			NotifyOnly:        false,            // Allow prompting for install
			Timeout:           30 * time.Second, // HTTP request timeout
		},
		Completion: CompletionConfig{
			EvidenceFooter: true,
		},
	}
}
