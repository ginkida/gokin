package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ClaudeCommand is the command-handler contract used by Claude skill hooks.
// Unsupported events and handler controls are rejected at load time.
type ClaudeCommand struct {
	Event           string
	Matcher         string
	Command         string
	Args            []string
	Timeout         time.Duration
	Once            bool
	Handler         string
	Prompt          string
	Model           string
	ContinueOnBlock bool
	URL             string
	Headers         map[string]string
	AllowedEnvVars  []string
	OnFailure       string
	MCPServer       string
	MCPTool         string
	MCPInput        map[string]any
	Async           bool
	AsyncRewake     bool
	TimeoutExplicit bool // SessionEnd distinguishes default and explicit budgets.
	plugin          *claudePluginRuntime
}

// CommandFingerprint binds the complete ordered command contract, including
// the distinction between shell invocation (nil args) and exec (empty args).
func CommandFingerprint(definitions []ClaudeCommand) string {
	// Keep the persisted hash independent of incidental internal struct fields.
	type contract struct {
		Event           string            `json:"event"`
		Matcher         string            `json:"matcher"`
		Command         string            `json:"command"`
		Args            []string          `json:"args"`
		Timeout         time.Duration     `json:"timeout_ns"`
		Once            bool              `json:"once"`
		Plugin          string            `json:"plugin,omitempty"`
		Handler         string            `json:"handler,omitempty"`
		Prompt          string            `json:"prompt,omitempty"`
		Model           string            `json:"model,omitempty"`
		ContinueOnBlock bool              `json:"continue_on_block,omitempty"`
		URL             string            `json:"url,omitempty"`
		Headers         map[string]string `json:"headers,omitempty"`
		AllowedEnvVars  []string          `json:"allowed_env_vars,omitempty"`
		OnFailure       string            `json:"on_failure,omitempty"`
		MCPServer       string            `json:"mcp_server,omitempty"`
		MCPTool         string            `json:"mcp_tool,omitempty"`
		MCPInput        map[string]any    `json:"mcp_input,omitempty"`
		Async           bool              `json:"async,omitempty"`
		AsyncRewake     bool              `json:"async_rewake,omitempty"`
		TimeoutExplicit bool              `json:"timeout_explicit,omitempty"`
	}
	canonical := make([]contract, len(definitions))
	for i, definition := range definitions {
		canonical[i] = contract{definition.Event, definition.Matcher, definition.Command, definition.Args, definition.Timeout, definition.Once, durablePluginRuntimeFingerprint(definition.plugin), definition.Handler, definition.Prompt, definition.Model, definition.ContinueOnBlock, definition.URL, definition.Headers, definition.AllowedEnvVars, definition.OnFailure, definition.MCPServer, definition.MCPTool, definition.MCPInput, definition.Async, definition.AsyncRewake, definition.TimeoutExplicit}
	}
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var claudeEventTypes = map[string]Type{
	"PreToolUse": PreTool, "PostToolUse": PostTool,
	"PostToolUseFailure": OnError, "Stop": Stop,
	"SubagentStop":     Stop,
	"UserPromptSubmit": UserPromptSubmit, "SubagentStart": SubagentStart,
	"SessionEnd": SessionEnd, "SessionStart": SessionStart,
	"FileChanged": FileChanged, "CwdChanged": CwdChanged,
	"DirectoryAdded": DirectoryAdded,
}

func ParseClaudeHooks(node yaml.Node) ([]ClaudeCommand, error) {
	return parseClaudeHooks(node, 32, false)
}

func parseClaudeHooks(node yaml.Node, limit int, settings bool) ([]ClaudeCommand, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return nil, fmt.Errorf("hooks must be an event map")
	}
	var result []ClaudeCommand
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		event, groups := node.Content[i].Value, node.Content[i+1]
		if seen[event] || claudeEventTypes[event] == "" || event == "SubagentStop" && !settings {
			return nil, fmt.Errorf("hooks event %q is duplicated or unsupported", event)
		}
		seen[event] = true
		if groups.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("hooks.%s must be a matcher list", event)
		}
		for _, group := range groups.Content {
			if err := validateClaudeGroupTypes(group, limit); err != nil {
				return nil, fmt.Errorf("hooks.%s: %w", event, err)
			}
			var rule struct {
				Matcher string `yaml:"matcher"`
				Hooks   []struct {
					Type            string            `yaml:"type"`
					Command         string            `yaml:"command"`
					Args            *[]string         `yaml:"args"`
					Timeout         *float64          `yaml:"timeout"`
					Once            bool              `yaml:"once"`
					Prompt          string            `yaml:"prompt"`
					Model           string            `yaml:"model"`
					ContinueOnBlock *bool             `yaml:"continueOnBlock"`
					URL             string            `yaml:"url"`
					Headers         map[string]string `yaml:"headers"`
					AllowedEnvVars  []string          `yaml:"allowedEnvVars"`
					OnFailure       string            `yaml:"onFailure"`
					Server          string            `yaml:"server"`
					Tool            string            `yaml:"tool"`
					Input           map[string]any    `yaml:"input"`
					Async           *bool             `yaml:"async"`
					AsyncRewake     *bool             `yaml:"asyncRewake"`
				} `yaml:"hooks"`
			}
			data, err := yaml.Marshal(group)
			if err != nil {
				return nil, err
			}
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			if err := decoder.Decode(&rule); err != nil {
				return nil, fmt.Errorf("hooks.%s: %w", event, claudeSchemaError(err))
			}
			if len(rule.Matcher) > 512 {
				return nil, fmt.Errorf("hooks matcher exceeds 512 bytes")
			}
			if event == "UserPromptSubmit" || event == "CwdChanged" {
				rule.Matcher = "" // This event always runs; Claude ignores its matcher.
			}
			if rule.Matcher != "" && rule.Matcher != "*" {
				if _, err := compileClaudeMatcher(rule.Matcher); err != nil {
					return nil, fmt.Errorf("hooks matcher: %w", err)
				}
			}
			if event == "Stop" && rule.Matcher != "" && rule.Matcher != "*" {
				return nil, fmt.Errorf("Stop hooks do not support a matcher")
			}
			if len(rule.Hooks) == 0 {
				return nil, fmt.Errorf("hooks.%s has no handlers", event)
			}
			for _, handler := range rule.Hooks {
				if event == "SessionStart" && handler.Type != "command" && handler.Type != "mcp_tool" {
					return nil, fmt.Errorf("SessionStart supports command and MCP tool handlers")
				}
				if (event == "SubagentStart" || event == "SessionEnd" || event == "FileChanged" || event == "CwdChanged" || event == "DirectoryAdded") && (handler.Type == "prompt" || handler.Type == "agent") {
					return nil, fmt.Errorf("%s supports command, HTTP and MCP tool handlers", event)
				}
				if handler.Type != "command" && handler.Type != "prompt" && handler.Type != "http" && handler.Type != "mcp_tool" && handler.Type != "agent" {
					return nil, fmt.Errorf("hooks handler type %q is unsupported", handler.Type)
				}
				if handler.Type == "command" && (strings.TrimSpace(handler.Command) == "" || len(handler.Command) > 8192 || strings.ContainsRune(handler.Command, 0)) {
					return nil, fmt.Errorf("hooks command must be non-empty and at most 8192 bytes")
				}
				h := ClaudeCommand{Event: event, Matcher: rule.Matcher, Command: handler.Command, Once: handler.Once, Timeout: 600 * time.Second}
				if event == "UserPromptSubmit" {
					h.Timeout = 30 * time.Second
				}
				if event == "SessionEnd" {
					h.Timeout = defaultSessionEndTimeout
				}
				if handler.Type != "command" && (handler.Async != nil || handler.AsyncRewake != nil) {
					return nil, fmt.Errorf("only command hooks can set async or asyncRewake")
				}
				if handler.Async != nil {
					h.Async = *handler.Async
				}
				if handler.AsyncRewake != nil {
					h.AsyncRewake = *handler.AsyncRewake
				}
				if handler.OnFailure != "" && handler.OnFailure != "continue" && handler.OnFailure != "block" {
					return nil, fmt.Errorf("hooks onFailure must be continue or block")
				}
				if handler.Type != "command" && handler.Type != "http" && handler.OnFailure != "" {
					return nil, fmt.Errorf("only command and HTTP hooks can set onFailure")
				}
				h.OnFailure = handler.OnFailure
				if handler.Type != "mcp_tool" && (handler.Server != "" || handler.Tool != "" || handler.Input != nil) {
					return nil, fmt.Errorf("only MCP tool hooks can set server, tool or input")
				}
				if handler.Type == "mcp_tool" {
					if handler.Command != "" || handler.Args != nil || handler.Prompt != "" || handler.Model != "" || handler.ContinueOnBlock != nil {
						return nil, fmt.Errorf("MCP tool hooks cannot set command, args, prompt, model or continueOnBlock")
					}
					if !validMCPHookName(handler.Server) || !validMCPHookName(handler.Tool) {
						return nil, fmt.Errorf("MCP tool hooks require valid server and tool names")
					}
					h.Handler, h.MCPServer, h.MCPTool = "mcp_tool", handler.Server, handler.Tool
					input, err := json.Marshal(handler.Input)
					if err != nil || len(input) > maxHookOutputBytes {
						return nil, fmt.Errorf("MCP hook input must be JSON of at most 64 KiB")
					}
					inputDecoder := json.NewDecoder(bytes.NewReader(input))
					inputDecoder.UseNumber()
					if err := inputDecoder.Decode(&h.MCPInput); err != nil {
						return nil, err
					}
				}
				if handler.Type != "http" && (handler.URL != "" || handler.Headers != nil || handler.AllowedEnvVars != nil) {
					return nil, fmt.Errorf("only HTTP hooks can set url, headers or allowedEnvVars")
				}
				if handler.Type == "http" {
					if handler.Command != "" || handler.Args != nil || handler.Prompt != "" || handler.Model != "" || handler.ContinueOnBlock != nil {
						return nil, fmt.Errorf("HTTP hooks cannot set command, args, prompt, model or continueOnBlock")
					}
					h.Handler, h.URL, h.Headers, h.AllowedEnvVars = "http", handler.URL, handler.Headers, handler.AllowedEnvVars
					if err := validateHTTPHook(h); err != nil {
						return nil, err
					}
				}
				if handler.Type == "prompt" || handler.Type == "agent" {
					if handler.Command != "" || handler.Args != nil || strings.TrimSpace(handler.Prompt) == "" || len(handler.Prompt) > 32768 || strings.ContainsRune(handler.Prompt, 0) {
						return nil, fmt.Errorf("prompt hooks require a non-empty prompt of at most 32768 bytes and no command or args")
					}
					if len(handler.Model) > 1024 || strings.ContainsAny(handler.Model, "\x00\n\r\t ") {
						return nil, fmt.Errorf("invalid prompt hook model")
					}
					h.Handler, h.Prompt, h.Model, h.Timeout = "prompt", handler.Prompt, handler.Model, 30*time.Second
					if handler.Type == "agent" {
						if handler.ContinueOnBlock != nil {
							return nil, fmt.Errorf("agent hooks cannot set continueOnBlock")
						}
						h.Handler, h.Timeout = "agent", 60*time.Second
					}
					if handler.ContinueOnBlock != nil {
						h.ContinueOnBlock = *handler.ContinueOnBlock
					}
				} else if handler.Prompt != "" || handler.Model != "" || handler.ContinueOnBlock != nil {
					return nil, fmt.Errorf("command hooks cannot set prompt, model or continueOnBlock")
				}
				if settings {
					// Claude only honors once in skill frontmatter.
					h.Once = false
				}
				if handler.Args != nil {
					h.Args = append([]string{}, (*handler.Args)...)
					if len(h.Args) > 64 {
						return nil, fmt.Errorf("hooks exec args exceed 64 values")
					}
					for _, arg := range h.Args {
						if len(arg) > 8192 || strings.ContainsRune(arg, 0) {
							return nil, fmt.Errorf("invalid hooks exec argument")
						}
					}
				}
				if handler.Timeout != nil {
					h.TimeoutExplicit = event == "SessionEnd"
					if math.IsNaN(*handler.Timeout) || math.IsInf(*handler.Timeout, 0) || *handler.Timeout <= 0 || *handler.Timeout > 600 {
						return nil, fmt.Errorf("hooks timeout must be between 0 and 600 seconds")
					}
					h.Timeout = time.Duration(*handler.Timeout * float64(time.Second))
				}
				result = append(result, h)
				if len(result) > limit {
					return nil, fmt.Errorf("hooks exceed %d handlers", limit)
				}
			}
		}
	}
	return result, nil
}

