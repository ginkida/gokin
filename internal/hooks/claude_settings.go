package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const maxClaudeSettingsBytes = 1 << 20

type claudeSettingsSource struct {
	path, provenance string
	definitions      []ClaudeCommand
	managed          bool
	plugin           *claudePluginRuntime
	fields           claudeSettingsFields
}

type claudeSettingsFields struct {
	hooks                       *yaml.Node
	disabled, managedOnly       *bool
	disabledSet, managedOnlySet bool
	plugins, pluginConfigs      *yaml.Node
	sideloadDisabled            *bool
	sideloadSet                 bool
	managedPluginPolicy         []byte
	httpPolicy                  httpHookPolicy
}

// ClaudeUserSettingsDir honors Claude's alternate configuration directory.
func ClaudeUserSettingsDir() (string, error) {
	if directory := os.Getenv("CLAUDE_CONFIG_DIR"); directory != "" {
		return filepath.Abs(directory)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// ConfigureClaudeSettings retains a stable manager shared by delegated agents.
// Project files never grant themselves trust. Bare builders do not call this.
func (m *Manager) ConfigureClaudeSettings(projectDir, userDir string, trusted bool) {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if m.claudeConfigured && m.claudeProjectDir == projectDir && m.claudeUserDir == userDir && m.claudeTrusted == trusted {
		return
	}
	if m.claudeProjectDir != projectDir || m.claudeUserDir != userDir {
		m.claudeRepositoryDirs = nil
		m.claudeRegistryPinned = false
		m.claudePinnedRegistry = nil
		m.claudePinnedKnownIDs = nil
		m.claudePinnedPluginFlags = nil
		m.claudePinnedPluginConfigs = nil
	}
	m.claudeProjectDir, m.claudeUserDir, m.claudeTrusted = projectDir, userDir, trusted
	m.claudeConfigured = true
	m.claudeFingerprint = ""
}

// ClaudeWorkspaceTrusted returns the trust decision captured by this session.
func (m *Manager) ClaudeWorkspaceTrusted() bool {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	return m.claudeTrusted
}

// ClaudeSettingsDirectories returns the locations captured by this session.
// Interactive management must share them with runtime discovery rather than
// resolving a changed environment or an embedding host's defaults again.
func (m *Manager) ClaudeSettingsDirectories() (userDir, managedDir string) {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	return m.claudeUserDir, m.claudeManagedDir
}

func readClaudeSettings(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Claude settings %s must be a readable regular file", path)
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Claude settings %s: %w", path, err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Claude settings %s must be a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxClaudeSettingsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Claude settings %s: %w", path, err)
	}
	if len(data) > maxClaudeSettingsBytes {
		return nil, fmt.Errorf("Claude settings %s exceed 1 MiB", path)
	}
	return data, nil
}

// Validate all JSON objects before selecting fields. In particular a duplicate
// disableAllHooks/hooks key cannot be interpreted differently by two readers.
func validateSettingsNode(node *yaml.Node, depth int) error {
	if depth > 128 {
		return fmt.Errorf("settings JSON nesting exceeds 128 levels")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if seen[key] {
				return fmt.Errorf("duplicate settings JSON key %q", key)
			}
			seen[key] = true
		}
	}
	for _, child := range node.Content {
		if err := validateSettingsNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func settingsFields(data []byte, managed bool) (claudeSettingsFields, error) {
	var fields claudeSettingsFields
	if managed && len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	if !json.Valid(data) {
		return fields, fmt.Errorf("settings must contain one valid JSON object")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fields, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fields, fmt.Errorf("settings must be a JSON object")
	}
	if err := validateSettingsNode(&document, 0); err != nil {
		return fields, err
	}
	root := document.Content[0]
	if managed {
		fields.managedPluginPolicy = append([]byte(nil), data...)
	}
	for i := 0; i < len(root.Content); i += 2 {
		value := root.Content[i+1]
		switch root.Content[i].Value {
		case "allowedHttpHookUrls", "httpHookAllowedEnvVars":
			name := root.Content[i].Value
			values, err := parseHTTPHookPolicyList(value, name)
			if err != nil {
				return fields, err
			}
			if name == "allowedHttpHookUrls" {
				fields.httpPolicy.urls = values
			} else {
				fields.httpPolicy.env = values
			}
		case "hooks":
			fields.hooks = value
		case "enabledPlugins":
			fields.plugins = value
		case "pluginConfigs":
			fields.pluginConfigs = value
		case "disableSideloadFlags":
			if !managed {
				continue
			}
			fields.sideloadSet = true
			if value.Tag == "!!null" {
				continue
			}
			if value.Tag != "!!bool" {
				return fields, fmt.Errorf("disableSideloadFlags must be a boolean")
			}
			flag := value.Value == "true"
			fields.sideloadDisabled = &flag
		case "disableAllHooks", "allowManagedHooksOnly":
			only := root.Content[i].Value == "allowManagedHooksOnly"
			// Managed-only keys have no authority in user/project/local settings.
			if only && !managed {
				continue
			}
			if value.Tag == "!!null" {
				if managed {
					if only {
						fields.managedOnlySet = true
					} else {
						fields.disabledSet = true
					}
				}
				continue
			}
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return fields, fmt.Errorf("%s must be a boolean", root.Content[i].Value)
			}
			flag := value.Value == "true"
			if only {
				fields.managedOnly, fields.managedOnlySet = &flag, true
			} else {
				fields.disabled, fields.disabledSet = &flag, true
			}
		}
	}
	return fields, nil
}

