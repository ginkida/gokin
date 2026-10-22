package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

type MCPHookOutput struct {
	Text    string
	IsError bool
}
type MCPHookInvoker func(context.Context, string, string, map[string]any) (MCPHookOutput, error)

// SetMCPHookInvoker binds the session's MCP manager, shared with its children.
// A hook calls the protocol directly; it does not recursively fire tool hooks.
func (m *Manager) SetMCPHookInvoker(invoke MCPHookInvoker) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.mcpHookInvoker = invoke
	m.mu.Unlock()
}

func validMCPHookName(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateMCPHookInput(node *yaml.Node, depth int) error {
	if depth > 64 {
		return fmt.Errorf("MCP hook input exceeds 64 nesting levels")
	}
	switch node.Kind {
	case yaml.MappingNode:
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Tag != "!!str" || seen[key.Value] {
				return fmt.Errorf("MCP hook input requires unique string keys")
			}
			seen[key.Value] = true
			if err := validateMCPHookInput(node.Content[i+1], depth+1); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, value := range node.Content {
			if err := validateMCPHookInput(value, depth+1); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!int", "!!float", "!!bool", "!!null":
		default:
			return fmt.Errorf("MCP hook input must contain JSON values")
		}
	default:
		return fmt.Errorf("MCP hook input must contain JSON values")
	}
	return nil
}

func cloneMCPHookInput(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_ = decoder.Decode(&cloned)
	return cloned
}

var mcpHookReference = regexp.MustCompile(`\$\{([^{}]+)\}`)

func expandMCPHookInput(input map[string]any, event map[string]any, info RuntimeInfo, plugin *claudePluginRuntime) (map[string]any, error) {
	var expand func(any) (any, error)
	remaining := maxHookOutputBytes
	charge := func(value any) error {
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > remaining {
			return fmt.Errorf("expanded MCP hook input exceeds 64 KiB or is not JSON")
		}
		remaining -= len(encoded)
		return nil
	}
	resolve := func(reference string) (any, error) {
		if strings.HasPrefix(reference, "${user_config.") || reference == "${CLAUDE_PLUGIN_ROOT}" || reference == "${CLAUDE_PLUGIN_DATA}" || reference == "${CLAUDE_PROJECT_DIR}" {
			value := pluginHookVariables(reference, info, plugin)
			if value == reference {
				return nil, fmt.Errorf("MCP hook references unavailable plugin metadata")
			}
			return value, nil
		}
		path := strings.TrimSuffix(strings.TrimPrefix(reference, "${"), "}")
		var value any = event
		for _, segment := range strings.Split(path, ".") {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("MCP hook input references a missing event field")
			}
			value, ok = object[segment]
			if !ok {
				return nil, fmt.Errorf("MCP hook input references a missing event field")
			}
		}
		return value, nil
	}
	expand = func(value any) (any, error) {
		switch value := value.(type) {
		case string:
			matches := mcpHookReference.FindAllStringIndex(value, -1)
			if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(value) {
				resolved, err := resolve(value)
				if err != nil {
					return nil, err
				}
				if err := charge(resolved); err != nil {
					return nil, err
				}
				return resolved, nil
			}
			var result strings.Builder
			end := 0
			for _, match := range matches {
				if result.Len()+match[0]-end > remaining {
					return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB")
				}
				result.WriteString(value[end:match[0]])
				replacement, err := resolve(value[match[0]:match[1]])
				if err != nil {
					return nil, err
				}
				if text, ok := replacement.(string); ok {
					if result.Len()+len(text) > remaining {
						return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB")
					}
					result.WriteString(text)
				} else {
					data, err := json.Marshal(replacement)
					if err != nil {
						return nil, err
					}
					if result.Len()+len(data) > remaining {
						return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB")
					}
					result.Write(data)
				}
				end = match[1]
			}
			if result.Len()+len(value)-end > remaining {
				return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB")
			}
			result.WriteString(value[end:])
			text := result.String()
			if err := charge(text); err != nil {
				return nil, err
			}
			return text, nil
		case map[string]any:
			remaining -= 2 + max(0, len(value)-1)
			if remaining < 0 {
				return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB")
			}
			result := make(map[string]any, len(value))
			for key, item := range value {
				if err := charge(key); err != nil {
					return nil, err
				}
				remaining--
				next, err := expand(item)
				if err != nil {
					return nil, err
				}
				result[key] = next
			}
			return result, nil
		case []any:
			remaining -= 2 + max(0, len(value)-1)
			if remaining < 0 {
				return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB")
			}
			result := make([]any, len(value))
			for i, item := range value {
				next, err := expand(item)
				if err != nil {
					return nil, err
				}
				result[i] = next
			}
			return result, nil
		default:
			if err := charge(value); err != nil {
				return nil, err
			}
			return value, nil
		}
	}
	if input == nil {
		return map[string]any{}, nil
	}
	value, err := expand(input)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxHookOutputBytes {
		return nil, fmt.Errorf("expanded MCP hook input exceeds 64 KiB or is not JSON")
	}
	return value.(map[string]any), nil
}

func (m *Manager) executeMCPHook(ctx context.Context, hook *Hook, hctx *Context, timeout time.Duration) (result Result) {
	if hook.Type == SessionStart && RuntimeInfoFromContext(ctx).SessionStartAtLaunch {
		return Result{Hook: hook, skipped: true}
	}
	started := time.Now()
	result.Hook = hook
	defer func() { result.Elapsed = time.Since(started) }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		result.Error = err
		return
	}
	revision := m.ExecutionRevision()
	if err := m.CheckReady(); err != nil {
		result.Error = err
		return
	}
	if !m.HookEnabled(hook) {
		result.Error = fmt.Errorf("MCP hook is no longer enabled")
		return
	}
	if binding, ok := ctx.Value(promptClientKey{}).(promptClientBinding); ok && binding.guard != nil {
		if err := binding.guard(); err != nil {
			result.Error = err
			return
		}
	}
	data, info, err := claudeInput(ctx, hook, hctx)
	if err != nil {
		result.Error = err
		return
	}
	var event map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&event); err != nil {
		result.Error = err
		return
	}
	input, err := expandMCPHookInput(hook.Claude.MCPInput, event, info, hook.claudePlugin)
	if err != nil {
		result.Error = err
		return
	}
	m.mu.RLock()
	invoke := m.mcpHookInvoker
	m.mu.RUnlock()
	if invoke == nil {
		result.Error = fmt.Errorf("MCP tool hooks require an available MCP manager")
		return
	}
	if err := m.CheckExecutionRevision(revision); err != nil {
		result.Error = err
		return
	}
	output, err := invoke(ctx, hook.Claude.MCPServer, hook.Claude.MCPTool, input)
	if err != nil {
		result.Error = err
		return
	}
	if output.IsError {
		result.Error = fmt.Errorf("MCP hook tool returned an error")
		return
	}
	if len(output.Text) > maxHookOutputBytes {
		result.Error = fmt.Errorf("MCP hook output exceeds 64 KiB")
		return
	}
	if strings.HasPrefix(strings.TrimSpace(output.Text), "{") && !validHTTPHookOutput([]byte(strings.TrimSpace(output.Text))) {
		result.Error = fmt.Errorf("invalid MCP hook JSON output")
		return
	}
	return interpretClaudeResult(result, output.Text, "", 0)
}
