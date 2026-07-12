package hooks

import (
	"os"
	"strings"
)

// Type represents when a hook should be triggered.
type Type string

const (
	// PreTool runs before a tool executes.
	PreTool Type = "pre_tool"
	// PostTool runs after a tool executes successfully.
	PostTool Type = "post_tool"
	// OnError runs when a tool fails.
	OnError Type = "on_error"
	// OnStart runs when the application starts.
	OnStart Type = "on_start"
	// OnExit runs when the application exits.
	OnExit Type = "on_exit"
	// Stop runs at the end of an agent turn. A FailOnError stop hook that
	// exits non-zero asks the agent to CONTINUE the turn, with the hook's
	// output as the reason (bounded to one continuation per user turn).
	Stop Type = "stop"
)

// Condition represents when a hook should run relative to previous results.
type Condition string

const (
	// ConditionAlways means the hook always runs (default).
	ConditionAlways Condition = "always"
	// ConditionIfPreviousSuccess means the hook runs only if the previous tool call succeeded.
	ConditionIfPreviousSuccess Condition = "if_previous_success"
	// ConditionIfPreviousFailure means the hook runs only if the previous tool call failed.
	ConditionIfPreviousFailure Condition = "if_previous_failure"
)

// Hook represents a configured hook.
type Hook struct {
	Name        string    `yaml:"name"`          // Human-readable name
	Type        Type      `yaml:"type"`          // When to trigger
	ToolName    string    `yaml:"tool_name"`     // Which tool triggers this (empty = all)
	Command     string    `yaml:"command"`       // Shell command to execute
	Enabled     bool      `yaml:"enabled"`       // Whether hook is active
	Condition   Condition `yaml:"condition"`     // Condition for running (always, if_previous_success, if_previous_failure)
	FailOnError bool      `yaml:"fail_on_error"` // When true and hook fails, cancel tool execution
	DependsOn   string    `yaml:"depends_on"`    // Name of another hook that must complete first

	// Source records where the hook was configured ("config" = main
	// config.yaml hooks section, "project" = <workDir>/.gokin/hooks.yaml).
	// Surfaced by /hooks list. Not part of the YAML contract.
	Source string `yaml:"-"`
}

// DisplayName identifies the hook in logs and user-facing failure surfaces:
// the configured Name, or a short command preview when the hook is unnamed
// (name is optional in the YAML schema).
func (h *Hook) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	const max = 40
	cmd := h.Command
	if runes := []rune(cmd); len(runes) > max {
		cmd = string(runes[:max-1]) + "…"
	}
	return cmd
}

// ShouldRun checks whether the hook should run given the context and completed hooks.
// It verifies that:
//   - The hook is enabled
//   - The condition is met (based on previousSuccess in context)
//   - The DependsOn hook has completed (if specified)
func (h *Hook) ShouldRun(ctx *Context, completedHooks map[string]bool) bool {
	if !h.Enabled {
		return false
	}

	// Check condition
	condition := h.Condition
	if condition == "" {
		condition = ConditionAlways
	}
	switch condition {
	case ConditionIfPreviousSuccess:
		if !ctx.previousSuccess {
			return false
		}
	case ConditionIfPreviousFailure:
		if ctx.previousSuccess {
			return false
		}
	case ConditionAlways:
		// Always run
	}

	// Check dependency
	if h.DependsOn != "" {
		if completedHooks == nil {
			return false
		}
		if !completedHooks[h.DependsOn] {
			return false
		}
	}

	return true
}

// Context provides data to hooks for variable substitution.
type Context struct {
	ToolName        string            // Name of the tool being executed
	ToolArgs        map[string]any    // Arguments passed to the tool
	ToolResult      string            // Result from tool (post_tool only)
	ToolError       string            // Error message (on_error only)
	WorkDir         string            // Working directory
	Extra           map[string]string // Additional variables
	previousSuccess bool              // Whether the previous tool call succeeded
	CapturedOutput  string            // Stdout+stderr captured from last hook execution
}

// NewContext creates a new hook context.
func NewContext(toolName string, args map[string]any, workDir string) *Context {
	return &Context{
		ToolName: toolName,
		ToolArgs: args,
		WorkDir:  workDir,
		Extra:    make(map[string]string),
	}
}

// SetResult sets the tool result for post-tool hooks.
func (c *Context) SetResult(result string) {
	c.ToolResult = result
}

