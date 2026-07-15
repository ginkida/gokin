package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gokin/internal/config"
	"gokin/internal/tools"
)

// AgentType defines the type of agent and its capabilities.
type AgentType string

const (
	// AgentTypeExplore is for exploring and searching codebases.
	// Tools: read, glob, grep, tree, list_dir
	AgentTypeExplore AgentType = "explore"

	// AgentTypeBash is for executing shell commands.
	// Tools: bash, read, glob
	AgentTypeBash AgentType = "bash"

	// AgentTypeGeneral is a general-purpose agent with access to all tools.
	AgentTypeGeneral AgentType = "general"

	// AgentTypePlan is for designing implementation strategies.
	// Tools: read-only exploration + planning tools
	AgentTypePlan AgentType = "plan"

	// AgentTypeGuide is for answering questions about Claude Code.
	// Tools: documentation/search focused
	AgentTypeGuide AgentType = "claude-code-guide"
)

// IsLightweight reports whether this agent type does mechanical search/command
// work (explore/bash/guide) rather than open-ended reasoning. Lightweight agents
// don't benefit from an extended-thinking budget — it just adds latency.
func (t AgentType) IsLightweight() bool {
	return t == AgentTypeExplore || t == AgentTypeBash || t == AgentTypeGuide
}

// SubAgentThinkingBudget returns the per-agent reasoning budget for the resolved
// thinking mode. The sub-agent loop has no TaskComplexity score, so agent TYPE
// is the complexity signal — mirroring the foreground router's adaptive choice:
// lightweight types skip thinking on auto; general/plan and custom types reason.
// off → never; on → reason even for lightweight types (a floor).
func SubAgentThinkingBudget(agentType AgentType, mode string) int32 {
	const (
		heavyBudget int32 = 8192
		onFloor     int32 = 4096
	)
	switch config.ResolveThinkingMode(mode) {
	case config.ThinkingModeOff:
		return 0
	case config.ThinkingModeOn:
		if agentType.IsLightweight() {
			return onFloor
		}
		return heavyBudget
	default: // auto
		if agentType.IsLightweight() {
			return 0
		}
		return heavyBudget
	}
}

// AllowedTools returns the list of tools allowed for this agent type.
func (t AgentType) AllowedTools() []string {
	switch t {
	case AgentTypeExplore:
		return []string{
			"read", "glob", "grep", "tree", "list_dir",
			"tools_list", "skill", "request_tool", "ask_agent",
		}
	case AgentTypeBash:
		return []string{"bash", "read", "glob", "tools_list", "skill", "request_tool", "ask_agent"}
	case AgentTypeGeneral:
		return nil // nil means all tools allowed
	case AgentTypePlan:
		// Read-only exploration + planning tools
		return []string{
			"read", "glob", "grep", "tree", "list_dir", "diff",
			"todo", "web_fetch", "web_search", "ask_user", "env",
			"tools_list", "skill", "request_tool", "ask_agent",
			// Planning tools
			"enter_plan_mode", "exit_plan_mode",
			"update_plan_progress", "get_plan_status",
		}
	case AgentTypeGuide:
		// Documentation/search focused
		return []string{"glob", "grep", "read", "web_fetch", "web_search", "tools_list", "skill", "request_tool", "ask_agent"}
	default:
		return []string{}
	}
}

// String returns the string representation of the agent type.
func (t AgentType) String() string {
	return string(t)
}

// ParseAgentType parses a string into an AgentType.
func ParseAgentType(s string) AgentType {
	switch s {
	case "explore":
		return AgentTypeExplore
	case "bash":
		return AgentTypeBash
	case "general":
		return AgentTypeGeneral
	case "plan":
		return AgentTypePlan
	case "claude-code-guide":
		return AgentTypeGuide
	default:
		return AgentTypeGeneral
	}
}

// AgentStatus represents the current status of an agent.
type AgentStatus string

const (
	AgentStatusPending   AgentStatus = "pending"
	AgentStatusRunning   AgentStatus = "running"
	AgentStatusCompleted AgentStatus = "completed"
	AgentStatusFailed    AgentStatus = "failed"
	AgentStatusCancelled AgentStatus = "cancelled"
)

