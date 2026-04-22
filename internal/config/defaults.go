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
	DefaultToolTimeout         = 30 * time.Second
	DefaultBashTimeout         = 30 * time.Second
	DefaultModelRoundTimeout   = 5 * time.Minute
	DefaultGracefulShutdown    = 10 * time.Second
	DefaultForcedShutdown      = 15 * time.Second
	DefaultPermissionTimeout   = 2 * time.Minute
	DefaultQuestionTimeout     = 5 * time.Minute
	DefaultPlanApprovalTimeout = 10 * time.Minute
	DefaultDiffDecisionTimeout = 5 * time.Minute

	// Coordinator settings
	DefaultMaxConcurrentAgents = 5
	// DefaultAgentTimeout is the cap for a single sub-agent run when no
	// per-type thoroughness override applies. The old 30-minute default
	// let stuck/looping agents burn 15+ minutes of the user's time before
	// the runner intervened; 10 minutes matches the "general/thorough"
	// ceiling which is already the longest intentional budget, and any
	// agent that legitimately needs more should set its own via type
	// metadata (AgentTypeGeneral etc.).
	DefaultAgentTimeout = 10 * time.Minute
	DefaultDecomposeThreshold  = 5
	DefaultParallelThreshold   = 8

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