func claudeSchemaError(err error) error {
	var typed *yaml.TypeError
	if !errors.As(err, &typed) {
		return err
	}
	messages := append([]string(nil), typed.Errors...)
	for i, message := range messages {
		if prefix, _, found := strings.Cut(message, " not found in type "); found {
			messages[i] = prefix + " is unsupported"
		}
	}
	return fmt.Errorf("%s", strings.Join(messages, "; "))
}

func validateClaudeGroupTypes(group *yaml.Node, limit int) error {
	if group.Kind != yaml.MappingNode {
		return fmt.Errorf("matcher group must be an object")
	}
	for i := 0; i < len(group.Content); i += 2 {
		key, value := group.Content[i].Value, group.Content[i+1]
		if key == "matcher" && (value.Kind != yaml.ScalarNode || value.Tag != "!!str") {
			return fmt.Errorf("matcher must be a string")
		}
		if key != "hooks" {
			continue
		}
		if value.Kind != yaml.SequenceNode {
			return fmt.Errorf("handlers must be a list")
		}
		if len(value.Content) > limit {
			return fmt.Errorf("hooks exceed %d handlers", limit)
		}
		for _, handler := range value.Content {
			if handler.Kind != yaml.MappingNode {
				return fmt.Errorf("handler must be an object")
			}
			for j := 0; j < len(handler.Content); j += 2 {
				field, node := handler.Content[j].Value, handler.Content[j+1]
				switch field {
				case "type", "command", "prompt", "model", "url", "onFailure", "server", "tool":
					if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
						return fmt.Errorf("handler %s must be a string", field)
					}
				case "once", "continueOnBlock", "async", "asyncRewake":
					if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
						return fmt.Errorf("handler %s must be a boolean", field)
					}
				case "timeout":
					if node.Kind != yaml.ScalarNode || node.Tag != "!!int" && node.Tag != "!!float" {
						return fmt.Errorf("handler timeout must be a number")
					}
				case "headers":
					if node.Kind != yaml.MappingNode || len(node.Content) > 128 {
						return fmt.Errorf("headers must be an object of at most 64 strings")
					}
					seen := make(map[string]bool)
					for k := 0; k < len(node.Content); k += 2 {
						name, value := node.Content[k], node.Content[k+1]
						key := strings.ToLower(name.Value)
						if name.Tag != "!!str" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" || seen[key] {
							return fmt.Errorf("HTTP headers require unique string names and values")
						}
						seen[key] = true
					}
				case "input":
					if node.Kind != yaml.MappingNode {
						return fmt.Errorf("MCP hook input must be an object")
					}
					if err := validateMCPHookInput(node, 0); err != nil {
						return err
					}
				case "args", "allowedEnvVars":
					if node.Kind != yaml.SequenceNode || len(node.Content) > 64 {
						return fmt.Errorf("handler args must be a list of at most 64 strings")
					}
					for _, arg := range node.Content {
						if arg.Kind != yaml.ScalarNode || arg.Tag != "!!str" {
							return fmt.Errorf("handler args must contain strings")
						}
					}
				}
			}
		}
	}
	return nil
}