// AgentResult contains the result of an agent's execution.
type AgentResult struct {
	AgentID   string         `json:"agent_id"`
	Type      AgentType      `json:"type"`
	Model     string         `json:"model,omitempty"` // Actual provider model used for this invocation.
	Provider  string         `json:"provider,omitempty"`
	Status    AgentStatus    `json:"status"`
	Output    string         `json:"output"`
	Error     string         `json:"error,omitempty"`
	Duration  time.Duration  `json:"duration"`
	Completed bool           `json:"completed"`
	Metadata  map[string]any `json:"metadata,omitempty"` // Additional context (e.g., learned_entry_id for feedback loop)

	// PolicyBlock carries the first authorization boundary refused during this
	// run. Status may still be completed when the model produced an explanation,
	// but synchronous callers must not mistake that prose for authorized work.
	PolicyBlock *tools.PolicyBlock `json:"policy_block,omitempty"`

	// OutputFile is the path to the file-backed output stream.
	// When set, full output is read from this file instead of the Output field.
	OutputFile string `json:"output_file,omitempty"`

	// Per-run token usage summed across the agent's model rounds. InputTokens
	// includes cached input (providers still bill and quota cache reads, usually
	// at a discounted rate); CacheReadInputTokens exposes that subset separately.
	// Surfaced so background callers such as /loop can enforce honest budgets.
	InputTokens          int     `json:"input_tokens,omitempty"`
	OutputTokens         int     `json:"output_tokens,omitempty"`
	CacheReadInputTokens int     `json:"cache_read_input_tokens,omitempty"`
	EstimatedCost        float64 `json:"estimated_cost,omitempty"`
	CostTracked          bool    `json:"cost_tracked,omitempty"`

	// InvocationScope is runtime-only attribution metadata. It is deliberately
	// excluded from persistence and model/tool output; the App accounting
	// callback uses exact equality to keep old async completions out of a newer
	// headless invocation while retaining them in cumulative session totals.
	InvocationScope InvocationScope `json:"-"`

	// MutatingToolCalls is how many code/repo-MUTATING tools (IsImplementationTool:
	// write/edit/delete/refactor/git_commit/…) the agent ran this turn. Surfaced so
	// the /loop scheduler can tell a "made changes" iteration from a no-op one
	// (churn detection) without re-reading the journal or snapshotting the tree.
	MutatingToolCalls int `json:"mutating_tool_calls,omitempty"`

	// TouchedPaths lists the files this agent SUCCESSFULLY mutated (workDir-
	// relative slash paths, deduped, sorted — the done-gate's touchedPaths
	// ledger via GetTouchedPaths). Surfaced so /loop can record WHICH files
	// each iteration changed, giving the next iteration concrete anchors
	// instead of re-grepping for its own prior work.
	TouchedPaths []string `json:"touched_paths,omitempty"`
}

// AgentOutputWriter streams agent output to both an in-memory buffer (capped)
// and a persistent file. This prevents OOM for long-running agents while
// keeping recent output quickly accessible.
type AgentOutputWriter struct {
	mu         sync.Mutex
	buf        []byte
	file       *os.File
	filePath   string
	totalBytes int64
	truncated  bool
}

const maxAgentMemoryOutput = 2 * 1024 * 1024 // 2 MB in-memory cap for agent output

// NewAgentOutputWriter creates a file-backed output writer for an agent.
// The file is created at .gokin/agent-output/{agentID}.log under workDir.
func NewAgentOutputWriter(workDir, agentID string) *AgentOutputWriter {
	w := &AgentOutputWriter{}
	dir := filepath.Join(workDir, ".gokin", "agent-output")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return w // proceed without file backing
	}
	path := filepath.Join(dir, agentID+".log")
	f, err := os.Create(path)
	if err != nil {
		return w
	}
	w.file = f
	w.filePath = path
	return w
}

// Write appends data to both in-memory buffer and file.
func (w *AgentOutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		w.file.Write(p)
	}
	w.totalBytes += int64(len(p))

	if !w.truncated && int64(len(w.buf))+int64(len(p)) <= maxAgentMemoryOutput {
		w.buf = append(w.buf, p...)
	} else if !w.truncated {
		w.truncated = true
	}
	return len(p), nil
}

// WriteString appends a string to the output.
func (w *AgentOutputWriter) WriteString(s string) {
	w.Write([]byte(s))
}

// String returns the in-memory portion of the output.
func (w *AgentOutputWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := string(w.buf)
	if w.truncated {
		s += fmt.Sprintf("\n\n[Output truncated: %d bytes total. Full output in: %s]",
			w.totalBytes, w.filePath)
	}
	return s
}

// FilePath returns the path to the output file.
func (w *AgentOutputWriter) FilePath() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.filePath
}

// TotalBytes returns total bytes written.
func (w *AgentOutputWriter) TotalBytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.totalBytes
}

// ReadFrom reads output from the file starting at the given offset.
// Returns the content and the new offset.
func (w *AgentOutputWriter) ReadFrom(offset int64) (string, int64, error) {
	w.mu.Lock()
	path := w.filePath
	w.mu.Unlock()

	if path == "" {
		// No file — fall back to in-memory
		s := w.String()
		if offset >= int64(len(s)) {
			return "", int64(len(s)), nil
		}
		return s[offset:], int64(len(s)), nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", offset, err
	}
	defer f.Close()

	// Use actual file size for accurate reads (total may be stale snapshot)
	fi, err := f.Stat()
	if err != nil {
		return "", offset, err
	}
	fileSize := fi.Size()

	if offset >= fileSize {
		return "", offset, nil // No new data
	}

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return "", offset, err
		}
	}

	buf := make([]byte, fileSize-offset)
	n, _ := f.Read(buf)
	return string(buf[:n]), offset + int64(n), nil
}

// Close closes the output file.
func (w *AgentOutputWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

// AgentTask represents a task to be executed by an agent.
type AgentTask struct {
	Prompt       string    `json:"prompt"`
	Type         AgentType `json:"type"`
	Background   bool      `json:"background"`
	Description  string    `json:"description,omitempty"`
	MaxTurns     int       `json:"max_turns,omitempty"`
	Model        string    `json:"model,omitempty"`
	Thoroughness string    `json:"thoroughness,omitempty"`
	OutputStyle  string    `json:"output_style,omitempty"`
}

// IsSuccess returns true if the agent completed successfully.
func (r *AgentResult) IsSuccess() bool {
	return r.Status == AgentStatusCompleted && r.Error == ""
}
