package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gokin/internal/fileutil"
	"gokin/internal/hooks"
	"gokin/internal/pluginpolicy"
	"gokin/internal/pluginsecrets"
)

func (m *Manager) stagedManager() (*Manager, error) {
	staged, err := NewManager(m.opts)
	if err == nil {
		staged.transaction = map[string]map[string]json.RawMessage{}
	}
	return staged, err
}

func (m *Manager) commitStaged(staged *Manager) error {
	if err := m.verifySnapshot(); err != nil {
		return err
	}
	documents := map[string]any{}
	for path, fields := range staged.transaction {
		documents[path] = fields
	}
	return m.save(documents)
}

func (m *Manager) effectiveDependencyNodes(scope string) (map[string]installedDependencyNode, error) {
	result := map[string]installedDependencyNode{}
	policy, err := m.policy()
	if err != nil {
		return nil, err
	}
	for _, tier := range []string{"user", "project", "local"} {
		nodes, err := m.dependencyNodes(tier)
		if err != nil {
			return nil, err
		}
		for id, node := range nodes {
			result[id] = node
		}
		_, fields, err := m.settings(tier)
		if err != nil {
			return nil, err
		}
		enabled, err := enabledEntries(fields)
		if err != nil {
			return nil, err
		}
		for id, on := range enabled {
			if node, exists := result[id]; exists {
				node.Enabled = on
				result[id] = node
			}
		}
		if tier == scope {
			break
		}
	}
	for id, on := range policy.Enabled {
		if node, exists := result[id]; exists {
			node.Enabled = on
			result[id] = node
		}
	}
	return result, nil
}

func (m *Manager) enableGraph(ctx context.Context, id, scope string) (Result, error) {
	staged, err := m.stagedManager()
	if err != nil {
		return Result{}, err
	}
	nodes, err := staged.effectiveDependencyNodes(scope)
	if err != nil {
		return Result{}, err
	}
	known, err := staged.marketplaces()
	if err != nil {
		return Result{}, err
	}
	policy, err := staged.policy()
	if err != nil {
		return Result{}, err
	}
	_, user, err := staged.settings("user")
	if err != nil {
		return Result{}, err
	}
	state := map[string]int{}
	changed := false
	var enable func(string, int) error
	enable = func(target string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if state[target] == 1 || depth > 64 {
			return fmt.Errorf("circular plugin dependencies at %s", target)
		}
		if state[target] == 2 {
			return nil
		}
		node, exists := nodes[target]
		if !exists {
			return fmt.Errorf("dependency %s is not installed; install it before enabling %s", target, id)
		}
		if on, set := policy.Enabled[target]; set && !on {
			return fmt.Errorf("dependency %s is disabled by managed policy", target)
		}
		marketName := strings.SplitN(target, "@", 2)[1]
		if required, set := policy.Required[marketName]; set && policy.Enabled[target] {
			required = pluginpolicy.ResolveLocalSource(required, m.opts.WorkDir)
			if node.Record.MarketplaceSource == nil || !pluginpolicy.EqualSource(*node.Record.MarketplaceSource, required) {
				return fmt.Errorf("plugin %s requires reinstallation from its managed marketplace", target)
			}
		}
		var market Marketplace
		json.Unmarshal(known[marketName], &market)
		if market.Source.Kind != "" {
			if err := policy.Check(market.Source); err != nil {
				return err
			}
		} else if policy.RestrictsMarketplaces() {
			return fmt.Errorf("plugin marketplace provenance is unavailable under managed policy")
		}
		if node.Record.MarketplaceSource != nil {
			if err := policy.Check(*node.Record.MarketplaceSource); err != nil {
				return err
			}
		}
		if policy.DisabledCommands && node.Record.Source.Kind == "command" {
			return fmt.Errorf("command plugin sources are disabled by managed policy")
		}
		state[target] = 1
		for _, dep := range node.Dependencies {
			child := dep.ID(target)
			if installed, exists := nodes[child]; exists && !satisfiesRanges(installed.Version, []string{dep.Version}) {
				return fmt.Errorf("requires %s %s, installed %s", child, dep.Version, installed.Version)
			}
			if err := enable(child, depth+1); err != nil {
				return err
			}
		}
		optionRoot, cleanup, err := staged.installedOptionRoot(node.Record)
		if err != nil {
			return err
		}
		_, validationErr := staged.validateInstall(optionRoot, target, user, node.Record)
		cleanup()
		if validationErr != nil {
			return validationErr
		}
		if err := staged.enableDependency(target, scope); err != nil {
			return err
		}
		changed = changed || !node.Enabled
		state[target] = 2
		return nil
	}
	if err := enable(id, 0); err != nil {
		return Result{}, err
	}
	if err := m.commitStaged(staged); err != nil {
		return Result{}, err
	}
	return Result{Command: "enable", Outcome: "ok", PluginID: id, Scope: scope, Changed: changed, Message: "Enabled " + id + " and its installed dependencies at " + scope + " scope."}, nil
}

