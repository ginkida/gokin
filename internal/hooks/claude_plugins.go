package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gokin/internal/plugindeps"
	"gokin/internal/pluginpolicy"
	"gokin/internal/pluginsecrets"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxClaudePlugins = pluginpolicy.MaxEnabledPlugins

type claudePluginRuntime struct {
	ID, Root, Data string
	Forced         bool
	Explicit       bool
	Options        map[string]string
	Sensitive      map[string]bool
	Project        bool
}

type claudePluginManifest struct {
	Name           string                        `json:"name"`
	Version        string                        `json:"version"`
	DefaultEnabled *bool                         `json:"defaultEnabled"`
	Hooks          json.RawMessage               `json:"hooks"`
	UserConfig     map[string]claudePluginOption `json:"userConfig"`
	Dependencies   json.RawMessage               `json:"dependencies"`
	Skills         json.RawMessage               `json:"skills"`
	Commands       json.RawMessage               `json:"commands"`
	Agents         json.RawMessage               `json:"agents"`
	MCPServers     json.RawMessage               `json:"mcpServers"`
	LSPServers     json.RawMessage               `json:"lspServers"`
}

type claudePluginOption struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Default     any      `json:"default"`
	Multiple    bool     `json:"multiple"`
	Sensitive   bool     `json:"sensitive"`
	Options     []string `json:"options"`
	Min         *float64 `json:"min"`
	Max         *float64 `json:"max"`
}

type claudePluginInstall struct {
	Scope             string               `json:"scope"`
	InstallPath       string               `json:"installPath"`
	ProjectPath       string               `json:"projectPath"`
	GokinSource       pluginpolicy.Source  `json:"gokinSource"`
	MarketplaceSource *pluginpolicy.Source `json:"gokinMarketplaceSource"`
	ResolvedVersion   string               `json:"gokinResolvedVersion"`
	LinkedRoot        string               `json:"gokinLinkedRoot"`
	LinkEntry         json.RawMessage      `json:"gokinLinkEntry"`
}

type claudePluginCandidate struct {
	ID, Root   string
	Priority   int
	Project    bool
	Version    string
	LinkedRoot string
	LinkEntry  json.RawMessage
}

// ConfigureClaudeDependencyValidation is for offline, isolated manifest
// validation before an installation graph exists. Runtime hosts leave it off.
func (m *Manager) ConfigureClaudeDependencyValidation() {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	m.claudeSkipDependencyChecks = true
	m.claudeFingerprint = ""
}

// ConfigureClaudeSecretOptions supplies in-memory proposed values to isolated
// validation. Runtime hosts use the authenticated, user-owned encrypted store.
func (m *Manager) ConfigureClaudeSecretOptions(read func(string) (map[string]any, error)) {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	m.claudeSecretOptions = read
	m.claudeFingerprint = ""
}

func (m *Manager) ClaudeDependencyWarnings() []string {
	_ = m.RefreshClaudeSettings()
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	return append([]string(nil), m.claudeDependencyWarnings...)
}

var pluginOptionPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var pluginOptionReference = regexp.MustCompile(`\$\{user_config\.([^{}]+)\}`)

// DisableClaudePlugins disables all plugin components for this runtime while
// retaining ordinary/managed settings hooks and native skills/MCP.
func (m *Manager) DisableClaudePlugins() {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	m.claudePluginsDisabled = true
	m.claudePluginDiscovery = false
	m.claudePluginDirs = nil
	m.claudeFingerprint = ""
}

// ConfigureClaudePlugins supplies explicit session-only plugin roots. Project
// settings cannot add sideload roots. Bare builders reject explicit plugins.
func (m *Manager) ConfigureClaudePlugins(directories []string) {
	m.claudeMu.Lock()
	defer m.claudeMu.Unlock()
	if m.claudePluginDiscovery && slices.Equal(m.claudePluginDirs, directories) {
		return
	}
	m.claudePluginDiscovery = true
	m.claudePluginDirs = append([]string(nil), directories...)
	m.claudeConfigured = true
	m.claudeFingerprint = ""
}