func CloneClaudeCommands(definitions []ClaudeCommand) []ClaudeCommand {
	result := append([]ClaudeCommand(nil), definitions...)
	for i := range result {
		if definitions[i].Args != nil {
			result[i].Args = append([]string{}, definitions[i].Args...)
		}
		result[i].Headers = maps.Clone(definitions[i].Headers)
		result[i].MCPInput = cloneMCPHookInput(definitions[i].MCPInput)
		if definitions[i].AllowedEnvVars != nil {
			result[i].AllowedEnvVars = append([]string{}, definitions[i].AllowedEnvVars...)
		}
	}
	return result
}

type RuntimeInfo struct {
	SessionID, TranscriptPath, ProjectDir, PermissionMode, Effort, AgentID, AgentType, ToolUseID, ToolNameOverride string
	StopHookActive                                                                                                 bool
	SessionTitle                                                                                                   string
	Model, EnvFile                                                                                                 string
	SessionStartAtLaunch                                                                                           bool
}

type runtimeInfoKey struct{}

func ContextWithRuntimeInfo(ctx context.Context, info RuntimeInfo) context.Context {
	return context.WithValue(ctx, runtimeInfoKey{}, info)
}

func RuntimeInfoFromContext(ctx context.Context) RuntimeInfo {
	info, _ := ctx.Value(runtimeInfoKey{}).(RuntimeInfo)
	return info
}