func (m *Manager) stageRemove(id, scope string) error {
	doc, installed, err := m.installs()
	if err != nil {
		return err
	}
	var keep []json.RawMessage
	for _, raw := range installed[id] {
		var record InstallRecord
		if json.Unmarshal(raw, &record) != nil {
			return fmt.Errorf("invalid plugin record")
		}
		if !m.scopeMatches(record, scope) {
			keep = append(keep, raw)
		}
	}
	if len(keep) == 0 {
		delete(installed, id)
	} else {
		installed[id] = keep
	}
	doc["plugins"], _ = json.Marshal(installed)
	path, settings, err := m.settings(scope)
	if err != nil {
		return err
	}
	enabled, err := enabledEntries(settings)
	if err != nil {
		return err
	}
	delete(enabled, id)
	settings["enabledPlugins"], _ = json.Marshal(enabled)
	documents := map[string]any{filepath.Join(m.pluginDir, "installed_plugins.json"): doc, path: settings}
	if len(keep) == 0 {
		userPath, user, err := m.settings("user")
		if err != nil {
			return err
		}
		if scope == "user" {
			user = settings
		}
		if raw, exists := user["pluginConfigs"]; exists {
			var configs map[string]json.RawMessage
			if json.Unmarshal(raw, &configs) != nil {
				return fmt.Errorf("pluginConfigs must be an object")
			}
			delete(configs, id)
			user["pluginConfigs"], _ = json.Marshal(configs)
		}
		documents[userPath] = user
		secretPath := pluginsecrets.Path(m.opts.UserDir)
		fields, err := m.readObject(secretPath)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			fields, err = pluginsecrets.Remove(id, fields)
			if err != nil {
				return err
			}
			documents[secretPath] = fields
		}
	}
	return m.save(documents)
}

func (m *Manager) guardDependencyChange(id, scope string, remove bool) error {
	staged, err := m.stagedManager()
	if err != nil {
		return err
	}
	if remove {
		if err := staged.stageRemove(id, scope); err != nil {
			return err
		}
	} else {
		path, fields, err := staged.settings(scope)
		if err != nil {
			return err
		}
		enabled, err := enabledEntries(fields)
		if err != nil {
			return err
		}
		enabled[id] = false
		fields["enabledPlugins"], _ = json.Marshal(enabled)
		if err := staged.save(map[string]any{path: fields}); err != nil {
			return err
		}
	}
	for _, view := range []string{"user", "project", "local"} {
		if !m.opts.Trusted && view != "user" && view != scope {
			continue
		}
		before, err := m.effectiveDependencyNodes(view)
		if err != nil {
			return err
		}
		after, err := staged.effectiveDependencyNodes(view)
		if err != nil {
			return err
		}
		oldIssues, newIssues := dependencyIssues(before), dependencyIssues(after)
		var parents []string
		for parent, node := range before {
			if parent != id && node.Enabled && oldIssues[parent] == "" && after[parent].Enabled && newIssues[parent] != "" {
				parents = append(parents, parent)
			}
		}
		sort.Strings(parents)
		if len(parents) > 0 {
			return fmt.Errorf("plugin %s is required by enabled plugin %s; disable it first: gokin plugin disable %s --scope %s", id, parents[0], parents[0], view)
		}
	}
	return nil
}

