package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gokin/internal/agent"
	"gokin/internal/fileutil"
	"gokin/internal/hooks"
	"gokin/internal/lsp"
	"gokin/internal/mcp"
	"gokin/internal/pluginpolicy"
	"gokin/internal/pluginsecrets"
	"gokin/internal/skills"
	"strings"
)

// effectiveManifest implements marketplace component merge rules before a
// copied install is published. Original marketplace/plugin files are untouched.
func effectiveManifest(root string, entry Entry) error {
	path := filepath.Join(root, ".claude-plugin", "plugin.json")
	manifest, err := readObject(path)
	if err != nil {
		return err
	}
	manifest, err = pluginpolicy.MergePluginManifest(manifest, entry.Raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, append(data, '\n'), 0600)
}

func (m *Manager) validateInstall(root, id string, settings map[string]json.RawMessage, runtimeRecord ...InstallRecord) (Validation, error) {
	var report Validation
	manifest, err := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return report, err
	}
	if json.Unmarshal(manifest["name"], &report.Name) != nil || !hooks.ValidClaudePluginName(report.Name) {
		return report, fmt.Errorf("plugin manifest requires a valid name")
	}
	_ = json.Unmarshal(manifest["version"], &report.Version)
	stage, err := os.MkdirTemp(filepath.Join(m.pluginDir, "gokin"), "validate-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(stage)
	user := filepath.Join(stage, "user")
	if err := os.MkdirAll(filepath.Join(user, "plugins"), 0700); err != nil {
		return report, err
	}
	record := InstallRecord{Scope: "user", InstallPath: root}
	if len(runtimeRecord) > 0 && runtimeRecord[0].Source.Kind == "command" && runtimeRecord[0].Source.Mode == "link" {
		record = runtimeRecord[0]
		record.Scope, record.ProjectPath = "user", ""
	}
	registry := map[string]any{"version": 2, "plugins": map[string]any{id: []InstallRecord{record}}}
	aliases, err := m.pluginAliases()
	if err != nil {
		return report, err
	}
	if len(aliases) > 0 {
		registry[pluginpolicy.PluginAliasesField] = aliases
	}
	fakeRegistry, _ := json.Marshal(registry)
	if err := os.WriteFile(filepath.Join(user, "plugins", "installed_plugins.json"), fakeRegistry, 0600); err != nil {
		return report, err
	}
	copySettings := map[string]json.RawMessage{}
	if raw, ok := settings["pluginConfigs"]; ok {
		copySettings["pluginConfigs"] = raw
	}
	copySettings["enabledPlugins"], _ = json.Marshal(map[string]bool{id: true})
	fakeSettings, _ := json.Marshal(copySettings)
	if err := os.WriteFile(filepath.Join(user, "settings.json"), fakeSettings, 0600); err != nil {
		return report, err
	}
	// Carry only the option layer into isolated validation. Marketplace policy
	// is checked by the real transaction; unrelated managed hooks never run.
	managed := filepath.Join(stage, "managed")
	documents, err := pluginpolicy.ReadManagedDocuments(m.opts.ManagedDir)
	if err != nil {
		return report, err
	}
	for i, data := range documents {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return report, err
		}
		options, exists := fields["pluginConfigs"]
		if !exists {
			continue
		}
		path := filepath.Join(managed, "managed-settings.d", fmt.Sprintf("%03d.json", i))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return report, err
		}
		isolated, _ := json.Marshal(map[string]json.RawMessage{"pluginConfigs": options})
		if err := os.WriteFile(path, isolated, 0600); err != nil {
			return report, err
		}
	}
	authority := hooks.NewManager(true, m.opts.WorkDir)
	authority.ConfigureClaudeDependencyValidation()
	authority.ConfigureClaudeSecretOptions(func(pluginID string) (map[string]any, error) {
		fields, err := m.readObject(pluginsecrets.Path(m.opts.UserDir))
		if err != nil {
			return nil, err
		}
		return pluginsecrets.Read(m.opts.UserDir, pluginID, fields, m.opts.SecretKeyring)
	})
	authority.ConfigureClaudeManagedSettings(managed)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, user, false)
	authority.ConfigureClaudePlugins(nil)
	selected, err := authority.ClaudePlugins()
	if err != nil {
		return report, err
	}
	if len(selected) != 1 {
		return report, fmt.Errorf("plugin validation did not select the installed source")
	}
	servers, err := mcp.LoadClaudePluginServers(authority, m.opts.WorkDir, true)
	if err != nil {
		return report, err
	}
	languages, err := lsp.LoadPluginConfigs(authority, m.opts.WorkDir)
	if err != nil {
		return report, err
	}
	catalog := skills.NewCatalog(nil)
	catalog.ConfigureClaudePlugins(authority)
	workflows := catalog.List()
	types := agent.NewAgentTypeRegistry()
	types.ConfigureClaudePlugins(authority)
	definitions := types.SnapshotAgentTypes()
	report.Components = map[string]int{"workflows": len(workflows), "mcpServers": len(servers), "lspServers": len(languages), "agents": 0, "hooks": len(authority.GetHooks())}
	for _, definition := range definitions {
		if strings.HasPrefix(definition.Name, report.Name+":") {
			report.Components["agents"]++
		}
	}
	report.Warnings = append(catalog.Warnings(), types.PluginWarnings()...)
	for _, field := range []string{"outputStyles", "themes", "workflows", "channels", "experimental"} {
		if raw, exists := manifest[field]; exists && string(raw) != "null" {
			report.Warnings = append(report.Warnings, "Runtime support is pending for plugin field "+field)
		}
	}
	sort.Strings(report.Warnings)
	return report, nil
}