// SetError sets the error for on-error hooks.
func (c *Context) SetError(err string) {
	c.ToolError = err
}

// SetPreviousSuccess sets whether the previous tool call succeeded.
func (c *Context) SetPreviousSuccess(success bool) {
	c.previousSuccess = success
}

// GetCapturedOutput returns the captured stdout+stderr from the last hook execution.
func (c *Context) GetCapturedOutput() string {
	return c.CapturedOutput
}

// ExpandCommand expands variables in the hook command.
// Supported variables:
//   - ${TOOL_NAME} - name of the tool
//   - ${FILE_PATH} - file_path argument if present
//   - ${COMMAND} - command argument if present (bash)
//   - ${PATTERN} - pattern argument if present (glob/grep)
//   - ${WORK_DIR} - working directory
//   - ${RESULT} - tool result (post_tool only)
//   - ${ERROR} - error message (on_error only)
//   - Any environment variable
func (c *Context) ExpandCommand(command string) string {
	// A SINGLE os.Expand pass over the PRISTINE command template — never
	// re-scan already-substituted output. The old code ran a sequence of
	// strings.ReplaceAll calls, each rescanning the CUMULATIVE result of all
	// prior substitutions: if a resolved value (tool name, file path, tool
	// output, an env var...) happened to literally contain text like
	// "${WORK_DIR}" or "$PATH", a LATER pass would match and substitute
	// INSIDE the shellEscape()'d quotes an earlier pass had already emitted,
	// splicing unescaped/differently-escaped content into the command —
	// shell command injection via attacker/tool-controlled values. os.Expand
	// only scans its INPUT string for "$name"/"${name}" tokens; the mapping
	// function's return value is inserted verbatim into the output and is
	// never rescanned, which is exactly the single-pass guarantee this
	// needs. Resolution order (first match wins): built-ins, tool args,
	// Extra map, then the environment variable fallback.
	return os.Expand(command, func(key string) string {
		switch key {
		case "TOOL_NAME":
			return shellEscape(c.ToolName)
		case "WORK_DIR":
			return shellEscape(c.WorkDir)
		case "RESULT":
			return shellEscape(c.ToolResult)
		case "ERROR":
			return shellEscape(c.ToolError)
		case "FILE_PATH":
			if v, ok := c.ToolArgs["file_path"].(string); ok {
				return shellEscape(v)
			}
			return ""
		case "COMMAND":
			if v, ok := c.ToolArgs["command"].(string); ok {
				return shellEscape(v)
			}
			return ""
		case "PATTERN":
			if v, ok := c.ToolArgs["pattern"].(string); ok {
				return shellEscape(v)
			}
			return ""
		case "CONTENT":
			if v, ok := c.ToolArgs["content"].(string); ok {
				// Truncate content if too long
				if runes := []rune(v); len(runes) > 100 {
					v = string(runes[:100]) + "..."
				}
				return shellEscape(v)
			}
			return ""
		}
		if v, ok := c.Extra[key]; ok {
			return shellEscape(v)
		}
		// Environment variable fallback. Values are shell-escaped to prevent
		// injection: a malicious env var like "; rm -rf /" must not be executed.
		if v, ok := os.LookupEnv(key); ok {
			return shellEscape(v)
		}
		return ""
	})
}

// shellEscape wraps a string in single quotes for safe shell interpolation.
// Internal single quotes are escaped via the standard POSIX pattern.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Matches checks if the hook should trigger for the given context.
// It checks that the hook is enabled, type matches, tool name matches,
// and the condition is valid (non-empty conditions must be recognized).
func (h *Hook) Matches(hookType Type, toolName string) bool {
	if !h.Enabled {
		return false
	}
	if h.Type != hookType {
		return false
	}

	// Validate condition if set — unrecognized conditions don't match
	if h.Condition != "" && h.Condition != ConditionAlways &&
		h.Condition != ConditionIfPreviousSuccess && h.Condition != ConditionIfPreviousFailure {
		return false
	}

	// Empty tool_name means match all tools
	if h.ToolName == "" {
		return true
	}
	return h.ToolName == toolName
}

// knownTypes gates LoadFile validation — a typo'd hook type would otherwise
// silently never fire.
var knownTypes = map[Type]bool{
	PreTool:  true,
	PostTool: true,
	OnError:  true,
	OnStart:  true,
	OnExit:   true,
	Stop:     true,
}
