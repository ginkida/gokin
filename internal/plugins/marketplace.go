package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/pluginpolicy"
)

func marketplaceNameAllowed(name string, source Source) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid marketplace name")
	}
	low := strings.ToLower(name)
	for _, reserved := range []string{"inline", "builtin", "skills-dir", "synced", "claude-plugin-test", "npm", "pip", "uv", "cargo", "github", "gh", "constructor", "hasownproperty", "isprototypeof", "propertyisenumerable", "tolocalestring", "tostring", "valueof"} {
		if low == reserved {
			return fmt.Errorf("marketplace name %q is reserved", name)
		}
	}
	if strings.HasPrefix(low, "claudeai-") {
		return fmt.Errorf("claudeai- marketplace names are reserved")
	}
	official := strings.HasPrefix(strings.ToLower(pluginpolicy.GitHubRepo(source)), "anthropics/")
	if (strings.Contains(low, "claude") || strings.Contains(low, "anthropic")) && !official {
		return fmt.Errorf("marketplace name %q is reserved for its official GitHub owner", name)
	}
	for _, reserved := range []string{"agent-skills", "anthropic-agent-skills", "life-sciences", "knowledge-work-plugins", "healthcare", "financial-services-plugins", "first-party-plugins"} {
		if strings.ReplaceAll(strings.TrimSuffix(low, "."), ".", "-") == reserved && !official {
			return fmt.Errorf("reserved marketplace name")
		}
	}
	return nil
}

func (m *Manager) readCatalog(name string, marketplace Marketplace) (Catalog, error) {
	policy, err := m.policy()
	if err != nil {
		return Catalog{}, err
	}
	if err := policy.Check(marketplace.Source); err != nil {
		return Catalog{}, err
	}
	if err := marketplaceNameAllowed(name, marketplace.Source); err != nil {
		return Catalog{}, err
	}
	var path string
	switch marketplace.Source.Kind {
	case "directory":
		path = filepath.Join(marketplace.Source.Path, ".claude-plugin", "marketplace.json")
	case "file":
		path = marketplace.Source.Path
	case "settings":
		data, _ := json.Marshal(map[string]any{"name": marketplace.Source.Name, "owner": json.RawMessage(marketplace.Source.Owner), "plugins": json.RawMessage(marketplace.Source.Plugins)})
		return ParseCatalog(data)
	case "github", "git":
		relative := marketplace.Source.Path
		if relative == "" {
			relative = ".claude-plugin/marketplace.json"
		}
		relative, err = safeRelative(relative, false)
		if err != nil {
			return Catalog{}, err
		}
		path = filepath.Join(marketplace.InstallLocation, relative)
	case "url":
		path = filepath.Join(marketplace.InstallLocation, "marketplace.json")
	default:
		return Catalog{}, fmt.Errorf("unsupported marketplace source")
	}
	data, err := fileutil.ReadResolvedRegularFile(path, 4<<20)
	if err != nil {
		return Catalog{}, err
	}
	catalog, err := ParseCatalog(data)
	if err != nil {
		return catalog, err
	}
	if catalog.Name != name {
		return catalog, fmt.Errorf("marketplace name changed from %q to %q", name, catalog.Name)
	}
	return catalog, nil
}
func (m *Manager) fetchMarketplace(ctx context.Context, source Source, stage string, accept []string) (Catalog, string, error) {
	var root, path string
	switch source.Kind {
	case "directory":
		root = source.Path
		path = filepath.Join(root, ".claude-plugin", "marketplace.json")
	case "file":
		root = filepath.Dir(filepath.Dir(source.Path))
		path = source.Path
	case "github", "git":
		var err error
		root, _, err = fetchGit(ctx, source, stage)
		if err != nil {
			return Catalog{}, "", err
		}
		relative := source.Path
		if relative == "" {
			relative = ".claude-plugin/marketplace.json"
		}
		path = filepath.Join(root, filepath.FromSlash(relative))
	case "url":
		root = filepath.Join(stage, "json-catalog")
		os.Mkdir(root, 0700)
		path = filepath.Join(root, "marketplace.json")
		headers, err := headersFor(ctx, source, Entry{}, accept)
		if err != nil {
			return Catalog{}, "", err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return Catalog{}, "", err
		}
		err = download(ctx, source.URL, headers, false, file, 4<<20)
		file.Close()
		if err != nil {
			return Catalog{}, "", err
		}
	case "settings":
		data, _ := json.Marshal(map[string]any{"name": source.Name, "owner": json.RawMessage(source.Owner), "plugins": json.RawMessage(source.Plugins)})
		catalog, err := ParseCatalog(data)
		if err == nil {
			err = marketplaceNameAllowed(catalog.Name, source)
		}
		return catalog, "", err
	default:
		return Catalog{}, "", fmt.Errorf("unsupported marketplace source")
	}
	data, err := fileutil.ReadResolvedRegularFile(path, 4<<20)
	if err != nil {
		return Catalog{}, "", err
	}
	catalog, err := ParseCatalog(data)
	if err != nil {
		return Catalog{}, "", err
	}
	if err := marketplaceNameAllowed(catalog.Name, source); err != nil {
		return Catalog{}, "", err
	}
	return catalog, root, nil
}
func (m *Manager) publishTree(source, kind, name string) (string, string, error) {
	bucket := filepath.Join(m.cacheDir, kind)
	if err := fileutil.EnsurePrivateDir(bucket); err != nil {
		return "", "", err
	}
	parent := filepath.Join(bucket, name)
	if err := fileutil.EnsurePrivateDir(parent); err != nil {
		return "", "", err
	}
	stage, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(stage)
	digest, err := copyTree(source, stage)
	if err != nil {
		return "", "", err
	}
	target := filepath.Join(parent, digest)
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("plugin cache version is not a real directory")
		}
		// A content-addressed path is reused only if its current contents still
		// match. Corruption never silently revives a cached executable tree.
		verify, err := os.MkdirTemp(parent, ".verify-")
		if err != nil {
			return "", "", err
		}
		defer os.RemoveAll(verify)
		actual, err := copyTree(target, verify)
		if err != nil || actual != digest {
			return "", "", fmt.Errorf("plugin cache integrity mismatch")
		}
		return target, digest, nil
	}
	if err := os.Rename(stage, target); err != nil {
		return "", "", err
	}
	return target, digest, nil
}