func pluginJSON(data []byte, target any) error {
	if _, err := settingsFields(data, false); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func mergePluginEnable(target map[string]bool, node *yaml.Node, aliases ...pluginpolicy.PluginAliases) error {
	return pluginpolicy.MergePluginEnable(target, node, aliases...)
}

func validPluginName(name string) bool { return pluginpolicy.ValidPluginName(name) }

// A misspelled schema control must not silently change how an option reaches a
// hook process, particularly the sensitive and multiple controls.
func (option *claudePluginOption) UnmarshalJSON(data []byte) error {
	type plain claudePluginOption
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	switch value.Type {
	case "string", "boolean", "number", "file", "directory":
	default:
		return fmt.Errorf("unsupported userConfig type")
	}
	if value.Multiple && value.Type != "string" {
		return fmt.Errorf("multiple userConfig requires string type")
	}
	if len(value.Options) > 0 && (value.Type != "string" || value.Multiple || value.Sensitive) {
		return fmt.Errorf("userConfig choices require a non-sensitive single string")
	}
	if (value.Min != nil || value.Max != nil) && value.Type != "number" {
		return fmt.Errorf("userConfig bounds require number type")
	}
	if value.Min != nil && value.Max != nil && *value.Min > *value.Max {
		return fmt.Errorf("userConfig minimum exceeds maximum")
	}
	*option = claudePluginOption(value)
	if option.Default != nil {
		if _, err := pluginOptionText(option.Default, *option); err != nil {
			return fmt.Errorf("invalid userConfig default: %w", err)
		}
	}
	return nil
}

func validPluginID(id string) bool {
	return pluginpolicy.ValidPluginID(id)
}

// Public checks keep lifecycle tooling aligned with discovery's accepted
// manifest names and existing registry identifiers.
func ValidClaudePluginName(name string) bool { return validPluginName(name) }
func ValidClaudePluginID(id string) bool     { return validPluginID(id) }

func pluginCanonicalDir(directory string) (string, error) {
	path, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("plugin directory unavailable: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("plugin root must be a directory")
	}
	return path, nil
}

func samePluginProject(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err := pluginCanonicalDir(a)
	if err != nil {
		return false
	}
	bb, err := pluginCanonicalDir(b)
	return err == nil && aa == bb
}

func containedPluginPath(root, relative string) (string, error) {
	if !strings.HasPrefix(relative, "./") || filepath.Ext(relative) != ".json" || strings.Contains(relative, "\\") {
		return "", fmt.Errorf("plugin hook path must start with ./ and name a .json file")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".." {
			return "", fmt.Errorf("plugin hook path escapes plugin directory")
		}
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("plugin hook path unavailable: %w", err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("plugin hook path escapes plugin directory")
	}
	return path, nil
}

func pluginChildRoots(directory string, explicit bool) ([]string, error) {
	f, err := os.Open(directory)
	if os.IsNotExist(err) && !explicit {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plugin directory: %w", err)
	}
	defer f.Close()
	entries, err := f.ReadDir(4097)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 4096 {
		return nil, fmt.Errorf("plugin directory exceeds 4096 entries")
	}
	var roots []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".claude-plugin", "plugin.json")); err == nil {
			roots = append(roots, path)
		}
	}
	sort.Strings(roots)
	if len(roots) > maxClaudePlugins {
		return nil, fmt.Errorf("plugin directories exceed %d plugins", maxClaudePlugins)
	}
	return roots, nil
}

