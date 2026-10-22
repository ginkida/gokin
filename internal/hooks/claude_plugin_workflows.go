package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gokin/internal/plugindeps"
	"strings"
)

// ClaudePlugin is a selected, local plugin. Runtime authority is private: a
// workflow document cannot manufacture a forced or explicitly loaded plugin.
type ClaudePlugin struct {
	Name             string
	Skills, Commands json.RawMessage
	Agents           json.RawMessage
	MCPServers       json.RawMessage
	LSPServers       json.RawMessage
	Version          string
	Dependencies     []plugindeps.Dependency
	runtime          *claudePluginRuntime
}

func (p ClaudePlugin) Root() string {
	if p.runtime == nil {
		return ""
	}
	return p.runtime.Root
}
func (p ClaudePlugin) ID() string {
	if p.runtime == nil {
		return ""
	}
	return p.runtime.ID
}
func (p ClaudePlugin) Personal() bool { return p.runtime != nil && !p.runtime.Project }

// Explicit CLI selection and managed force-enablement supply component
// activation authority independently of a subsystem's discovery default.
func (p ClaudePlugin) ExplicitlyEnabled() bool {
	return p.runtime != nil && (p.runtime.Explicit || p.runtime.Forced)
}

// CheckSelected validates a component's original runtime against live plugin
// selection. Hook enable flags do not control non-hook components.
func (p ClaudePlugin) CheckSelected(manager *Manager) error {
	if p.runtime == nil || manager == nil {
		return fmt.Errorf("plugin component has no runtime authority")
	}
	selected, err := manager.ClaudePlugins()
	if err != nil {
		return err
	}
	for _, current := range selected {
		if current.Name == p.Name && current.ID() == p.ID() && pluginRuntimeFingerprint(current.runtime) == pluginRuntimeFingerprint(p.runtime) {
			return nil
		}
	}
	return fmt.Errorf("plugin %q is unavailable, disabled or its runtime changed", p.Name)
}

// RuntimeFingerprint binds a parsed component to its root, options and trust
// source without exposing option values or allowing files to invent authority.
func (p ClaudePlugin) RuntimeFingerprint() string { return pluginRuntimeFingerprint(p.runtime) }

func (p ClaudePlugin) ProcessEnvironment(projectDir string) (map[string]string, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("plugin runtime is unavailable")
	}
	env := map[string]string{"CLAUDE_PLUGIN_ROOT": p.runtime.Root, "CLAUDE_PLUGIN_DATA": p.runtime.Data, "CLAUDE_PROJECT_DIR": projectDir}
	for key, value := range p.runtime.Options {
		env["CLAUDE_PLUGIN_OPTION_"+strings.ToUpper(key)] = value
	}
	return env, nil
}

func (p ClaudePlugin) Expand(value string) (string, error) {
	return p.expand(value, false)
}

// ExpandContent keeps credentials out of model-visible skills and agents.
func (p ClaudePlugin) ExpandContent(value string) (string, error) {
	return p.expand(value, true)
}

func (p ClaudePlugin) expand(value string, content bool) (string, error) {
	if p.runtime == nil {
		return "", fmt.Errorf("plugin runtime is unavailable")
	}
	var out strings.Builder
	for {
		i := strings.Index(value, "${")
		if i < 0 {
			out.WriteString(value)
			break
		}
		out.WriteString(value[:i])
		value = value[i:]
		end := strings.IndexByte(value, '}')
		if end < 0 {
			out.WriteString(value)
			break
		}
		ref := value[:end+1]
		replacement := ref
		switch ref {
		case "${CLAUDE_PLUGIN_ROOT}":
			replacement = p.runtime.Root
		case "${CLAUDE_PLUGIN_DATA}":
			replacement = p.runtime.Data
		default:
			if strings.HasPrefix(ref, "${user_config.") {
				key := strings.TrimSuffix(strings.TrimPrefix(ref, "${user_config."), "}")
				if content && p.runtime.Sensitive[key] {
					replacement = "[sensitive option " + key + "]"
					break
				}
				var ok bool
				replacement, ok = p.runtime.Options[key]
				if !ok {
					return "", fmt.Errorf("plugin workflow references an unavailable user_config option")
				}
			}
		}
		out.WriteString(replacement)
		value = value[end+1:]
	}
	return out.String(), nil
}

func pluginRuntimeFingerprint(p *claudePluginRuntime) string {
	if p == nil {
		return ""
	}
	data, _ := json.Marshal(struct {
		ID, Root, Data   string
		Options          map[string]string
		Sensitive        map[string]bool
		Project          bool
		Forced, Explicit bool
	}{p.ID, p.Root, p.Data, p.Options, p.Sensitive, p.Project, p.Forced, p.Explicit})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Durable definitions bind executable inputs, while live authority checks use
// pluginRuntimeFingerprint so forced/explicit policy changes remain visible.
func durablePluginRuntimeFingerprint(p *claudePluginRuntime) string {
	if p == nil {
		return ""
	}
	data, _ := json.Marshal(struct {
		ID, Root, Data string
		Options        map[string]string
		Sensitive      map[string]bool
		Project        bool
	}{p.ID, p.Root, p.Data, p.Options, p.Sensitive, p.Project})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (p ClaudePlugin) BindHooks(definitions []ClaudeCommand) []ClaudeCommand {
	definitions = CloneClaudeCommands(definitions)
	for i := range definitions {
		definitions[i].plugin = p.runtime
	}
	return definitions
}

func (p ClaudePlugin) ValidateHooks(definitions []ClaudeCommand) error {
	for _, definition := range definitions {
		if definition.Args == nil && strings.Contains(definition.Command, "${user_config.") {
			return fmt.Errorf("shell-form plugin hooks cannot interpolate user_config; use exec args or CLAUDE_PLUGIN_OPTION variables")
		}
		for _, value := range append([]string{definition.Command}, definition.Args...) {
			if _, err := p.Expand(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) ClaudePlugins() ([]ClaudePlugin, error) {
	if err := m.RefreshClaudeSettings(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	plugins := make([]ClaudePlugin, len(m.claudePlugins))
	for i, p := range m.claudePlugins {
		p.Skills = append(json.RawMessage(nil), p.Skills...)
		p.Commands = append(json.RawMessage(nil), p.Commands...)
		p.Agents = append(json.RawMessage(nil), p.Agents...)
		p.MCPServers = append(json.RawMessage(nil), p.MCPServers...)
		p.LSPServers = append(json.RawMessage(nil), p.LSPServers...)
		plugins[i] = p
	}
	return plugins, nil
}

// CommandsPolicyError checks the live policy and selected runtime, including
// already-consumed once handlers. Required checks cannot disappear silently.
func (m *Manager) CommandsPolicyError(definitions []ClaudeCommand) error {
	if len(definitions) == 0 {
		return nil
	}
	if err := m.CheckReady(); err != nil {
		return err
	}
	for _, definition := range definitions {
		if definition.plugin == nil {
			if err := m.SkillHookPolicyError(); err != nil {
				return err
			}
		} else if m == nil || !m.HookEnabled(&Hook{Enabled: true, claudePlugin: definition.plugin}) {
			return fmt.Errorf("required plugin skill hooks are unavailable or blocked by current policy")
		}
	}
	return nil
}
