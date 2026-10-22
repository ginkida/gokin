package hooks

import (
	"path/filepath"
	"slices"
)

// Explicit runtime registration inherits the session's workspace trust and
// managed settings policy. A settings file cannot register another root.
func (m *Manager) ConfigureClaudeRepositoryDirs(directories []string) {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if slices.Equal(m.claudeRepositoryDirs, directories) {
		return
	}
	m.claudeRepositoryDirs = append([]string(nil), directories...)
	if len(directories) > 0 {
		m.claudePluginDiscovery = true
	}
	m.claudeFingerprint = ""
}

// Callers hold claudeMu, as for the primary project settings and registry.
func (m *Manager) claudeProjectRoots() []string {
	if !m.claudeTrusted {
		return nil
	}
	var roots []string
	if m.claudeProjectDir != "" {
		roots = append(roots, m.claudeProjectDir)
	}
	for _, directory := range m.claudeRepositoryDirs {
		if !slices.Contains(roots, directory) {
			roots = append(roots, directory)
		}
	}
	return roots
}

func (m *Manager) claudeProjectSettingsPaths() []string {
	var paths []string
	for _, directory := range m.claudeProjectRoots() {
		paths = append(paths, filepath.Join(directory, ".claude", "settings.json"), filepath.Join(directory, ".claude", "settings.local.json"))
	}
	return paths
}

func (m *Manager) claudeMatchesProject(directory string) bool {
	for _, project := range m.claudeProjectRoots() {
		if samePluginProject(directory, project) {
			return true
		}
	}
	return false
}
