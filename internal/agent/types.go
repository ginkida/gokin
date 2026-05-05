package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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

// AllowedTools returns the list of tools allowed for this agent type.
func (t AgentType) AllowedTools() []string {
	switch t {
	case AgentTypeExplore:
		return []string{
			"read", "glob", "grep", "tree", "list_dir",
			"tools_list", "request_tool", "ask_agent",
		}
	case AgentTypeBash:
		return []string{"bash", "read", "glob", "tools_list", "request_tool", "ask_agent"}
	case AgentTypeGeneral:
		return nil // nil means all tools allowed
	case AgentTypePlan:
		// Read-only exploration + planning tools
		return []string{
			"read", "glob", "grep", "tree", "list_dir", "diff",
			"todo", "web_fetch", "web_search", "ask_user", "env",
			"tools_list", "request_tool", "ask_agent",
			// Planning tools
			"enter_plan_mode", "exit_plan_mode",
			"update_plan_progress", "get_plan_status",
		}
	case AgentTypeGuide:
		// Documentation/search focused
		return []string{"glob", "grep", "read", "web_fetch", "web_search", "tools_list", "request_tool", "ask_agent"}
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
	AgentID   string                 `json:"agent_id"`
	Type      AgentType              `json:"type"`
	Status    AgentStatus            `json:"status"`
	Output    string                 `json:"output"`
	Error     string                 `json:"error,omitempty"`
	Duration  time.Duration          `json:"duration"`
	Completed bool                   `json:"completed"`
	Metadata  map[string]any `json:"metadata,omitempty"` // Additional context (e.g., learned_entry_id for feedback loop)

	// OutputFile is the path to the file-backed output stream.
	// When set, full output is read from this file instead of the Output field.
	OutputFile string `json:"output_file,omitempty"`
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