func (m *Manager) AddMarketplace(ctx context.Context, input string, accept []string) (Result, error) {
	source, err := ParseMarketplaceSource(input, m.opts.WorkDir)
	if err != nil {
		return Result{}, err
	}
	result, err := m.addMarketplace(ctx, source, accept, false, "", true, false)
	if err != nil {
		return result, err
	}
	report, repairErr := m.RepairDependencies(ctx)
	result.Warnings = append(result.Warnings, report.Errors...)
	result.Warnings = append(result.Warnings, report.Skipped...)
	if repairErr != nil {
		result.Warnings = append(result.Warnings, repairErr.Error())
	}
	if len(report.Installed) > 0 {
		result.Message += " Repaired dependencies: " + strings.Join(report.Installed, ", ") + "."
	}
	return result, nil
}
func (m *Manager) addMarketplace(ctx context.Context, source Source, accept []string, update bool, expected string, declare, replace bool) (Result, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	if err := policy.Check(source); err != nil {
		return Result{}, err
	}
	stage, err := os.MkdirTemp(filepath.Join(m.pluginDir, "gokin"), "fetch-marketplace-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)
	catalog, root, err := m.fetchMarketplace(ctx, source, stage, accept)
	if err != nil {
		return Result{}, err
	}
	if expected != "" && catalog.Name != expected {
		return Result{}, fmt.Errorf("updated marketplace changed its name")
	}
	known, err := m.marketplaces()
	if err != nil {
		return Result{}, err
	}
	var old Marketplace
	if raw := known[catalog.Name]; len(raw) > 0 {
		if json.Unmarshal(raw, &old) != nil {
			return Result{}, fmt.Errorf("invalid marketplace record")
		}
		if !replace && !sameSource(old.Source, source) {
			return Result{}, fmt.Errorf("marketplace name already refers to another source")
		}
	} else if update {
		return Result{}, fmt.Errorf("marketplace is not registered")
	}
	location := root
	if source.Kind == "github" || source.Kind == "git" || source.Kind == "url" {
		location, _, err = m.publishTree(root, "marketplaces", catalog.Name)
		if err != nil {
			return Result{}, err
		}
	}
	if fresh, err := m.policy(); err != nil {
		return Result{}, err
	} else if err := fresh.Check(source); err != nil {
		return Result{}, err
	}
	if replace && !declare {
		fresh, err := m.startupPlan(ctx)
		if err != nil {
			return Result{}, err
		}
		entry, exists := fresh.Markets[catalog.Name]
		current, err := normalizeMarketplaceSource(entry.Source, entry.Base)
		if !exists || err != nil || !sameSource(current, source) {
			return Result{}, fmt.Errorf("marketplace declaration changed during registration; retry")
		}
		if err := m.verifySnapshot(); err != nil {
			return Result{}, err
		}
	}
	entry := Marketplace{Source: source, InstallLocation: location, LastUpdated: time.Now().UTC().Format(time.RFC3339)}
	nextFields := map[string]json.RawMessage{}
	if previous, exists := known[catalog.Name]; exists {
		if json.Unmarshal(previous, &nextFields) != nil {
			return Result{}, fmt.Errorf("invalid existing marketplace metadata")
		}
	}
	freshFields, _ := json.Marshal(entry)
	var updates map[string]json.RawMessage
	json.Unmarshal(freshFields, &updates)
	for key, value := range updates {
		nextFields[key] = value
	}
	raw, _ := json.Marshal(nextFields)
	known[catalog.Name] = raw
	if len(known) > 64 {
		return Result{}, fmt.Errorf("marketplace registry exceeds 64 entries")
	}
	documents := map[string]any{filepath.Join(m.pluginDir, "known_marketplaces.json"): known}
	if declare {
		userPath, userSettings, err := m.settings("user")
		if err != nil {
			return Result{}, err
		}
		declarations := map[string]json.RawMessage{}
		if raw, exists := userSettings["extraKnownMarketplaces"]; exists {
			if json.Unmarshal(raw, &declarations) != nil {
				return Result{}, fmt.Errorf("extraKnownMarketplaces must be an object")
			}
		}
		declaration := map[string]json.RawMessage{}
		if raw, exists := declarations[catalog.Name]; exists {
			if json.Unmarshal(raw, &declaration) != nil {
				return Result{}, fmt.Errorf("invalid marketplace declaration")
			}
		}
		declaration["source"], _ = json.Marshal(source)
		declarations[catalog.Name], _ = json.Marshal(declaration)
		userSettings["extraKnownMarketplaces"], _ = json.Marshal(declarations)
		documents[userPath] = userSettings
	}
	staged, err := m.stagedManager()
	if err != nil {
		return Result{}, err
	}
	if err := staged.save(documents); err != nil {
		return Result{}, err
	}
	renamed, err := staged.stageCatalogRenames(ctx, catalog)
	if err != nil {
		return Result{}, err
	}
	removed, err := staged.stageCatalogRemovals(ctx, catalog)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := m.commitStaged(staged); err != nil {
		return Result{}, err
	}
	command := "marketplace-add"
	warnings := append(m.cleanupRemovedData(removed), renamed...)
	if update {
		command = "marketplace-update"
	}
	return Result{Command: command, Outcome: "ok", Marketplace: catalog.Name, Changed: true, Message: "Marketplace " + catalog.Name + " is ready.", Warnings: warnings, Pruned: removed}, nil
}
func (m *Manager) UpdateMarketplace(ctx context.Context, name string, accept []string) (Result, error) {
	known, err := m.marketplaces()
	if err != nil {
		return Result{}, err
	}
	var entry Marketplace
	if json.Unmarshal(known[name], &entry) != nil {
		return Result{}, fmt.Errorf("marketplace %q is not registered", name)
	}
	source, err := normalizeMarketplaceSource(entry.Source, m.opts.WorkDir)
	if err != nil {
		return Result{}, err
	}
	return m.addMarketplace(ctx, source, accept, true, name, false, false)
}
func (m *Manager) RemoveMarketplace(ctx context.Context, name string) (Result, error) {
	if !ValidName(name) {
		return Result{}, fmt.Errorf("invalid marketplace name")
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	if _, required := policy.Required[name]; required {
		return Result{}, fmt.Errorf("marketplace is required by managed policy")
	}
	known, err := m.marketplaces()
	if err != nil {
		return Result{}, err
	}
	if _, ok := known[name]; !ok {
		return Result{}, fmt.Errorf("marketplace is not registered")
	}
	_, installs, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	for id, records := range installs {
		if strings.HasSuffix(id, "@"+name) && len(records) > 0 {
			return Result{}, fmt.Errorf("uninstall %s before removing its marketplace", id)
		}
	}
	delete(known, name)
	userPath, userSettings, err := m.settings("user")
	if err != nil {
		return Result{}, err
	}
	if raw, exists := userSettings["extraKnownMarketplaces"]; exists {
		var declarations map[string]json.RawMessage
		if json.Unmarshal(raw, &declarations) != nil {
			return Result{}, fmt.Errorf("extraKnownMarketplaces must be an object")
		}
		delete(declarations, name)
		userSettings["extraKnownMarketplaces"], _ = json.Marshal(declarations)
	}
	documents := map[string]any{filepath.Join(m.pluginDir, "known_marketplaces.json"): known, userPath: userSettings}
	doc, _, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	aliases, err := pluginpolicy.ReadPluginAliases(doc)
	if err != nil {
		return Result{}, err
	}
	changedAliases := false
	for id := range aliases {
		if strings.HasSuffix(id, "@"+name) {
			delete(aliases, id)
			changedAliases = true
		}
	}
	if changedAliases {
		if len(aliases) == 0 {
			delete(doc, pluginpolicy.PluginAliasesField)
		} else {
			doc[pluginpolicy.PluginAliasesField], _ = json.Marshal(aliases)
		}
		documents[filepath.Join(m.pluginDir, "installed_plugins.json")] = doc
	}
	if err := m.save(documents); err != nil {
		return Result{}, err
	}
	return Result{Command: "marketplace-remove", Outcome: "ok", Marketplace: name, Changed: true, Message: "Removed marketplace " + name + "."}, nil
}
func (m *Manager) MarketplaceList() (map[string]Marketplace, error) {
	known, err := m.marketplaces()
	if err != nil {
		return nil, err
	}
	out := map[string]Marketplace{}
	for name, raw := range known {
		var entry Marketplace
		if json.Unmarshal(raw, &entry) != nil {
			return nil, fmt.Errorf("invalid marketplace registry")
		}
		entry.Source.Headers = nil
		entry.Source.HeadersHelper = ""
		out[name] = entry
	}
	return out, nil
}