// Validate checks a plugin/marketplace without running hooks, servers,
// installers, package managers or network requests.
func (m *Manager) Validate(path string) (Validation, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.opts.WorkDir, path)
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		marketplace := filepath.Join(path, ".claude-plugin", "marketplace.json")
		if data, err := fileutil.ReadResolvedRegularFile(marketplace, 4<<20); err == nil {
			catalog, err := ParseCatalog(data)
			return Validation{Name: catalog.Name, Components: map[string]int{"plugins": len(catalog.Plugins)}}, err
		}
	}
	return m.validatePlugin(path)
}

// validatePlugin also handles a plugin colocated with its marketplace. Release
// validation must inspect its components rather than only the catalog.
func (m *Manager) validatePlugin(path string) (Validation, error) {
	matchedRecord := InstallRecord{InstallPath: path}
	installedID := ""
	_, installed, err := m.installs()
	if err != nil {
		return Validation{}, err
	}
	for candidate, records := range installed {
		for _, raw := range records {
			var record InstallRecord
			if json.Unmarshal(raw, &record) == nil && samePath(record.InstallPath, path) {
				installedID, matchedRecord = candidate, record
				break
			}
		}
	}
	manifest, err := m.installedManifest(matchedRecord)
	if err != nil {
		return Validation{}, err
	}
	var name string
	if json.Unmarshal(manifest["name"], &name) != nil || !hooks.ValidClaudePluginName(name) {
		return Validation{}, fmt.Errorf("plugin manifest requires a name")
	}
	// Validation's temporary state belongs to the OS temp directory, so a
	// read-only validate command never creates the user's plugin store.
	stage, err := os.MkdirTemp("", "gokin-plugin-validate-")
	if err != nil {
		return Validation{}, err
	}
	defer os.RemoveAll(stage)
	validator, err := NewManager(m.opts)
	if err != nil {
		return Validation{}, err
	}
	validator.pluginDir = stage
	if err := os.MkdirAll(filepath.Join(stage, "gokin"), 0700); err != nil {
		return Validation{}, err
	}
	// Preserve logical identities in the isolated registry so managed option
	// keys using retired names resolve exactly as they do during installation.
	aliases, err := m.pluginAliases()
	if err != nil {
		return Validation{}, err
	}
	aliasJSON, err := json.Marshal(aliases)
	if err != nil {
		return Validation{}, err
	}
	validator.transaction = map[string]map[string]json.RawMessage{
		filepath.Join(stage, "installed_plugins.json"): {
			"version": json.RawMessage("2"), "plugins": json.RawMessage("{}"),
			pluginpolicy.PluginAliasesField: aliasJSON,
		},
	}
	_, settings, err := m.settings("user")
	if err != nil {
		return Validation{}, err
	}
	id := name + "@validation"
	if installedID != "" {
		id = installedID
	}
	optionRoot, cleanup, err := validator.installedOptionRoot(matchedRecord)
	if err != nil {
		return Validation{}, err
	}
	defer cleanup()
	report, err := validator.validateInstall(optionRoot, id, settings, matchedRecord)
	if err == nil && len(manifest["dependencies"]) > 0 && string(manifest["dependencies"]) != "[]" {
		report.Warnings = append(report.Warnings, "Dependency declarations are valid; availability and versions are checked when the installed plugin graph loads.")
	}
	return report, err
}