// refreshPluginSources reads through the same hash/budget as settings. No shell,
// package install or network access occurs during discovery or compilation.
func (m *Manager) refreshPluginSources(sources []claudeSettingsSource, enabled, only, disabled, managedDisabled bool, read func(string) ([]byte, error), selected *[]ClaudePlugin) ([]claudeSettingsSource, error) {
	if m.claudePluginsDisabled {
		return nil, nil
	}
	m.claudeDependencyWarnings = nil
	var policyDocuments [][]byte
	for _, source := range sources {
		if source.managed {
			policyDocuments = append(policyDocuments, source.fields.managedPluginPolicy)
		}
	}
	marketplacePolicy, err := pluginpolicy.FromDocuments(policyDocuments)
	if err != nil {
		return nil, err
	}
	knownMarketplaces := map[string]struct {
		Source pluginpolicy.Source `json:"source"`
	}{}
	if m.claudeUserDir != "" && (marketplacePolicy.RestrictsMarketplaces() || len(marketplacePolicy.Required) > 0) {
		data, err := read(filepath.Join(m.claudeUserDir, "plugins", "known_marketplaces.json"))
		if err != nil {
			return nil, err
		}
		if data != nil {
			if pluginJSON(data, &knownMarketplaces) != nil {
				return nil, fmt.Errorf("invalid known marketplace registry")
			}
		}
	}
	aliases := pluginpolicy.PluginAliases{}
	if m.claudeUserDir != "" {
		data, err := read(filepath.Join(m.claudeUserDir, "plugins", "installed_plugins.json"))
		if err != nil {
			return nil, err
		}
		if data != nil {
			var doc map[string]json.RawMessage
			if pluginJSON(data, &doc) != nil {
				return nil, fmt.Errorf("invalid installed plugin registry")
			}
			aliases, err = pluginpolicy.ReadPluginAliases(doc)
			if err != nil {
				return nil, err
			}
		}
	}
	m.claudePluginAliases = aliases
	aliases = m.sessionPluginAliases(aliases)
	marketplacePolicy.Enabled = aliases.RenameFlags(marketplacePolicy.Enabled)
	ordinary, managed := map[string]bool{}, map[string]bool{}
	managedOriginal := map[string]bool{}
	sideloadDisabled := false
	for _, source := range sources {
		target := ordinary
		if source.managed {
			target = managed
			if source.fields.sideloadSet {
				sideloadDisabled = source.fields.sideloadDisabled != nil && *source.fields.sideloadDisabled
			}
		}
		if source.managed {
			if err := mergePluginEnable(managedOriginal, source.fields.plugins); err != nil {
				return nil, err
			}
		}
		if err := mergePluginEnable(target, source.fields.plugins, aliases); err != nil {
			return nil, fmt.Errorf("Claude settings %s: %w", source.path, err)
		}
	}
	m.keepPinnedPluginFlags(ordinary, false)
	m.keepPinnedPluginFlags(managed, true)
	for old := range managedOriginal {
		target, changed, _ := aliases.Resolve(old)
		if changed {
			notice := old + ": Removed from marketplace (managed declaration unchanged)"
			if target != "" {
				notice = old + ": Renamed to " + target + " (managed declaration unchanged)"
			}
			m.claudeDependencyWarnings = append(m.claudeDependencyWarnings, notice)
		}
	}
	hasForced := false
	for _, on := range managed {
		hasForced = hasForced || on
	}
	if sideloadDisabled && len(m.claudePluginDirs) > 0 {
		return nil, fmt.Errorf("--plugin-dir is disabled by managed disableSideloadFlags policy")
	}
	if !m.claudePluginDiscovery && !hasForced && (only || disabled || !enabled && len(m.claudePluginDirs) == 0) {
		return nil, nil
	}
	policy := map[string]bool{}
	for id, on := range ordinary {
		if m.claudeRegistryPinned && !m.claudePinnedKnownIDs[id] && !strings.HasSuffix(id, "@inline") && !strings.HasSuffix(id, "@skills-dir") {
			continue
		}
		policy[id] = on
	}
	for id, on := range managed {
		policy[id] = on
	}
	var candidates []claudePluginCandidate
	// Installed registry paths are user-owned, including in-place installations.
	if m.claudeUserDir != "" {
		path := filepath.Join(m.claudeUserDir, "plugins", "installed_plugins.json")
		data, err := read(path)
		if err != nil {
			return nil, err
		}
		if data != nil {
			var registry struct {
				Version int                              `json:"version"`
				Plugins map[string][]claudePluginInstall `json:"plugins"`
			}
			if err := pluginJSON(data, &registry); err != nil {
				return nil, fmt.Errorf("Claude plugin registry %s: %w", path, err)
			}
			if registry.Plugins == nil {
				return nil, fmt.Errorf("Claude plugin registry must contain a plugins object")
			}
			if registry.Version != 2 {
				return nil, fmt.Errorf("Claude plugin registry version %d is unsupported; expected 2", registry.Version)
			}
			if len(registry.Plugins) > maxClaudePlugins {
				return nil, fmt.Errorf("installed plugin registry exceeds %d plugins", maxClaudePlugins)
			}
			var ids []string
			for id := range registry.Plugins {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				if m.claudeRegistryPinned && !m.claudePinnedKnownIDs[id] && !managed[id] {
					continue
				}
				if _, changed, _ := aliases.Resolve(id); changed {
					continue
				}
				if !validPluginID(id) {
					return nil, fmt.Errorf("invalid plugin identifier in installed registry")
				}
				if on, set := policy[id]; set && !on {
					continue
				}
				if !m.claudePluginDiscovery && (!enabled || only || disabled) && !managed[id] {
					continue
				}
				records := registry.Plugins[id]
				if len(records) > 256 {
					return nil, fmt.Errorf("plugin install records exceed 256")
				}
				best, rank := -1, 0
				for i, record := range records {
					market := strings.SplitN(id, "@", 2)[1]
					required, requiredSet := marketplacePolicy.Required[market]
					required = pluginpolicy.ResolveLocalSource(required, m.claudeProjectDir)
					if requiredSet && managed[id] && (record.MarketplaceSource == nil || !pluginpolicy.EqualSource(*record.MarketplaceSource, required)) {
						// A same-name catalog from a lower tier cannot provide a
						// managed plugin after its required source changes.
						continue
					}
					candidateRank, err := m.claudePluginInstallRank(record)
					if err != nil {
						return nil, err
					}
					if candidateRank > rank {
						best, rank = i, candidateRank
					}
				}
				if best >= 0 {
					if marketplacePolicy.RestrictsMarketplaces() {
						market := strings.SplitN(id, "@", 2)[1]
						source := knownMarketplaces[market].Source
						if required, ok := marketplacePolicy.Required[market]; ok && source.Kind == "" {
							source = required
						}
						if source.Kind == "" {
							return nil, fmt.Errorf("plugin %s has no known marketplace provenance under managed policy", id)
						}
						if err := marketplacePolicy.Check(source); err != nil {
							return nil, fmt.Errorf("plugin %s: %w", id, err)
						}
						if installedSource := records[best].MarketplaceSource; installedSource != nil {
							if err := marketplacePolicy.Check(*installedSource); err != nil {
								return nil, fmt.Errorf("plugin %s: installed source: %w", id, err)
							}
						}
					}
					if marketplacePolicy.DisabledCommands && records[best].GokinSource.Kind == "command" {
						return nil, fmt.Errorf("plugin %s uses a disabled command source", id)
					}
					if !filepath.IsAbs(records[best].InstallPath) {
						return nil, fmt.Errorf("installed plugin path must be absolute")
					}
					if records[best].GokinSource.Kind == "command" && records[best].GokinSource.Mode == "link" && records[best].LinkedRoot == "" {
						return nil, fmt.Errorf("linked plugin source path is unavailable")
					}
					if records[best].LinkedRoot != "" && (records[best].GokinSource.Kind != "command" || records[best].GokinSource.Mode != "link") {
						return nil, fmt.Errorf("invalid linked plugin source")
					}
					priority := 2
					if managed[id] {
						priority = 4
					}
					candidates = append(candidates, claudePluginCandidate{id, records[best].InstallPath, priority, rank > 1, records[best].ResolvedVersion, records[best].LinkedRoot, records[best].LinkEntry})
				}
			}
		}
	}
	// Automatic skills-directory plugins respect the same project trust floor.
	if (m.claudePluginDiscovery || enabled && !only && !disabled || hasForced) && marketplacePolicy.Check(pluginpolicy.Source{Kind: "skills-dir"}) == nil {
		directories := []string{}
		if m.claudeUserDir != "" {
			directories = append(directories, filepath.Join(m.claudeUserDir, "skills"))
		}
		for _, directory := range m.claudeProjectRoots() {
			directories = append(directories, filepath.Join(directory, ".claude", "skills"))
		}
		for i, directory := range directories {
			roots, err := pluginChildRoots(directory, false)
			if err != nil {
				return nil, err
			}
			for _, root := range roots {
				candidates = append(candidates, claudePluginCandidate{"", root, i, m.claudeMatchesProject(filepath.Dir(filepath.Dir(directory))), "", "", nil})
			}
		}
	}
	if len(m.claudePluginDirs) > 0 && !sideloadDisabled && (m.claudePluginDiscovery || !only && !disabled || hasForced) {
		if len(m.claudePluginDirs) > 32 {
			return nil, fmt.Errorf("--plugin-dir exceeds 32 roots")
		}
		for _, directory := range m.claudePluginDirs {
			root, err := pluginCanonicalDir(directory)
			if err != nil {
				return nil, err
			}
			_, manifestErr := os.Stat(filepath.Join(root, ".claude-plugin", "plugin.json"))
			_, hooksErr := os.Stat(filepath.Join(root, "hooks", "hooks.json"))
			_, skillsErr := os.Stat(filepath.Join(root, "skills"))
			_, commandsErr := os.Stat(filepath.Join(root, "commands"))
			_, rootSkillErr := os.Stat(filepath.Join(root, "SKILL.md"))
			if manifestErr == nil || hooksErr == nil || skillsErr == nil || commandsErr == nil || rootSkillErr == nil {
				candidates = append(candidates, claudePluginCandidate{"", root, 3, false, "", "", nil})
				continue
			}
			roots, err := pluginChildRoots(root, true)
			if err != nil {
				return nil, err
			}
			if len(roots) == 0 {
				return nil, fmt.Errorf("--plugin-dir contains no plugin manifest or components")
			}
			for _, root := range roots {
				candidates = append(candidates, claudePluginCandidate{"", root, 3, false, "", "", nil})
			}
		}
	}
	if len(candidates) > maxClaudePlugins {
		return nil, fmt.Errorf("plugin discovery exceeds %d plugins", maxClaudePlugins)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Priority > candidates[j].Priority })
	locked := map[string]bool{}
	for id := range managed {
		locked[strings.SplitN(id, "@", 2)[0]] = true
	}
	seenNames, loaded := map[string]bool{}, map[string]bool{}
	var result []claudeSettingsSource
	dependencyNodes := map[string]plugindeps.Node{}
	unavailable := map[string]error{}
	hookManifests := map[string]struct {
		path string
		raw  json.RawMessage
	}{}
	suppressed := map[string]bool{}
	for _, candidate := range candidates {
		rootPath := candidate.Root
		if candidate.LinkedRoot != "" {
			var err error
			rootPath, err = pluginpolicy.ResolveLinkedRoot(candidate.Root, candidate.LinkedRoot, m.claudeProjectDir)
			if errors.Is(err, pluginpolicy.ErrLinkedWorkspace) {
				suppressed[candidate.ID] = true
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("plugin %s: %w", candidate.ID, err)
			}
		}
		root, err := pluginCanonicalDir(rootPath)
		if err != nil {
			if candidate.ID != "" && errors.Is(err, os.ErrNotExist) {
				unavailable[candidate.ID] = err
				continue
			}
			return nil, fmt.Errorf("plugin %s: %w", candidate.ID, err)
		}
		path := filepath.Join(root, ".claude-plugin", "plugin.json")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			var pathErr error
			path, pathErr = containedPluginPath(root, "./.claude-plugin/plugin.json")
			if pathErr != nil {
				return nil, pathErr
			}
		}
		data, err := read(path)
		if err != nil {
			return nil, err
		}
		if candidate.LinkedRoot != "" {
			manifestFields, entryFields := map[string]json.RawMessage{}, map[string]json.RawMessage{}
			if data != nil && pluginJSON(data, &manifestFields) != nil {
				return nil, fmt.Errorf("invalid linked plugin manifest")
			}
			if pluginJSON(candidate.LinkEntry, &entryFields) != nil || entryFields == nil {
				return nil, fmt.Errorf("invalid linked marketplace entry")
			}
			merged, err := pluginpolicy.MergePluginManifest(manifestFields, entryFields)
			if err != nil {
				return nil, err
			}
			data, _ = json.Marshal(merged)
		}
		var manifest claudePluginManifest
		if data == nil {
			manifest.Name = filepath.Base(root)
			if candidate.ID != "" {
				manifest.Name = strings.SplitN(candidate.ID, "@", 2)[0]
			}
		} else if err := pluginJSON(data, &manifest); err != nil {
			return nil, fmt.Errorf("plugin manifest %s: %w", path, err)
		}
		if !validPluginName(manifest.Name) {
			return nil, fmt.Errorf("invalid plugin manifest name in %s", path)
		}
		id := candidate.ID
		if id == "" {
			origin := "skills-dir"
			if candidate.Priority == 3 {
				origin = "inline"
			}
			id = manifest.Name + "@" + origin
		}
		if candidate.ID == "" && !locked[manifest.Name] {
			dependencyNodes[id] = plugindeps.Node{ID: id, Name: manifest.Name, Local: true}
		}
		if on, set := policy[id]; set {
			if !on {
				continue
			}
		} else if manifest.DefaultEnabled != nil && !*manifest.DefaultEnabled {
			continue
		}
		forced := managed[id]
		if !m.claudePluginDiscovery && (only || disabled || !enabled && candidate.Priority != 3) && !forced {
			continue
		}
		if !forced && locked[manifest.Name] {
			continue
		}
		if seenNames[manifest.Name] {
			continue
		}
		dependencies, err := plugindeps.Parse(manifest.Dependencies)
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %w", id, err)
		}
		for i := range dependencies {
			target, changed, _ := aliases.Resolve(dependencies[i].ID(id))
			if changed && target != "" {
				dependencies[i].Name, dependencies[i].Marketplace, _ = strings.Cut(target, "@")
			}
		}
		version := manifest.Version
		if candidate.Version != "" {
			version = candidate.Version
		}
		dependencyNodes[id] = plugindeps.Node{ID: id, Name: manifest.Name, Version: version, Local: candidate.ID == "", Enabled: true, Dependencies: dependencies}
		seenNames[manifest.Name], loaded[id] = true, true
		if forced {
			locked[manifest.Name] = true
		}
		if m.claudeUserDir == "" {
			return nil, fmt.Errorf("plugin %s requires a Claude user directory", id)
		}
		runtime := &claudePluginRuntime{ID: id, Root: root, Forced: forced, Explicit: candidate.Priority == 3, Project: candidate.Project, Data: filepath.Join(m.claudeUserDir, "plugins", "data", ClaudePluginStorageKey(id))}
		if len(manifest.UserConfig) > 0 {
			options, err := m.pluginOptions(id, manifest.UserConfig, sources, read)
			if err != nil {
				return nil, fmt.Errorf("plugin %s: %w", id, err)
			}
			runtime.Options = options
			runtime.Sensitive = map[string]bool{}
			for key, field := range manifest.UserConfig {
				if field.Sensitive {
					runtime.Sensitive[key] = true
				}
			}
		}
		*selected = append(*selected, ClaudePlugin{Name: manifest.Name, Version: version, Dependencies: dependencies, Skills: append(json.RawMessage(nil), manifest.Skills...), Commands: append(json.RawMessage(nil), manifest.Commands...), Agents: append(json.RawMessage(nil), manifest.Agents...), MCPServers: append(json.RawMessage(nil), manifest.MCPServers...), LSPServers: append(json.RawMessage(nil), manifest.LSPServers...), runtime: runtime})
		hookManifests[id] = struct {
			path string
			raw  json.RawMessage
		}{path, manifest.Hooks}
	}
	requiredDependencies := map[string]bool{}
	for id, node := range dependencyNodes {
		for _, dependency := range node.Dependencies {
			requiredDependencies[dependency.ID(id)] = true
		}
	}
	for id, err := range unavailable {
		if !requiredDependencies[id] || managed[id] {
			return nil, fmt.Errorf("plugin %s: %w", id, err)
		}
	}
	for id, on := range policy {
		if !on || loaded[id] || suppressed[id] || !managed[id] && (strings.HasSuffix(id, "@inline") || strings.HasSuffix(id, "@skills-dir")) {
			continue
		}
		if !m.claudePluginDiscovery && (!enabled || only || disabled) && !managed[id] {
			continue
		}
		name := strings.SplitN(id, "@", 2)[0]
		if seenNames[name] && !managed[id] {
			continue
		}
		if requiredDependencies[id] && !managed[id] {
			continue // The offline graph emits a diagnostic for its consumers.
		}
		if aliases.IsTarget(id) {
			m.claudeDependencyWarnings = append(m.claudeDependencyWarnings, id+": renamed plugin is not cached; install it explicitly with gokin plugin install "+id)
			continue
		}
		return nil, fmt.Errorf("enabled plugin %s is unavailable for this workspace; install it with gokin plugin install %s or supply --plugin-dir", id, id)
	}
	if !m.claudeSkipDependencyChecks {
		issues := plugindeps.Check(dependencyNodes)
		valid := (*selected)[:0]
		for _, plugin := range *selected {
			if issue := issues[plugin.ID()]; issue != "" {
				if plugin.runtime.Forced {
					return nil, fmt.Errorf("managed plugin %s: %s", plugin.ID(), issue)
				}
				m.claudeDependencyWarnings = append(m.claudeDependencyWarnings, plugin.ID()+": "+issue)
			} else {
				valid = append(valid, plugin)
			}
		}
		*selected = valid
	}
	byID := map[string]ClaudePlugin{}
	for _, plugin := range *selected {
		byID[plugin.ID()] = plugin
	}
	for pass := 0; pass < len(*selected); pass++ {
		changed := false
		for _, plugin := range *selected {
			for _, dependency := range plugin.Dependencies {
				target := dependency.ID(plugin.ID())
				for _, local := range []string{dependency.Name + "@inline", dependency.Name + "@skills-dir"} {
					if _, exists := byID[local]; exists {
						target = local
						break
					}
				}
				if child, exists := byID[target]; exists {
					if plugin.runtime.Forced && !child.runtime.Forced {
						child.runtime.Forced, changed = true, true
					}
					if plugin.runtime.Explicit && !child.runtime.Explicit {
						child.runtime.Explicit, changed = true, true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	for _, plugin := range *selected {
		runtime := plugin.runtime
		if managedDisabled || (only || disabled || !enabled && !runtime.Explicit) && !runtime.Forced {
			continue
		}
		manifest := hookManifests[plugin.ID()]
		pluginSources, err := readPluginHooks(plugin.Root(), manifest.path, manifest.raw, runtime, read)
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %w", plugin.ID(), err)
		}
		result = append(result, pluginSources...)
	}
	return result, nil
}

func sanitizePluginID(id string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			return c
		}
		return '-'
	}, id)
}

