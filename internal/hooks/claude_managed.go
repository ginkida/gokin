package hooks

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxManagedSettingsFiles = 256
const maxManagedSettingsBytes = 16 << 20

// ClaudeManagedSettingsDir is an OS-owned location, never a project setting or
// environment override. Embedding hosts can configure their own trusted source.
func ClaudeManagedSettingsDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

func (m *Manager) ConfigureClaudeManagedSettings(directory string) {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if m.claudeManagedDir == directory {
		return
	}
	m.claudeManagedDir = directory
	m.claudeConfigured = true
	m.claudeFingerprint = ""
}

func managedSettingsSources(directory string) ([]claudeSettingsSource, bool, error) {
	if directory == "" {
		return nil, false, nil
	}
	base := filepath.Join(directory, "managed-settings.json")
	sources := []claudeSettingsSource{{path: base, provenance: "claude:managed", managed: true}}
	_, err := os.Stat(base)
	present := err == nil || !os.IsNotExist(err)
	dropDirectory := filepath.Join(directory, "managed-settings.d")
	info, err := os.Stat(dropDirectory)
	if os.IsNotExist(err) {
		return sources, present, nil
	}
	if err != nil || !info.IsDir() {
		return nil, true, fmt.Errorf("managed settings %s must be a readable directory", dropDirectory)
	}
	directoryFile, err := os.Open(dropDirectory)
	if err != nil {
		return nil, true, fmt.Errorf("read managed settings directory %s: %w", dropDirectory, err)
	}
	defer directoryFile.Close()
	entries, err := directoryFile.ReadDir(4097)
	if err != nil && err != io.EOF {
		return nil, true, fmt.Errorf("read managed settings directory %s: %w", dropDirectory, err)
	}
	if len(entries) > 4096 {
		return nil, true, fmt.Errorf("managed settings directory exceeds 4096 entries")
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > maxManagedSettingsFiles || present && len(names)+1 > maxManagedSettingsFiles {
		return nil, true, fmt.Errorf("managed settings exceed %d files", maxManagedSettingsFiles)
	}
	for _, name := range names {
		sources = append(sources, claudeSettingsSource{path: filepath.Join(dropDirectory, name), provenance: "claude:managed", managed: true})
	}
	return sources, present || len(names) > 0, nil
}

// Managed drop-ins share one tier: arrays add handlers, while null removes an
// earlier hooks block or event. Lower settings tiers cannot remove these checks.
func mergeManagedHookDefinitions(sources []claudeSettingsSource) error {
	// Resolve removals before compiling commands: removed handlers have no
	// executable authority. JSON validity is checked independently for every file.
	effective := make([]*yaml.Node, len(sources))
	for i := range sources {
		if !sources[i].managed || sources[i].fields.hooks == nil {
			continue
		}
		node := sources[i].fields.hooks
		if node.Tag == "!!null" {
			for j := 0; j < i; j++ {
				effective[j] = nil
			}
			continue
		}
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("managed settings %s: hooks must be an event map", sources[i].path)
		}
		effective[i] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for key := 0; key < len(node.Content); key += 2 {
			event, groups := node.Content[key], node.Content[key+1]
			if groups.Tag == "!!null" {
				for j := 0; j < i; j++ {
					if effective[j] == nil {
						continue
					}
					var kept []*yaml.Node
					for k := 0; k < len(effective[j].Content); k += 2 {
						if effective[j].Content[k].Value != event.Value {
							kept = append(kept, effective[j].Content[k:k+2]...)
						}
					}
					effective[j].Content = kept
				}
			} else {
				effective[i].Content = append(effective[i].Content, event, groups)
			}
		}
	}
	for i, node := range effective {
		if node == nil || len(node.Content) == 0 {
			continue
		}
		definitions, err := parseClaudeHooks(*node, 256, true)
		if err != nil {
			return fmt.Errorf("managed settings %s: %w", sources[i].path, err)
		}
		sources[i].definitions = definitions
	}
	return nil
}

func (m *Manager) unmanagedHooksEnabledLocked() bool {
	return m.enabled && !m.claudeDisabled && !m.claudeManagedOnly && !m.claudeManagedDisabled
}

func (m *Manager) SkillHooksAllowed() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.unmanagedHooksEnabledLocked()
}

func (m *Manager) SkillHookPolicyError() error {
	if err := m.CheckReady(); err != nil {
		return err
	}
	if m.SkillHooksAllowed() {
		return nil
	}
	if m != nil {
		m.mu.RLock()
		only := m.claudeManagedOnly
		m.mu.RUnlock()
		if only {
			return fmt.Errorf("skill hooks are blocked by managed allowManagedHooksOnly policy")
		}
	}
	return fmt.Errorf("skill hooks require hooks.enabled: true and an enabled unmanaged hook policy")
}

// Goals use a session-scoped prompt Stop evaluator and have the same ordinary
// hook/trust boundary. Required organizational hooks never authorize a goal.
func (m *Manager) GoalPolicyError() error {
	if err := m.SkillHookPolicyError(); err != nil {
		return fmt.Errorf("goal evaluator unavailable: %w", err)
	}
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if m.claudeConfigured && !m.claudeTrusted {
		return fmt.Errorf("goal evaluator requires a trusted workspace")
	}
	return nil
}

func (m *Manager) ManagedHooksOnly() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.claudeManagedOnly
}

func (m *Manager) HookEnabled(hook *Hook) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !hook.Enabled {
		return false
	}
	if hook.claudePlugin != nil {
		for _, selected := range m.claudePlugins {
			p := selected.runtime
			if p.ID == hook.claudePlugin.ID && pluginRuntimeFingerprint(p) == pluginRuntimeFingerprint(hook.claudePlugin) {
				return !m.claudeManagedDisabled && (p.Forced || !m.claudeDisabled && !m.claudeManagedOnly && (p.Explicit || m.enabled))
			}
		}
		return false
	}
	if hook.claudeManaged {
		return !m.claudeManagedDisabled
	}
	return m.unmanagedHooksEnabledLocked()
}

func (m *Manager) ManagedHooksActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.claudeManagedActive
}