func ClaudeToolName(name string) string {
	if canonical := map[string]string{
		"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit",
		"grep": "Grep", "glob": "Glob", "notebook_edit": "NotebookEdit",
		"task": "Agent", "web_fetch": "WebFetch", "web_search": "WebSearch",
		"ask_user": "AskUserQuestion", "todo": "TodoWrite", "lsp": "LSP",
	}[name]; canonical != "" {
		return canonical
	}
	return name
}

func ClaudeAgentType(name string) string {
	if canonical := map[string]string{"general": "general-purpose", "explore": "Explore", "plan": "Plan"}[name]; canonical != "" {
		return canonical
	}
	return name
}

func claudeInput(ctx context.Context, hook *Hook, hctx *Context) ([]byte, RuntimeInfo, error) {
	info := RuntimeInfoFromContext(ctx)
	if info.ProjectDir == "" {
		info.ProjectDir = hctx.WorkDir
	}
	if info.PermissionMode == "" {
		info.PermissionMode = "default"
	}
	event := hook.Claude.Event
	if event == "Stop" && info.AgentID != "" {
		event = "SubagentStop"
	}
	input := map[string]any{"session_id": info.SessionID, "transcript_path": info.TranscriptPath,
		"cwd": hctx.WorkDir, "permission_mode": info.PermissionMode, "hook_event_name": event}
	if info.AgentID != "" {
		input["agent_id"], input["agent_type"] = info.AgentID, ClaudeAgentType(info.AgentType)
	}
	if info.Effort != "" {
		input["effort"] = map[string]string{"level": info.Effort}
	}
	if info.SessionTitle != "" {
		input["session_title"] = info.SessionTitle
	}
	if hook.Type == DirectoryAdded {
		input["directory"], input["source"] = hctx.Directory, hctx.Source
	} else if hook.Type == CwdChanged {
		input["old_cwd"], input["new_cwd"] = hctx.OldCwd, hctx.NewCwd
	} else if hook.Type == FileChanged {
		input["file_path"], input["event"] = hctx.FilePath, hctx.FileEvent
	} else if hook.Type == SessionStart {
		input["source"] = hctx.Source
		if info.Model != "" {
			input["model"] = info.Model
		}
	} else if hook.Type == SessionEnd {
		input["reason"] = hctx.Reason
	} else if hook.Type == UserPromptSubmit {
		input["prompt"] = hctx.Prompt
	} else if hook.Type == SubagentStart {
		// Common agent identity fields above are the complete event payload.
	} else if hook.Type == Stop {
		input["stop_hook_active"], input["last_assistant_message"] = info.StopHookActive, hctx.ToolResult
	} else {
		toolName := info.ToolNameOverride
		if toolName == "" {
			toolName = ClaudeToolName(hctx.ToolName)
		}
		input["tool_name"], input["tool_input"], input["tool_use_id"] = toolName, hctx.ToolArgs, info.ToolUseID
		if hook.Type == PostTool {
			input["tool_response"] = hctx.ToolResult
		}
		if hook.Type == OnError {
			input["error"], input["is_interrupt"] = hctx.ToolError, ctx.Err() != nil
		}
	}
	data, err := json.Marshal(input)
	return data, info, err
}

