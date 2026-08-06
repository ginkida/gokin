package config

import "time"

// Default configuration values.
// These constants centralize all hardcoded values to enable easy configuration.
const (
	// Token and content limits
	DefaultMaxTokens          = 8192
	DefaultMaxChars           = 10000
	DefaultToolResultMaxChars = 30000
	DefaultMaxFetchContent    = 50000
	DefaultDiffTruncation     = 50000

	// Cache settings
	DefaultCacheSize    = 1000
	DefaultCacheTTL     = 5 * time.Minute
	DefaultLRUCacheSize = 1000

	// File system limits
	DefaultMaxWatches     = 1000
	DefaultMaxGlobResults = 1000
	DefaultChunkSize      = 1000

	// Audit settings
	DefaultAuditMaxEntries = 10000

	// Retry settings
	DefaultMaxRetries  = 10
	DefaultRetryDelay  = 1 * time.Second
	DefaultHTTPTimeout = 120 * time.Second

	// Timeout settings
	DefaultToolTimeout = 30 * time.Second
	DefaultBashTimeout = 30 * time.Second
	// DefaultModelRoundTimeout is the single source of truth for the HARD cap
	// on one model round. internal/client re-exports it so the executor and
	// sub-agent loop can share the value without duplicating the duration.
	DefaultModelRoundTimeout = 14 * time.Minute
	// DefaultModelWatchdogFloor and Headroom keep presentation/orchestration
	// inactivity watchdogs outside the hard provider-round deadline. They are
	// shared by App, plan execution, TUI, and doctor so those layers cannot
	// silently drift back below the configured round cap.
	DefaultModelWatchdogFloor    = 15 * time.Minute
	DefaultModelWatchdogHeadroom = time.Minute
	DefaultGracefulShutdown      = 10 * time.Second
	DefaultForcedShutdown        = 15 * time.Second
	DefaultPermissionTimeout     = 2 * time.Minute
	// DefaultQuestionTimeout is the fallback wait for an ask_user question
	// response when config.Permission.QuestionTimeoutSeconds is unset (0).
	// Resolved per-question by questionPromptTimeout (configurable;
	// <0 = no timeout, wait indefinitely with reminders). Longer than the
	// permission timeout because a clarifying question often needs thought.
	DefaultQuestionTimeout     = 10 * time.Minute
	DefaultPlanApprovalTimeout = 10 * time.Minute
	DefaultDiffDecisionTimeout = 5 * time.Minute

	// Coordinator settings
	DefaultMaxConcurrentAgents = 5
	// DefaultAgentTimeout is the cap for a normal sub-agent run when no
	// per-type thoroughness override applies. Keep it comfortably above the
	// single-round cap: otherwise the outer agent/task deadline makes
	// DefaultModelRoundTimeout unreachable before a provider can finish one
	// heavy response. Quick/thorough modes retain their explicit budgets.
	DefaultAgentTimeoutHeadroom = 6 * time.Minute
	DefaultAgentTimeout         = DefaultModelRoundTimeout + DefaultAgentTimeoutHeadroom
	// DefaultThoroughAgentTimeout is an explicit deep-work budget shared by
	// every built-in/dynamic agent type. It must not be shorter than normal.
	DefaultThoroughAgentTimeout = 35 * time.Minute
	DefaultDecomposeThreshold   = 5
	DefaultParallelThreshold    = 8

	// Context management
	DefaultContextWarningThreshold   = 0.8
	DefaultContextSummarizationRatio = 0.5

	// Session settings
	DefaultMaxSessionHistory = 200

	// Memory settings
	DefaultMaxMemoryEntries = 100

	// Rate limiting
	DefaultRequestsPerMinute = 60
	DefaultTokensPerMinute   = 100000

	// Ollama
	DefaultOllamaBaseURL = "http://localhost:11434"

	// UI update intervals
	DefaultGraphUpdateInterval    = 500 * time.Millisecond
	DefaultParallelUpdateInterval = 300 * time.Millisecond
	DefaultQueueUpdateInterval    = 500 * time.Millisecond

	// Progress intervals
	DefaultProgressInterval = 5 * time.Second
	DefaultCleanupInterval  = 5 * time.Minute
	DefaultTaskCleanupAge   = 30 * time.Minute
)

// ModelWatchdogTimeout returns the inactivity budget for layers that supervise
// a provider round without owning its hard deadline. The extra headroom makes
// the provider/model-round error win the race, while the floor preserves a
// useful recovery window when users intentionally configure a short round.
func ModelWatchdogTimeout(modelRound time.Duration) time.Duration {
	if modelRound <= 0 {
		modelRound = DefaultModelRoundTimeout
	}
	watchdog := modelRound + DefaultModelWatchdogHeadroom
	if watchdog < DefaultModelWatchdogFloor {
		return DefaultModelWatchdogFloor
	}
	return watchdog
}
