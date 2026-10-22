package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gokin/internal/pluginpolicy"
	"gokin/internal/pluginsecrets"
)

func renameObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("plugin rename settings must be objects")
	}
	return fields, nil
}

// The explicit destination owns conflicts, including null removals. Options
// merge per key so configuring the new name does not discard other old values.
func mergeRenamedConfig(from, to json.RawMessage) (json.RawMessage, error) {
	left, err := renameObject(from)
	if err != nil {
		return nil, err
	}
	if len(to) == 0 {
		return from, nil
	}
	if string(to) == "null" {
		return to, nil
	}
	right, err := renameObject(to)
	if err != nil {
		return nil, err
	}
	for key, value := range left {
		current, set := right[key]
		if !set {
			right[key] = value
			continue
		}
		if key == "options" && string(current) != "null" {
			old, err := renameObject(value)
			if err != nil {
				return nil, err
			}
			options, err := renameObject(current)
			if err != nil {
				return nil, err
			}
			for name, item := range old {
				if _, set := options[name]; !set {
					options[name] = item
				}
			}
			right[key], _ = json.Marshal(options)
		}
	}
	return json.Marshal(right)
}

func (m *Manager) stageCatalogRenames(ctx context.Context, catalog Catalog) ([]string, error) {
	if len(catalog.Renames) == 0 {
		return nil, nil
	}
	doc, installed, err := m.installs()
	if err != nil {
		return nil, err
	}
	aliases, err := pluginpolicy.ReadPluginAliases(doc)
	if err != nil {
		return nil, err
	}
	previous := pluginpolicy.PluginAliases{}
	for id, target := range aliases {
		previous[id] = target
	}
	var ids []string
	for name, target := range catalog.Renames {
		id := name + "@" + catalog.Name
		if target == nil {
			aliases[id] = nil
		} else {
			next := *target + "@" + catalog.Name
			aliases[id] = &next
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := aliases.Depth(ids[i]), aliases.Depth(ids[j])
		if left != right {
			return left < right
		}
		return ids[i] < ids[j]
	})
	doc[pluginpolicy.PluginAliasesField], _ = json.Marshal(aliases)
	if _, err := pluginpolicy.ReadPluginAliases(doc); err != nil {
		return nil, err
	}
	if err := m.save(map[string]any{filepath.Join(m.pluginDir, "installed_plugins.json"): doc}); err != nil {
		return nil, err
	}
	var notices []string
	referenced := map[string]bool{}
	for _, scope := range []string{"user", "project", "local"} {
		if scope != "user" && !m.opts.Trusted {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, fields, err := m.settings(scope)
		if err != nil {
			return nil, err
		}
		changed := false
		for _, field := range []string{"enabledPlugins", "pluginConfigs"} {
			values, err := renameObject(fields[field])
			if err != nil {
				return nil, err
			}
			fieldChanged := false
			for _, id := range ids {
				value, set := values[id]
				if !set {
					continue
				}
				target, _, err := aliases.Resolve(id)
				if err != nil {
					return nil, err
				}
				delete(values, id)
				if target != "" {
					if current, set := values[target]; !set {
						values[target] = value
					} else if field == "pluginConfigs" {
						merged, err := mergeRenamedConfig(value, current)
						if err != nil {
							return nil, err
						}
						values[target] = merged
					}
				}
				referenced[id] = true
				fieldChanged = true
				message := id + " [" + scope + "]: Removed from marketplace"
				if target != "" {
					message = id + " [" + scope + "]: Renamed to " + target
				}
				notices = append(notices, message)
			}
			if fieldChanged {
				fields[field], _ = json.Marshal(values)
				changed = true
			}
		}
		if changed {
			if err := m.save(map[string]any{path: fields}); err != nil {
				return nil, err
			}
		}
	}
	managed, err := pluginpolicy.ReadManaged(m.opts.ManagedDir)
	if err != nil {
		return nil, err
	}
	secretPath := pluginsecrets.Path(m.opts.UserDir)
	secrets, err := m.readObject(secretPath)
	if err != nil {
		return nil, err
	}
	if len(secrets) > 0 {
		known, err := m.marketplaces()
		if err != nil {
			return nil, err
		}
		var marketplace Marketplace
		if json.Unmarshal(known[catalog.Name], &marketplace) != nil {
			return nil, fmt.Errorf("rename marketplace provenance is unavailable")
		}
		var encrypted map[string]json.RawMessage
		if json.Unmarshal(secrets["plugins"], &encrypted) != nil {
			return nil, fmt.Errorf("invalid encrypted plugin option store")
		}
		for _, id := range ids {
			target, _, err := aliases.Resolve(id)
			if err != nil {
				return nil, err
			}
			prior, existed, _ := previous.Resolve(id)
			if !referenced[id] && existed && prior == target {
				continue
			}
			if !referenced[id] && len(installed[id]) == 0 && !managed.Enabled[id] {
				continue
			}
			if _, hasSecret := encrypted[id]; hasSecret && target != "" {
				if len(installed[id]) == 0 {
					return nil, fmt.Errorf("cannot migrate encrypted options without installed marketplace provenance; configure the new plugin explicitly")
				}
				for _, raw := range installed[id] {
					var record InstallRecord
					if json.Unmarshal(raw, &record) != nil {
						return nil, fmt.Errorf("invalid plugin record")
					}
					if record.MarketplaceSource == nil || !sameSource(*record.MarketplaceSource, marketplace.Source) {
						return nil, fmt.Errorf("cannot migrate encrypted options across a changed marketplace source; reinstall and configure the new plugin explicitly")
					}
				}
			}
			// Old immutable versions can remain selected by another session.
			// Retain their authenticated options until their last uninstall.
			secrets, err = pluginsecrets.Rename(m.opts.UserDir, id, target, secrets, len(installed[id]) > 0 || managed.Enabled[id], m.opts.SecretKeyring)
			if err != nil {
				return nil, err
			}
		}
		if err := m.save(map[string]any{secretPath: secrets}); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	unique := notices[:0]
	for _, notice := range notices {
		if !seen[notice] {
			seen[notice] = true
			unique = append(unique, notice)
		}
	}
	return unique, nil
}

// Local-directory aliases can be provisioned from files already on disk. A
// renamed Git/URL plugin requires the explicit installation described by the
// reference, rather than silently fetching a new executable identity.
func (m *Manager) RestoreRenamedLocalPlugins(ctx context.Context) (StartupReport, error) {
	report := StartupReport{}
	doc, installed, err := m.installs()
	if err != nil {
		return report, err
	}
	aliases, err := pluginpolicy.ReadPluginAliases(doc)
	if err != nil {
		return report, err
	}
	policy, err := m.policy()
	if err != nil {
		return report, err
	}
	manual := map[string]bool{}
	for old, records := range installed {
		target, changed, _ := aliases.Resolve(old)
		if !changed || target == "" {
			continue
		}
		for _, raw := range records {
			var record InstallRecord
			if json.Unmarshal(raw, &record) != nil {
				return report, fmt.Errorf("invalid plugin record")
			}
			if !m.scopeMatches(record, record.Scope) || record.Scope != "user" && !m.opts.Trusted {
				continue
			}
			if !record.AutoInstalled {
				manual[target+"\x00"+record.Scope] = true
			}
		}
	}
	known, err := m.marketplaces()
	if err != nil {
		return report, err
	}
	var ids []string
	for id := range aliases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		target, _, err := aliases.Resolve(id)
		if err != nil {
			return report, err
		}
		if target == "" {
			continue
		}
		if on, set := policy.Enabled[target]; set && !on {
			continue
		}
		_, market, _, entry, err := m.findEntry(target, known)
		if err != nil || market.Source.Kind != "directory" && market.Source.Kind != "file" {
			continue
		}
		var relative string
		if json.Unmarshal(entry.Source, &relative) != nil {
			continue
		}
		for _, raw := range installed[id] {
			var record InstallRecord
			if json.Unmarshal(raw, &record) != nil {
				return report, fmt.Errorf("invalid plugin record")
			}
			if !m.scopeMatches(record, record.Scope) || record.Scope != "user" && !m.opts.Trusted {
				continue
			}
			_, fresh, err := m.installs()
			if err != nil {
				return report, err
			}
			present := false
			promote := false
			for _, raw := range fresh[target] {
				var candidate InstallRecord
				json.Unmarshal(raw, &candidate)
				present = present || m.scopeMatches(candidate, record.Scope)
				promote = promote || m.scopeMatches(candidate, record.Scope) && candidate.AutoInstalled && manual[target+"\x00"+record.Scope]
			}
			if present {
				if promote {
					if _, err := m.Install(ctx, target, InstallOptions{Scope: record.Scope, Background: true}); err != nil {
						report.Errors = append(report.Errors, target+": "+err.Error())
					}
				}
				continue
			}
			result, err := m.Install(ctx, target, InstallOptions{Scope: record.Scope, Background: true, KeepEnabledSettings: true, dependency: !manual[target+"\x00"+record.Scope]})
			if err != nil {
				report.Errors = append(report.Errors, target+": "+err.Error())
				continue
			}
			report.Installed = append(report.Installed, target+" ["+record.Scope+"]")
			report.Skipped = append(report.Skipped, result.Warnings...)
		}
	}
	return report, nil
}

func (m *Manager) requireCurrentPluginID(id string) error {
	aliases, err := m.pluginAliases()
	if err != nil {
		return err
	}
	target, changed, err := aliases.Resolve(id)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if target == "" {
		return fmt.Errorf("plugin %s was removed from its marketplace", id)
	}
	return fmt.Errorf("plugin %s was renamed to %s; use the current identifier", id, target)
}

func (m *Manager) pluginAliases() (pluginpolicy.PluginAliases, error) {
	doc, _, err := m.installs()
	if err != nil {
		return nil, err
	}
	return pluginpolicy.ReadPluginAliases(doc)
}

func renamedDependency(aliases pluginpolicy.PluginAliases, parent, name, market string) (string, string) {
	if market == "" {
		_, market, _ = strings.Cut(parent, "@")
	}
	target, changed, err := aliases.Resolve(name + "@" + market)
	if err == nil && changed && target != "" {
		name, market, _ = strings.Cut(target, "@")
	}
	return name, market
}