// ClaudePluginStorageKey binds filesystem storage to the complete identifier.
// Replacing punctuation alone lets a-b@c and a@b-c share persistent data.
func ClaudePluginStorageKey(id string) string {
	prefix := sanitizePluginID(id)
	if len(prefix) > 48 {
		prefix = prefix[:48]
	}
	digest := sha256.Sum256([]byte(id))
	return prefix + "-" + hex.EncodeToString(digest[:])
}

func readPluginHooks(root, manifestPath string, declared json.RawMessage, runtime *claudePluginRuntime, read func(string) ([]byte, error)) ([]claudeSettingsSource, error) {
	var sources []claudeSettingsSource
	add := func(path string, node *yaml.Node) error {
		if node == nil {
			return fmt.Errorf("plugin hooks file must contain a hooks event map")
		}
		definitions, err := parseClaudeHooks(*node, 256, true)
		if err != nil {
			return err
		}
		for _, definition := range definitions {
			if definition.Args == nil && strings.Contains(definition.Command, "${user_config.") {
				return fmt.Errorf("shell-form plugin hooks cannot interpolate user_config; use exec args or CLAUDE_PLUGIN_OPTION variables")
			}
			values := append([]string{definition.Command, definition.Prompt, definition.Model, definition.URL}, definition.Args...)
			if definition.MCPInput != nil {
				data, _ := json.Marshal(definition.MCPInput)
				values = append(values, string(data))
			}
			for _, value := range definition.Headers {
				values = append(values, value)
			}
			for _, value := range values {
				for _, match := range pluginOptionReference.FindAllStringSubmatch(value, -1) {
					if _, ok := runtime.Options[match[1]]; !ok {
						return fmt.Errorf("hook references an unavailable user_config option")
					}
				}
			}
		}
		sources = append(sources, claudeSettingsSource{path: path, provenance: "claude:plugin:" + runtime.ID, definitions: definitions, managed: runtime.Forced, plugin: runtime})
		return nil
	}
	load := func(path string, required bool) error {
		data, err := read(path)
		if err != nil {
			return err
		}
		if data == nil {
			if required {
				return fmt.Errorf("plugin hook file unavailable: %s", path)
			}
			return nil
		}
		fields, err := settingsFields(data, false)
		if err != nil {
			return err
		}
		return add(path, fields.hooks)
	}
	defaultPath := filepath.Join(root, "hooks", "hooks.json")
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		path, err := containedPluginPath(root, "./hooks/hooks.json")
		if err != nil {
			return nil, err
		}
		if err := load(path, true); err != nil {
			return nil, err
		}
	} else {
		if _, err := read(defaultPath); err != nil {
			return nil, err
		}
	}
	if len(declared) == 0 || string(declared) == "null" {
		return sources, nil
	}
	var values []json.RawMessage
	if declared[0] == '[' {
		if err := json.Unmarshal(declared, &values); err != nil {
			return nil, err
		}
	} else {
		values = []json.RawMessage{declared}
	}
	if len(values) > 256 {
		return nil, fmt.Errorf("plugin manifest exceeds 256 hook sources")
	}
	for i, value := range values {
		var relative string
		if err := json.Unmarshal(value, &relative); err == nil {
			path, err := containedPluginPath(root, relative)
			if err != nil {
				return nil, err
			}
			if path == defaultPath {
				continue
			}
			if err := load(path, true); err != nil {
				return nil, err
			}
		} else {
			var document yaml.Node
			if err := yaml.Unmarshal(value, &document); err != nil {
				return nil, err
			}
			if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
				return nil, fmt.Errorf("plugin hooks must be a path, event map or array")
			}
			if err := add(fmt.Sprintf("%s#hooks[%d]", manifestPath, i), document.Content[0]); err != nil {
				return nil, err
			}
		}
	}
	return sources, nil
}

