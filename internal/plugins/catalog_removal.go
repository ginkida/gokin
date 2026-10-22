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
)

// Removal notices contain no executable paths or configuration values. Keep a
// bounded history in the registry so a later CLI invocation can explain why a
// previously installed plugin disappeared.
type removedPlugin struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	ProjectPath string `json:"projectPath,omitempty"`
}

func removalNotices(doc map[string]json.RawMessage) ([]removedPlugin, error) {
	var notices []removedPlugin
	if raw, exists := doc["gokinRemovedPlugins"]; exists {
		if json.Unmarshal(raw, &notices) != nil || len(notices) > 256 {
			return nil, fmt.Errorf("invalid removed plugin history")
		}
		for _, notice := range notices {
			if !ValidID(notice.ID) || notice.Scope != "user" && notice.Scope != "project" && notice.Scope != "local" {
				return nil, fmt.Errorf("invalid removed plugin history")
			}
		}
	}
	return notices, nil
}

func forgetRemoval(doc map[string]json.RawMessage, id, scope, project string) error {
	notices, err := removalNotices(doc)
	if err != nil {
		return err
	}
	keep := notices[:0]
	for _, notice := range notices {
		if notice.ID == id && notice.Scope == scope && (scope == "user" || samePath(notice.ProjectPath, project)) {
			continue
		}
		keep = append(keep, notice)
	}
	if len(keep) == 0 {
		delete(doc, "gokinRemovedPlugins")
	} else {
		doc["gokinRemovedPlugins"], _ = json.Marshal(keep)
	}
	return nil
}

// Called under the management lease, on a staged manager. Only the current
// trusted workspace's scopes may be rewritten; another project's records stay
// for that project's own startup. Managed records are never removed.
func (m *Manager) stageCatalogRemovals(ctx context.Context, catalog Catalog) ([]string, error) {
	if !catalog.ForceRemoveDeletedPlugins {
		return nil, nil
	}
	policy, err := m.policy()
	if err != nil {
		return nil, err
	}
	doc, installs, err := m.installs()
	if err != nil {
		return nil, err
	}
	notices, err := removalNotices(doc)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, entry := range catalog.Plugins {
		listed[entry.Name] = true
	}
	var ids []string
	for id := range installs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var removed []string
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, market, _ := strings.Cut(id, "@")
		_, renamed := catalog.Renames[name]
		if market != catalog.Name || listed[name] || renamed || policy.Enabled[id] {
			continue
		}
		for _, scope := range []string{"user", "project", "local"} {
			if scope != "user" && !m.opts.Trusted {
				continue
			}
			found := false
			for _, raw := range installs[id] {
				var record InstallRecord
				if json.Unmarshal(raw, &record) != nil {
					return nil, fmt.Errorf("invalid plugin record")
				}
				found = found || m.scopeMatches(record, scope)
			}
			if !found {
				continue
			}
			// Publisher withdrawal is authoritative. Consumers remain installed
			// and the normal dependency checker deactivates them with diagnostics.
			if err := m.stageRemove(id, scope); err != nil {
				return nil, err
			}
			path, settings, err := m.settings(scope)
			if err != nil {
				return nil, err
			}
			if raw, set := settings["pluginConfigs"]; set {
				var configs map[string]json.RawMessage
				if json.Unmarshal(raw, &configs) != nil {
					return nil, fmt.Errorf("pluginConfigs must be an object")
				}
				delete(configs, id)
				settings["pluginConfigs"], _ = json.Marshal(configs)
				if err := m.save(map[string]any{path: settings}); err != nil {
					return nil, err
				}
			}
			notice := removedPlugin{ID: id, Scope: scope}
			if scope != "user" {
				notice.ProjectPath = m.opts.WorkDir
			}
			notices = append(notices, notice)
			removed = append(removed, id+" ["+scope+"]")
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if len(notices) > 256 {
		notices = notices[len(notices)-256:]
	}
	doc, _, err = m.installs()
	if err != nil {
		return nil, err
	}
	doc["gokinRemovedPlugins"], _ = json.Marshal(notices)
	if err := m.save(map[string]any{filepath.Join(m.pluginDir, "installed_plugins.json"): doc}); err != nil {
		return nil, err
	}
	return removed, nil
}

func (m *Manager) cleanupRemovedData(removed []string) []string {
	if len(removed) == 0 {
		return nil
	}
	_, installs, err := m.installs()
	if err != nil {
		return []string{"Removed plugins; data cleanup could not read registry"}
	}
	seen := map[string]bool{}
	var warnings []string
	for _, label := range removed {
		id, _, _ := strings.Cut(label, " [")
		if seen[id] || len(installs[id]) > 0 {
			continue
		}
		seen[id] = true
		root := filepath.Join(m.pluginDir, "data")
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			continue
		}
		if err := fileutil.EnsurePrivateDir(root); err != nil {
			warnings = append(warnings, id+": data retained because its directory is unsafe")
		} else if err := os.RemoveAll(filepath.Join(root, hooks.ClaudePluginStorageKey(id))); err != nil {
			warnings = append(warnings, id+": plugin data cleanup failed")
		}
	}
	return warnings
}

// ReconcileCatalogRemovals applies cached publisher withdrawals without a
// download or subprocess. Each marketplace commits independently.
func (m *Manager) ReconcileCatalogRemovals(ctx context.Context) (StartupReport, error) {
	report := StartupReport{}
	// An empty/untrusted declaration must not create a plugin store merely
	// because its host reached this lifecycle boundary.
	known, err := m.marketplaces()
	if err != nil || len(known) == 0 {
		return report, err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return report, err
	}
	defer unlock()
	known, err = m.marketplaces()
	if err != nil {
		return report, err
	}
	var names []string
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		var market Marketplace
		if json.Unmarshal(known[name], &market) != nil {
			return report, fmt.Errorf("invalid marketplace registry")
		}
		catalog, err := m.readCatalog(name, market)
		if err != nil {
			report.Errors = append(report.Errors, name+": "+err.Error())
			continue
		}
		staged, err := m.stagedManager()
		if err != nil {
			return report, err
		}
		renamed, err := staged.stageCatalogRenames(ctx, catalog)
		if err != nil {
			report.Errors = append(report.Errors, name+": "+err.Error())
			continue
		}
		removed, err := staged.stageCatalogRemovals(ctx, catalog)
		if err == nil && (len(removed) > 0 || len(catalog.Renames) > 0) {
			err = m.commitStaged(staged)
		}
		if err != nil {
			report.Errors = append(report.Errors, name+": "+err.Error())
			continue
		}
		report.Renamed = append(report.Renamed, renamed...)
		report.Removed = append(report.Removed, removed...)
		report.Skipped = append(report.Skipped, m.cleanupRemovedData(removed)...)
		// The next independent transaction compares against our own last commit.
		if err := m.snapshotState(); err != nil {
			return report, err
		}
	}
	return report, nil
}