type PruneOptions struct {
	Scope    string
	DryRun   bool
	KeepData bool
}

func (m *Manager) Prune(ctx context.Context, opts PruneOptions) (Result, error) {
	if opts.Scope == "" {
		opts.Scope = "user"
	}
	if _, err := m.settingsPath(opts.Scope); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !opts.DryRun {
		unlock, err := m.lock(ctx)
		if err != nil {
			return Result{}, err
		}
		defer unlock()
	}
	nodes, err := m.dependencyNodes(opts.Scope)
	if err != nil {
		return Result{}, err
	}
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	needed := map[string]bool{}
	var markScope func(string)
	markScope = func(id string) {
		if needed[id] {
			return
		}
		needed[id] = true
		for _, dep := range nodes[id].Dependencies {
			markScope(dep.ID(id))
		}
	}
	for id, node := range nodes {
		if !node.Record.AutoInstalled || policy.Enabled[id] {
			markScope(id)
		}
	}
	// Removing a redundant scope copy is allowed when the remaining copy still
	// satisfies consumers. Keep copies whose removal would break a live manual
	// or managed root in any affected workspace view.
	for pass := 0; pass < len(nodes); pass++ {
		preview, err := m.stagedManager()
		if err != nil {
			return Result{}, err
		}
		for id, node := range nodes {
			if node.Record.AutoInstalled && !needed[id] {
				if err := preview.stageRemove(id, opts.Scope); err != nil {
					return Result{}, err
				}
			}
		}
		changed := false
		for _, view := range []string{"user", "project", "local"} {
			if !m.opts.Trusted && view != "user" && view != opts.Scope {
				continue
			}
			before, err := m.effectiveDependencyNodes(view)
			if err != nil {
				return Result{}, err
			}
			after, err := preview.effectiveDependencyNodes(view)
			if err != nil {
				return Result{}, err
			}
			oldIssues, newIssues := dependencyIssues(before), dependencyIssues(after)
			seen := map[string]bool{}
			var protect func(string)
			protect = func(id string) {
				if seen[id] {
					return
				}
				seen[id] = true
				if _, candidate := nodes[id]; candidate && !needed[id] {
					needed[id] = true
					changed = true
				}
				for _, dep := range before[id].Dependencies {
					protect(dep.ID(id))
				}
			}
			for id, node := range before {
				if node.Enabled && (!node.Record.AutoInstalled || policy.Enabled[id]) && oldIssues[id] == "" && after[id].Enabled && newIssues[id] != "" {
					for _, dep := range node.Dependencies {
						protect(dep.ID(id))
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	var pruned []string
	for id, node := range nodes {
		if node.Record.AutoInstalled && !needed[id] {
			pruned = append(pruned, id)
		}
	}
	sort.Strings(pruned)
	result := Result{Command: "prune", Outcome: "ok", Scope: opts.Scope, Pruned: pruned, DryRun: opts.DryRun, Message: "Nothing to prune."}
	if len(pruned) == 0 {
		return result, nil
	}
	result.Message = "Auto-installed plugins no longer needed: " + strings.Join(pruned, ", ") + "."
	if opts.DryRun {
		return result, nil
	}
	staged, err := m.stagedManager()
	if err != nil {
		return Result{}, err
	}
	for _, id := range pruned {
		if err := staged.stageRemove(id, opts.Scope); err != nil {
			return Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := m.commitStaged(staged); err != nil {
		return Result{}, err
	}
	result.Changed = true
	for _, id := range pruned {
		_, remaining, err := m.installs()
		if err != nil {
			return result, err
		}
		if len(remaining[id]) == 0 && !opts.KeepData {
			dataRoot := filepath.Join(m.pluginDir, "data")
			if _, err := os.Lstat(dataRoot); err == nil {
				if err := fileutil.EnsurePrivateDir(dataRoot); err != nil {
					result.Warnings = append(result.Warnings, "Plugin data retained because its directory is unsafe")
				} else if err := os.RemoveAll(filepath.Join(dataRoot, hooks.ClaudePluginStorageKey(id))); err != nil {
					result.Warnings = append(result.Warnings, "Plugin data cleanup failed")
				}
			}
		}
	}
	return result, nil
}