func (m *Manager) pluginOptions(id string, schema map[string]claudePluginOption, sources []claudeSettingsSource, read func(string) ([]byte, error)) (map[string]string, error) {
	if len(schema) > 64 {
		return nil, fmt.Errorf("plugin userConfig exceeds 64 options")
	}
	optionID := id
	if target, changed, _ := m.claudePluginAliases.Resolve(id); changed && target != "" {
		optionID = target
	}
	if target, kept := m.claudeKeptPluginAliases[id]; kept && target == "" {
		if config, set := m.claudePinnedPluginConfigs[id]; set {
			data, _ := json.Marshal(map[string]any{"pluginConfigs": map[string]json.RawMessage{id: config}})
			fields, err := settingsFields(data, false)
			if err != nil {
				return nil, err
			}
			sources = append(append([]claudeSettingsSource(nil), sources...), claudeSettingsSource{provenance: "claude:user", fields: fields})
		}
	}
	options := map[string]any{}
	// Only user and managed sources own pluginConfigs. If ordinary hooks were
	// excluded, sample the user settings solely for these required option values.
	hasUser := false
	for _, source := range sources {
		hasUser = hasUser || source.provenance == "claude:user"
	}
	if !hasUser && m.claudeUserDir != "" {
		path := filepath.Join(m.claudeUserDir, "settings.json")
		data, err := read(path)
		if err != nil {
			return nil, err
		}
		if data != nil {
			fields, err := settingsFields(data, false)
			if err != nil {
				return nil, err
			}
			sources = append(sources, claudeSettingsSource{path: path, provenance: "claude:user", fields: fields})
		}
	}
	// Resolve null removals independently within each tier before applying
	// managed values. Removing a managed option restores the user's value.
	for _, managed := range []bool{false, true} {
		tier := map[string]any{}
		for _, source := range sources {
			if source.managed != managed || !managed && source.provenance != "claude:user" || source.fields.pluginConfigs == nil {
				continue
			}
			node := source.fields.pluginConfigs
			if node.Tag == "!!null" {
				clear(tier)
				continue
			}
			if node.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("pluginConfigs must be an object")
			}
			indices := make([]int, 0, len(node.Content)/2)
			for i := 0; i < len(node.Content); i += 2 {
				indices = append(indices, i)
			}
			sort.SliceStable(indices, func(i, j int) bool {
				left, right := node.Content[indices[i]].Value, node.Content[indices[j]].Value
				leftDepth, rightDepth := m.claudePluginAliases.Depth(left), m.claudePluginAliases.Depth(right)
				if leftDepth != rightDepth {
					return leftDepth > rightDepth
				}
				if left == optionID {
					return false
				}
				if right == optionID {
					return true
				}
				return left < right
			})
			for _, i := range indices {
				resolved, _, _ := m.claudePluginAliases.Resolve(node.Content[i].Value)
				if node.Content[i].Value == id && resolved == "" {
					resolved = id
				}
				if resolved != optionID {
					continue
				}
				config := node.Content[i+1]
				if config.Tag == "!!null" {
					clear(tier)
					continue
				}
				if config.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("pluginConfigs entry must be an object")
				}
				for k := 0; k < len(config.Content); k += 2 {
					if config.Content[k].Value != "options" {
						continue
					}
					values := config.Content[k+1]
					if values.Tag == "!!null" {
						clear(tier)
						continue
					}
					if values.Kind != yaml.MappingNode {
						return nil, fmt.Errorf("plugin options must be an object")
					}
					var decoded map[string]any
					if err := values.Decode(&decoded); err != nil {
						return nil, fmt.Errorf("invalid plugin option values")
					}
					for key, value := range decoded {
						if value == nil {
							delete(tier, key)
						} else {
							tier[key] = value
						}
					}
				}
			}
		}
		for key, value := range tier {
			options[key] = value
		}
	}

	result := map[string]string{}
	secrets := map[string]any{}
	for _, field := range schema {
		if !field.Sensitive {
			continue
		}
		var err error
		if m.claudeSecretOptions != nil {
			secrets, err = m.claudeSecretOptions(optionID)
		} else {
			var fields map[string]json.RawMessage
			data, readErr := read(pluginsecrets.Path(m.claudeUserDir))
			if readErr != nil {
				return nil, readErr
			}
			if data != nil && pluginJSON(data, &fields) != nil {
				return nil, fmt.Errorf("invalid encrypted plugin option store")
			}
			secrets, err = pluginsecrets.Read(m.claudeUserDir, optionID, fields, nil)
		}
		if err != nil {
			return nil, err
		}
		break
	}
	envKeys := map[string]bool{}
	total := 0
	for key, field := range schema {
		if !pluginOptionPattern.MatchString(key) || envKeys[strings.ToUpper(key)] {
			return nil, fmt.Errorf("invalid or colliding userConfig option name")
		}
		envKeys[strings.ToUpper(key)] = true
		value, present := options[key]
		if field.Sensitive {
			if present {
				return nil, fmt.Errorf("sensitive plugin option %s must use secure storage; configure it from stdin", key)
			}
			value, present = secrets[key]
		}
		if !present {
			value = field.Default
		}
		if value == nil {
			if field.Required {
				return nil, fmt.Errorf("required plugin option %s is not configured", key)
			}
			continue
		}
		text, err := pluginOptionText(value, field)
		if err != nil {
			return nil, fmt.Errorf("invalid plugin option %s: %w", key, err)
		}
		if len(text) > 8192 || strings.ContainsRune(text, 0) {
			return nil, fmt.Errorf("plugin option exceeds safe process limits")
		}
		total += len(text)
		if total > 64<<10 {
			return nil, fmt.Errorf("plugin options exceed 64 KiB")
		}
		result[key] = text
	}
	return result, nil
}