// Claude hooks own shell expansion. In particular, $variables, quotes and
// command substitutions in their script must not go through native templates.
func claudePathVariables(value string, info RuntimeInfo) string {
	return strings.ReplaceAll(value, "${CLAUDE_PROJECT_DIR}", info.ProjectDir)
}

func interpretClaudeResult(input Result, stdout, stderr string, exitCode int) (result Result) {
	result = input
	if result.Hook.Type == SessionEnd {
		result.Output = ""
		return result
	}
	defer func() {
		if exitCode == 2 && (result.Hook.Type == PreTool || result.Hook.Type == Stop || result.Hook.Type == UserPromptSubmit) {
			result.BlockReason = strings.TrimSpace(stderr)
			if result.BlockReason == "" {
				result.BlockReason = "Hook exited with blocking status 2"
			}
		}
		if result.BlockReason != "" {
			result.Output = result.BlockReason
		}
	}()
	result.Output = strings.TrimSpace(stderr)
	var output struct {
		Continue           *bool  `json:"continue"`
		StopReason         string `json:"stopReason"`
		Decision           string `json:"decision"`
		Reason             string `json:"reason"`
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput *struct {
			HookEventName            string         `json:"hookEventName"`
			PermissionDecision       string         `json:"permissionDecision"`
			PermissionDecisionReason string         `json:"permissionDecisionReason"`
			UpdatedInput             map[string]any `json:"updatedInput"`
			AdditionalContext        string         `json:"additionalContext"`
			SessionTitle             string         `json:"sessionTitle"`
			SuppressOriginalPrompt   bool           `json:"suppressOriginalPrompt"`
			InitialUserMessage       string         `json:"initialUserMessage"`
			ReloadSkills             bool           `json:"reloadSkills"`
			WatchPaths               []string       `json:"watchPaths"`
		} `json:"hookSpecificOutput"`
	}
	trimmed := strings.TrimSpace(stdout)
	if strings.HasPrefix(trimmed, "{") {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		if err := decoder.Decode(&output); err != nil {
			result.Error = fmt.Errorf("invalid Claude hook JSON: %w", err)
			return result
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			result.Error = fmt.Errorf("Claude hook stdout must contain one JSON object")
			return result
		}
		if specific := output.HookSpecificOutput; specific != nil {
			if specific.HookEventName != "" && specific.HookEventName != result.Hook.Claude.Event && !(result.Hook.Type == Stop && specific.HookEventName == "SubagentStop") {
				result.Error = fmt.Errorf("hook output event %q does not match %s", specific.HookEventName, result.Hook.Claude.Event)
				return result
			}
			result.AdditionalContext = specific.AdditionalContext
			if result.Hook.Type == UserPromptSubmit {
				result.SessionTitle = specific.SessionTitle
				result.SuppressOriginalPrompt = specific.SuppressOriginalPrompt
			}
			if result.Hook.Type == SessionStart {
				result.SessionTitle = specific.SessionTitle
				result.InitialUserMessage = specific.InitialUserMessage
				result.ReloadSkills = specific.ReloadSkills
			}
			if result.Hook.Type == SessionStart || result.Hook.Type == FileChanged || result.Hook.Type == CwdChanged {
				if err := ValidateWatchPaths(specific.WatchPaths); err != nil {
					result.Error = err
					return result
				}
				result.WatchPaths = specific.WatchPaths
			}
			if result.Hook.Type == PreTool {
				switch specific.PermissionDecision {
				case "", "allow", "ask", "deny":
					result.PermissionDecision = specific.PermissionDecision
				default:
					result.Error = fmt.Errorf("invalid hook permission decision %q", specific.PermissionDecision)
					return result
				}
				result.UpdatedInput = specific.UpdatedInput
				if specific.PermissionDecision == "deny" {
					result.BlockReason = specific.PermissionDecisionReason
					if result.BlockReason == "" {
						result.BlockReason = "Tool denied by hook"
					}
				}
			}
		}
		if output.Decision == "block" && (result.Hook.Type == Stop || result.Hook.Type == PreTool || result.Hook.Type == UserPromptSubmit) {
			result.BlockReason = output.Reason
			if result.BlockReason == "" {
				result.BlockReason = "Hook requested continuation or denied the action"
			}
		}
		if output.Decision == "block" && (result.Hook.Type == PostTool || result.Hook.Type == OnError) && output.Reason != "" {
			if result.AdditionalContext != "" {
				result.AdditionalContext += "\n"
			}
			result.AdditionalContext += output.Reason
		}
		if output.Continue != nil && !*output.Continue {
			result.StopReason = output.StopReason
			if result.StopReason == "" {
				result.StopReason = "Hook stopped execution"
			}
		}
		if (result.Hook.Type == UserPromptSubmit || result.Hook.Type == SessionStart || result.Hook.Type == FileChanged || result.Hook.Type == CwdChanged || result.Hook.Type == DirectoryAdded) && output.SystemMessage != "" {
			result.SystemMessage = output.SystemMessage
		}
	} else if exitCode == 0 {
		result.Output = trimmed
		if result.Hook.Type == UserPromptSubmit || result.Hook.Type == SessionStart {
			result.AdditionalContext = trimmed
		}
	}
	if result.Hook.Type == SubagentStart || result.Hook.Type == SessionStart || result.Hook.Type == FileChanged || result.Hook.Type == CwdChanged || result.Hook.Type == DirectoryAdded {
		result.BlockReason, result.StopReason, result.PermissionDecision = "", "", ""
		result.UpdatedInput = nil
		if exitCode != 0 || result.Error != nil {
			result.AdditionalContext = ""
			result.SessionTitle, result.InitialUserMessage, result.SystemMessage = "", "", ""
			result.ReloadSkills, result.WatchPaths = false, nil
		}
	}
	if result.Hook.Type == FileChanged || result.Hook.Type == CwdChanged || result.Hook.Type == DirectoryAdded {
		result.AdditionalContext, result.Output = "", ""
	}
	return result
}