// RefreshClaudeSettings samples all authorized files before publishing a scope.
// Content hashes avoid rebuilding unchanged handlers, while reading at request
// and execution boundaries picks up edits without relying on a watcher race.
func (m *Manager) RefreshClaudeSettings() error {
	if m == nil {
		return nil
	}
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if !m.claudeConfigured {
		return nil
	}
	m.mu.RLock()
	enabled := m.enabled
	m.mu.RUnlock()
	sources, managedPresent, refreshErr := managedSettingsSources(m.claudeManagedDir)
	hash := sha256.New()
	fmt.Fprintf(hash, "enabled:%t:managed:%t:plugin-dirs:%q:repository-dirs:%q:", enabled, managedPresent, m.claudePluginDirs, m.claudeRepositoryDirs)
	totalBytes := 0
	sampled := map[string][]byte{}
	read := func(path string) ([]byte, error) {
		if data, ok := sampled[path]; ok {
			return data, nil
		}
		var data []byte
		var err error
		if m.claudeRegistryPinned && path == filepath.Join(m.claudeUserDir, "plugins", "installed_plugins.json") {
			data = m.pinnedRegistryWithLiveAliases(path)
		} else {
			data, err = readClaudeSettings(path)
		}
		if err != nil {
			return nil, err
		}
		totalBytes += len(data)
		if totalBytes > maxManagedSettingsBytes {
			return nil, fmt.Errorf("Claude settings exceed 16 MiB in total")
		}
		fmt.Fprintf(hash, "%d:%s:%t:%d:", len(path), path, data != nil, len(data))
		hash.Write(data)
		sampled[path] = data
		return data, nil
	}
	readSource := func(source *claudeSettingsSource) error {
		data, err := read(source.path)
		if err != nil {
			return err
		}
		if data == nil {
			return nil
		}
		source.fields, err = settingsFields(data, source.managed)
		if err != nil {
			return fmt.Errorf("Claude settings %s: %w", source.path, err)
		}
		return nil
	}

	var managedDisabled, managedOnly *bool
	if refreshErr == nil {
		for i := range sources {
			if refreshErr = readSource(&sources[i]); refreshErr != nil {
				break
			}
			fields := sources[i].fields
			if fields.disabledSet {
				managedDisabled = fields.disabled
			}
			if fields.managedOnlySet {
				managedOnly = fields.managedOnly
			}
		}
	}
	only := managedOnly != nil && *managedOnly
	disabledManaged := managedDisabled != nil && *managedDisabled
	disabled := false
	ordinaryLoaded := false
	// Mandatory settings are independent of workspace trust and the native
	// enable flag. Ignored lower tiers cannot break managed-only enforcement.
	if refreshErr == nil && (enabled && !only && !disabledManaged || m.claudePluginDiscovery || len(m.claudePluginDirs) > 0 && !only && !disabledManaged) {
		ordinaryLoaded = true
		var ordinary []claudeSettingsSource
		if m.claudeUserDir != "" {
			ordinary = append(ordinary, claudeSettingsSource{path: filepath.Join(m.claudeUserDir, "settings.json"), provenance: "claude:user"})
		}
		for _, directory := range m.claudeProjectRoots() {
			projectSource, localSource := "claude:project", "claude:local"
			if directory != m.claudeProjectDir {
				projectSource, localSource = "claude:repository:"+directory+":project", "claude:repository:"+directory+":local"
			}
			ordinary = append(ordinary,
				claudeSettingsSource{path: filepath.Join(directory, ".claude", "settings.json"), provenance: projectSource},
				claudeSettingsSource{path: filepath.Join(directory, ".claude", "settings.local.json"), provenance: localSource})
		}
		for i := range ordinary {
			if refreshErr = readSource(&ordinary[i]); refreshErr != nil {
				break
			}
			if ordinary[i].fields.disabled != nil {
				disabled = *ordinary[i].fields.disabled
			}
		}
		sources = append(sources, ordinary...)
	}
	if managedDisabled != nil {
		disabled = *managedDisabled
	}
	// HTTP restrictions apply even to managed hooks. In managed-only mode,
	// inspect just these fields in otherwise ignored lower tiers.
	if refreshErr == nil && !ordinaryLoaded {
		paths := []string{}
		if m.claudeUserDir != "" {
			paths = append(paths, filepath.Join(m.claudeUserDir, "settings.json"))
		}
		paths = append(paths, m.claudeProjectSettingsPaths()...)
		for _, path := range paths {
			data, err := read(path)
			if err != nil {
				refreshErr = err
				break
			}
			policy, err := ignoredSettingsHTTPPolicy(data)
			if err != nil {
				refreshErr = fmt.Errorf("Claude settings %s: %w", path, err)
				break
			}
			sources = append(sources, claudeSettingsSource{path: path, fields: claudeSettingsFields{httpPolicy: policy}})
		}
	}
	var selectedPlugins []ClaudePlugin
	if refreshErr == nil && (m.claudePluginDiscovery || !disabledManaged) {
		var pluginSources []claudeSettingsSource
		pluginSources, refreshErr = m.refreshPluginSources(sources, enabled, only, disabled || disabledManaged, disabledManaged, read, &selectedPlugins)
		sources = append(sources, pluginSources...)
	}
	for _, plugin := range selectedPlugins {
		fmt.Fprintf(hash, "plugin-runtime:%s:", plugin.RuntimeFingerprint())
	}
	fingerprint := hex.EncodeToString(hash.Sum(nil))
	if refreshErr == nil && fingerprint == m.claudeFingerprint {
		return nil
	}
	prepared := &Scope{}
	var httpPolicy httpHookPolicy
	for _, source := range sources {
		if source.fields.httpPolicy.urls != nil {
			httpPolicy.urls = append(append([]string{}, httpPolicy.urls...), source.fields.httpPolicy.urls...)
		}
		if source.fields.httpPolicy.env != nil {
			httpPolicy.env = append(append([]string{}, httpPolicy.env...), source.fields.httpPolicy.env...)
		}
	}
	managedActive, pluginActive := false, false
	if refreshErr == nil && !disabledManaged {
		refreshErr = mergeManagedHookDefinitions(sources)
		for i := range sources {
			if refreshErr != nil {
				break
			}
			source := &sources[i]
			if !source.managed && source.plugin == nil {
				if !enabled || only || disabled || source.fields.hooks == nil || source.fields.hooks.Tag == "!!null" {
					continue
				}
				source.definitions, refreshErr = parseClaudeHooks(*source.fields.hooks, 256, true)
			}
			if refreshErr == nil && len(source.definitions) > 0 {
				var registration *Registration
				registration, refreshErr = prepared.prepare(source.provenance+":"+source.path, source.provenance+":"+source.path, true, source.definitions)
				if refreshErr == nil {
					if source.plugin != nil {
						for _, entry := range registration.entries {
							entry.hook.claudePlugin = source.plugin
							entry.hook.claudeManaged = source.plugin.Forced
						}
					}
					registration.Commit()
					pluginActive = pluginActive || source.plugin != nil
					managedActive = managedActive || source.managed
				}
			}
			if refreshErr != nil {
				refreshErr = fmt.Errorf("Claude settings %s: %w", source.path, refreshErr)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claudeError, m.claudeManagedPresent = refreshErr, managedPresent
	m.claudeExplicitPlugins = len(m.claudePluginDirs) > 0
	if refreshErr == nil {
		m.claudeScope, m.claudeDisabled = prepared, disabled
		m.claudeHTTPPolicy = httpPolicy
		m.claudeManagedOnly, m.claudeManagedDisabled, m.claudeManagedActive = only, disabledManaged, managedActive
		m.claudePluginActive = pluginActive
		m.claudePlugins = selectedPlugins
		m.claudeRevision++
		m.claudeFingerprint = fingerprint
	} else {
		// Repairing to the last successful contents must clear transient errors.
		m.claudeFingerprint = ""
	}
	return refreshErr
}

// CheckReady validates mandatory settings even when ordinary hooks are disabled.
func (m *Manager) CheckReady() error {
	if m == nil {
		return nil
	}
	err := m.RefreshClaudeSettings()
	m.mu.RLock()
	required := m.enabled || m.claudeManagedPresent || m.claudeExplicitPlugins
	m.mu.RUnlock()
	if required && err != nil {
		return fmt.Errorf("required Claude settings hooks are unavailable: %w", err)
	}
	return nil
}

func (m *Manager) ClaudeSettingsError() error {
	_ = m.RefreshClaudeSettings()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.claudeError
}

func (m *Manager) ClaudeHooksDisabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.claudeDisabled || m.claudeManagedDisabled
}

func (m *Manager) ExecutionRevision() uint64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.claudeRevision
}

func (m *Manager) CheckExecutionRevision(revision uint64) error {
	if err := m.CheckReady(); err != nil {
		return err
	}
	if m.ExecutionRevision() != revision {
		return fmt.Errorf("hook configuration changed while this tool call was waiting; retry with the current checks")
	}
	return nil
}

// Used only for the native pre-hook half of delegated tool execution.
func (m *Manager) RunNativePreToolInDir(ctx context.Context, workDir, toolName string, args map[string]any) []Result {
	return m.run(ctx, PreTool, NewContext(toolName, args, workDir), false)
}