func pluginOptionText(value any, field claudePluginOption) (string, error) {
	switch field.Type {
	case "string", "file", "directory":
		if field.Multiple {
			data, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			var values []string
			if err := json.Unmarshal(data, &values); err != nil {
				return "", fmt.Errorf("expected string array")
			}
			return string(data), nil
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("expected string")
		}
		if field.Required && text == "" {
			return "", fmt.Errorf("value is required")
		}
		if len(field.Options) > 0 && !slices.Contains(field.Options, text) {
			return "", fmt.Errorf("value is outside declared options")
		}
		return text, nil
	case "boolean":
		flag, ok := value.(bool)
		if !ok {
			if text, yes := value.(string); yes {
				var err error
				flag, err = strconv.ParseBool(text)
				ok = err == nil
			}
		}
		if !ok {
			return "", fmt.Errorf("expected boolean")
		}
		return strconv.FormatBool(flag), nil
	case "number":
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		text := strings.Trim(string(data), "\"")
		number, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return "", fmt.Errorf("expected finite number")
		}
		if field.Min != nil && number < *field.Min || field.Max != nil && number > *field.Max {
			return "", fmt.Errorf("number is outside declared bounds")
		}
		return strconv.FormatFloat(number, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported userConfig type")
	}
}

func pluginHookVariables(value string, info RuntimeInfo, plugin *claudePluginRuntime) string {
	value = claudePathVariables(value, info)
	if plugin == nil {
		return value
	}
	value = strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", filepath.ToSlash(plugin.Root), "${CLAUDE_PLUGIN_DATA}", filepath.ToSlash(plugin.Data)).Replace(value)
	return pluginOptionReference.ReplaceAllStringFunc(value, func(match string) string { return plugin.Options[pluginOptionReference.FindStringSubmatch(match)[1]] })
}
