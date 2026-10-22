package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gokin/internal/pluginpolicy"
	"path/filepath"
)

// PreviewClaudePluginInstalls reads pending installed versions through a
// separate authority. It preserves this host's trust/managed policy and
// sideload roots, without changing the running session or starting processes.
func (m *Manager) PreviewClaudePluginInstalls() (*Manager, error) {
	if m == nil {
		return nil, fmt.Errorf("plugin authority is unavailable")
	}
	m.claudeMu.Lock()
	project, user, managed, trusted := m.claudeProjectDir, m.claudeUserDir, m.claudeManagedDir, m.claudeTrusted
	discovery := m.claudePluginDiscovery
	directories := append([]string(nil), m.claudePluginDirs...)
	repositories := append([]string(nil), m.claudeRepositoryDirs...)
	secrets := m.claudeSecretOptions
	m.claudeMu.Unlock()
	preview := NewManager(false, project)
	preview.ConfigureClaudeManagedSettings(managed)
	preview.ConfigureClaudeSettings(project, user, trusted)
	preview.ConfigureClaudeRepositoryDirs(repositories)
	if discovery {
		preview.ConfigureClaudePlugins(directories)
	}
	if secrets != nil {
		preview.ConfigureClaudeSecretOptions(secrets)
	}
	return preview, nil
}

// PinClaudePluginInstalls captures immutable installed-version paths for this
// session. Settings and managed policy remain live, so disabling or blocking a
// plugin still revokes its components. Calling it again explicitly adopts the
// newly installed versions, as /reload-plugins does.
func (m *Manager) PinClaudePluginInstalls() error {
	if m == nil {
		return nil
	}
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if m.claudeUserDir == "" {
		return nil
	}
	data, err := readClaudeSettings(filepath.Join(m.claudeUserDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return err
	}
	known := map[string]bool{}
	pinnedFlags := map[string]bool{}
	pinnedConfigs := map[string]json.RawMessage{}
	if data != nil {
		var registry struct {
			Version int                              `json:"version"`
			Plugins map[string][]claudePluginInstall `json:"plugins"`
		}
		if err := pluginJSON(data, &registry); err != nil {
			return err
		}
		if registry.Version != 2 || registry.Plugins == nil || len(registry.Plugins) > maxClaudePlugins {
			return fmt.Errorf("invalid installed plugin registry; expected version 2 and a bounded plugins object")
		}
		for id, records := range registry.Plugins {
			if !validPluginID(id) || len(records) > 256 {
				return fmt.Errorf("invalid installed plugin record")
			}
			known[id] = true
		}
	}
	// New ordinary declarations belong to the next explicit reload. Remember
	// existing declarations too, so a missing plugin still fails closed rather
	// than disappearing merely because its install couldn't be repaired.
	paths := []string{filepath.Join(m.claudeUserDir, "settings.json")}
	paths = append(paths, m.claudeProjectSettingsPaths()...)
	for _, path := range paths {
		settings, err := readClaudeSettings(path)
		if err != nil {
			return err
		}
		if settings == nil {
			continue
		}
		fields, err := settingsFields(settings, false)
		if err != nil {
			return err
		}
		enabled := map[string]bool{}
		if err := mergePluginEnable(enabled, fields.plugins); err != nil {
			return err
		}
		for id, on := range enabled {
			known[id] = true
			pinnedFlags[id] = on
		}
		if path == filepath.Join(m.claudeUserDir, "settings.json") && fields.pluginConfigs != nil {
			var configs map[string]any
			if err := fields.pluginConfigs.Decode(&configs); err != nil {
				return err
			}
			for id, value := range configs {
				pinnedConfigs[id], _ = json.Marshal(value)
			}
		}
	}
	m.claudePinnedRegistry = bytes.Clone(data)
	m.claudePinnedKnownIDs = known
	m.claudePinnedPluginFlags = pinnedFlags
	m.claudePinnedPluginConfigs = pinnedConfigs
	m.claudeRegistryPinned = true
	m.claudeFingerprint = ""
	return nil
}

// Keep version paths pinned while sampling only the live rename metadata.
// A corrupt replacement registry cannot erase a previously valid snapshot.
func (m *Manager) pinnedRegistryWithLiveAliases(path string) []byte {
	data := m.claudePinnedRegistry
	live, err := readClaudeSettings(path)
	if err != nil || live == nil {
		return data
	}
	var current map[string]json.RawMessage
	if pluginJSON(live, &current) != nil {
		return data
	}
	aliases, err := pluginpolicy.ReadPluginAliases(current)
	if err != nil || len(aliases) == 0 {
		return data
	}
	var pinned map[string]json.RawMessage
	if data == nil {
		pinned = map[string]json.RawMessage{"version": json.RawMessage("2"), "plugins": json.RawMessage("{}")}
	} else if pluginJSON(data, &pinned) != nil {
		return data
	}
	pinned[pluginpolicy.PluginAliasesField] = current[pluginpolicy.PluginAliasesField]
	result, err := json.Marshal(pinned)
	if err != nil {
		return data
	}
	return result
}

func (m *Manager) sessionPluginAliases(live pluginpolicy.PluginAliases) pluginpolicy.PluginAliases {
	m.claudeKeptPluginAliases = map[string]string{}
	if !m.claudeRegistryPinned || m.claudePinnedRegistry == nil {
		return live
	}
	var pinned map[string]json.RawMessage
	if pluginJSON(m.claudePinnedRegistry, &pinned) != nil {
		return live
	}
	oldAliases, err := pluginpolicy.ReadPluginAliases(pinned)
	if err != nil {
		return live
	}
	var records map[string][]json.RawMessage
	if json.Unmarshal(pinned["plugins"], &records) != nil {
		return live
	}
	effective := pluginpolicy.PluginAliases{}
	for id, target := range live {
		effective[id] = target
	}
	for id := range live {
		if len(records[id]) == 0 {
			continue
		}
		if _, changed, _ := oldAliases.Resolve(id); changed {
			continue
		}
		target, _, _ := live.Resolve(id)
		m.claudeKeptPluginAliases[id] = target
		delete(effective, id)
	}
	return effective
}

func (m *Manager) keepPinnedPluginFlags(flags map[string]bool, managed bool) {
	for old, target := range m.claudeKeptPluginAliases {
		if _, set := flags[old]; set {
			continue
		}
		if value, set := flags[target]; set && !value {
			flags[old] = false
			continue
		}
		if !managed {
			if value, set := m.claudePinnedPluginFlags[old]; set {
				flags[old] = value
			}
		}
	}
}