func setOptionValues(settings map[string]json.RawMessage, id string, values map[string]string, root string) error {
	if len(values) == 0 {
		return nil
	}
	if len(values) > 64 {
		return fmt.Errorf("too many plugin option values")
	}
	configs := map[string]json.RawMessage{}
	if raw, set := settings["pluginConfigs"]; set {
		if json.Unmarshal(raw, &configs) != nil {
			return fmt.Errorf("pluginConfigs must be an object")
		}
	}
	if configs == nil {
		configs = map[string]json.RawMessage{}
	}
	entry := map[string]json.RawMessage{}
	if raw, set := configs[id]; set {
		if json.Unmarshal(raw, &entry) != nil {
			return fmt.Errorf("pluginConfigs entry must be an object")
		}
	}
	if entry == nil {
		entry = map[string]json.RawMessage{}
	}
	options := map[string]any{}
	if raw, set := entry["options"]; set {
		if json.Unmarshal(raw, &options) != nil {
			return fmt.Errorf("plugin options must be an object")
		}
	}
	if options == nil {
		options = map[string]any{}
	}
	manifest, err := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	var schema map[string]struct {
		Multiple bool `json:"multiple"`
	}
	if raw, set := manifest["userConfig"]; set && json.Unmarshal(raw, &schema) != nil {
		return fmt.Errorf("invalid userConfig schema")
	}
	for key, value := range values {
		if key == "" || len(value) > 32<<10 {
			return fmt.Errorf("invalid plugin option key/value")
		}
		if schema[key].Multiple {
			var many []string
			if json.Unmarshal([]byte(value), &many) != nil || many == nil {
				return fmt.Errorf("multiple option %s requires a JSON string array", key)
			}
			options[key] = many
		} else {
			options[key] = value
		}
	}
	entry["options"], _ = json.Marshal(options)
	configs[id], _ = json.Marshal(entry)
	settings["pluginConfigs"], _ = json.Marshal(configs)
	return nil
}

func ensureDeclaredOptions(root string, values map[string]string) error {
	manifest, err := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	var schema map[string]json.RawMessage
	if raw, set := manifest["userConfig"]; set {
		if json.Unmarshal(raw, &schema) != nil {
			return fmt.Errorf("userConfig must be an object")
		}
	}
	for key := range values {
		if _, exists := schema[key]; !exists {
			return fmt.Errorf("plugin does not declare option %q", key)
		}
	}
	return nil
}

func (m *Manager) setOptionValues(settings map[string]json.RawMessage, id string, values map[string]string, root string) error {
	if err := setOptionValues(settings, id, values, root); err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	manifest, err := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	var schema map[string]struct {
		Sensitive bool `json:"sensitive"`
	}
	if raw, set := manifest["userConfig"]; set && json.Unmarshal(raw, &schema) != nil {
		return fmt.Errorf("invalid userConfig schema")
	}
	var configs map[string]json.RawMessage
	json.Unmarshal(settings["pluginConfigs"], &configs)
	var entry map[string]json.RawMessage
	json.Unmarshal(configs[id], &entry)
	var options map[string]any
	json.Unmarshal(entry["options"], &options)
	secrets := map[string]any{}
	for key := range values {
		if schema[key].Sensitive {
			secrets[key] = options[key]
			delete(options, key)
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	if m.transaction == nil {
		return fmt.Errorf("secret options require a plugin transaction")
	}
	path := pluginsecrets.Path(m.opts.UserDir)
	fields, err := m.readObject(path)
	if err != nil {
		return err
	}
	fields, err = pluginsecrets.Update(m.opts.UserDir, id, fields, secrets, m.opts.SecretKeyring)
	if err != nil {
		return err
	}
	if err := m.save(map[string]any{path: fields}); err != nil {
		return err
	}
	entry["options"], _ = json.Marshal(options)
	configs[id], _ = json.Marshal(entry)
	settings["pluginConfigs"], _ = json.Marshal(configs)
	return nil
}

func rejectSensitiveArguments(root string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	manifest, err := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	var schema map[string]struct {
		Sensitive bool `json:"sensitive"`
	}
	if json.Unmarshal(manifest["userConfig"], &schema) != nil {
		return fmt.Errorf("invalid userConfig schema")
	}
	for _, key := range keys {
		if schema[key].Sensitive {
			return fmt.Errorf("sensitive option %s must be supplied with --values-stdin, not --config", key)
		}
	}
	return nil
}
