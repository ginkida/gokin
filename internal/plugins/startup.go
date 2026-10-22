package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/pluginpolicy"
)

type marketplaceDeclaration struct {
	Source     Source `json:"source"`
	AutoUpdate *bool  `json:"autoUpdate,omitempty"`
	Scope      string `json:"-"`
	Base       string `json:"-"`
}
type pluginDeclaration struct {
	Enabled bool
	Scope   string
}
type startupPlan struct {
	Markets map[string]marketplaceDeclaration
	Plugins map[string]pluginDeclaration
	Env     map[string]string
}

// StartupReport records independent successes and failures. A failed refresh
// does not erase an existing catalog or prevent other declared plugins loading.
type StartupReport struct {
	Registered []string `json:"registered"`
	Installed  []string `json:"installed"`
	Updated    []string `json:"updated"`
	Removed    []string `json:"removed"`
	Renamed    []string `json:"renamed,omitempty"`
	Skipped    []string `json:"skipped"`
	Errors     []string `json:"errors"`
}

func (m *Manager) startupPlan(ctx context.Context) (startupPlan, error) {
	aliases, aliasErr := m.pluginAliases()
	if aliasErr != nil {
		return startupPlan{}, aliasErr
	}
	plan := startupPlan{Markets: map[string]marketplaceDeclaration{}, Plugins: map[string]pluginDeclaration{}, Env: map[string]string{}}
	managed := startupPlan{Markets: map[string]marketplaceDeclaration{}, Plugins: map[string]pluginDeclaration{}, Env: map[string]string{}}
	merge := func(target *startupPlan, fields map[string]json.RawMessage, scope, base string) error {
		raw, set := fields["extraKnownMarketplaces"]
		if !set {
			raw, set = fields["additionalMarketplaces"]
		}
		if set {
			if string(raw) == "null" {
				clear(target.Markets)
			} else {
				var entries map[string]json.RawMessage
				if json.Unmarshal(raw, &entries) != nil || entries == nil {
					return fmt.Errorf("extraKnownMarketplaces must be an object")
				}
				for name, raw := range entries {
					if string(raw) == "null" {
						delete(target.Markets, name)
						continue
					}
					var entry marketplaceDeclaration
					if !ValidName(name) || json.Unmarshal(raw, &entry) != nil || entry.Source.Kind == "" {
						return fmt.Errorf("invalid marketplace declaration %q", name)
					}
					entry.Scope, entry.Base = scope, base
					target.Markets[name] = entry
				}
			}
		}
		if raw, set := fields["enabledPlugins"]; set {
			if string(raw) == "null" {
				clear(target.Plugins)
			} else {
				var rawEntries map[string]json.RawMessage
				if json.Unmarshal(raw, &rawEntries) != nil || rawEntries == nil {
					return fmt.Errorf("enabledPlugins must be an object")
				}
				raw, _ = json.Marshal(aliases.RenameRawKeys(rawEntries))
				var entries map[string]*bool
				if json.Unmarshal(raw, &entries) != nil || entries == nil {
					return fmt.Errorf("enabledPlugins must be an object")
				}
				for id, on := range entries {
					if !ValidID(id) {
						return fmt.Errorf("invalid enabled plugin identifier")
					}
					if on == nil {
						delete(target.Plugins, id)
					} else {
						target.Plugins[id] = pluginDeclaration{*on, scope}
					}
				}
			}
		}
		if raw, set := fields["env"]; set {
			var env map[string]string
			if json.Unmarshal(raw, &env) != nil {
				return fmt.Errorf("settings env must contain string values")
			}
			// Only update controls are read here. This never exports arbitrary
			// repository env values to fetchers, hooks or model clients.
			for _, key := range []string{"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "FORCE_AUTOUPDATE_PLUGINS"} {
				if value, exists := env[key]; exists {
					target.Env[key] = value
				}
			}
		}
		return nil
	}
	base := m.opts.WorkDir
	if m.opts.Trusted {
		// A worktree's relative project marketplace belongs to its main checkout.
		if info, err := os.Stat(filepath.Join(base, ".git")); err == nil && !info.IsDir() {
			gitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			data, err := runCommand(gitCtx, base, "git", gitArgs("rev-parse", "--git-common-dir")...)
			cancel()
			if err != nil {
				return plan, fmt.Errorf("resolve main checkout: %w", err)
			}
			common := strings.TrimSpace(string(data))
			if !filepath.IsAbs(common) {
				common = filepath.Join(base, common)
			}
			if filepath.Base(common) == ".git" {
				base = filepath.Dir(common)
			}
		}
	}
	for _, scope := range []string{"user", "project", "local"} {
		if scope != "user" && !m.opts.Trusted {
			continue
		}
		_, fields, err := m.settings(scope)
		if err != nil {
			return plan, err
		}
		if err := merge(&plan, fields, scope, base); err != nil {
			return plan, err
		}
	}
	documents, err := pluginpolicy.ReadManagedDocuments(m.opts.ManagedDir)
	if err != nil {
		return plan, err
	}
	if _, err := pluginpolicy.FromDocuments(documents); err != nil {
		return plan, err
	}
	for _, data := range documents {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields == nil {
			return plan, fmt.Errorf("managed settings must be an object")
		}
		if err := merge(&managed, fields, "managed", base); err != nil {
			return plan, err
		}
	}
	for name, entry := range managed.Markets {
		plan.Markets[name] = entry
	}
	for id, entry := range managed.Plugins {
		plan.Plugins[id] = entry
	}
	for key, value := range managed.Env {
		plan.Env[key] = value
	}
	for id, entry := range plan.Plugins {
		if entry.Enabled && strings.HasSuffix(id, "@claude-plugins-official") {
			if _, explicit := plan.Markets["claude-plugins-official"]; !explicit {
				plan.Markets["claude-plugins-official"] = marketplaceDeclaration{Source: pluginpolicy.OfficialMarketplace(), Scope: entry.Scope, Base: base}
			}
		}
	}
	if len(plan.Markets) > 64 || len(plan.Plugins) > 64 {
		return plan, fmt.Errorf("declared marketplaces/plugins exceed count limits")
	}
	return plan, nil
}

func (p startupPlan) updatesAllowed() bool {
	value := func(key string) string {
		if v, set := p.Env[key]; set {
			return v
		}
		return os.Getenv(key)
	}
	return value("FORCE_AUTOUPDATE_PLUGINS") == "1" || value("DISABLE_UPDATES") != "1" && value("DISABLE_AUTOUPDATER") != "1" && value("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") != "1"
}

func defaultAutoUpdate(name string, source Source) bool {
	if !strings.HasPrefix(strings.ToLower(pluginpolicy.GitHubRepo(source)), "anthropics/") {
		return false
	}
	switch name {
	case "claude-plugins-official", "agent-skills", "anthropic-agent-skills", "life-sciences", "healthcare", "financial-services-plugins":
		return true
	}
	return false
}
func (p startupPlan) autoUpdates(name string, market Marketplace) bool {
	if declaration, exists := p.Markets[name]; exists && declaration.AutoUpdate != nil {
		return *declaration.AutoUpdate
	}
	if market.AutoUpdate != nil {
		return *market.AutoUpdate
	}
	return defaultAutoUpdate(name, market.Source)
}

// SyncStartup reconciles explicit settings before runtime components load.
// The caller owns the deadline; this runs no hook, server or helper command.
// It leaves project declarations in the project and preserves enable overrides.
func (m *Manager) SyncStartup(ctx context.Context) (StartupReport, error) {
	return m.syncLifecycle(ctx, true, true)
}

// SyncDeclared restores declarations and missing dependencies without refreshing
// installed versions. Interactive hosts schedule AutoUpdate after user input.
func (m *Manager) SyncDeclared(ctx context.Context) (StartupReport, error) {
	return m.syncLifecycle(ctx, true, false)
}

// AutoUpdate refreshes opted-in registered marketplaces and their installed
// plugins. It reads current settings/policy at execution time, never registers
// a new marketplace, and never runs command sources or header helpers.
func (m *Manager) AutoUpdate(ctx context.Context) (StartupReport, error) {
	return m.syncLifecycle(ctx, false, true)
}

func (m *Manager) syncLifecycle(ctx context.Context, declarations, updates bool) (StartupReport, error) {
	report := StartupReport{Registered: []string{}, Installed: []string{}, Updated: []string{}, Skipped: []string{}, Errors: []string{}}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	plan, err := m.startupPlan(ctx)
	if err != nil {
		return report, err
	}
	known := map[string]Marketplace{}
	rawKnown, err := m.marketplaces()
	if err != nil {
		return report, err
	}
	for name, raw := range rawKnown {
		var entry Marketplace
		if err := json.Unmarshal(raw, &entry); err != nil {
			return report, err
		}
		known[name] = entry
	}
	var names []string
	for name := range plan.Markets {
		names = append(names, name)
	}
	sort.Strings(names)
	registered := map[string]bool{}
	rebased := map[string]bool{}
	failed := map[string]bool{}
	for _, name := range names {
		if !declarations {
			break
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		entry := plan.Markets[name]
		source, err := normalizeMarketplaceSource(entry.Source, entry.Base)
		if err == nil && source.HeadersHelper != "" {
			err = fmt.Errorf("marketplace helper requires explicit marketplace add/update")
		}
		current, exists := known[name]
		if err == nil && exists && !sameSource(current.Source, source) {
			rebased[name] = true
		}
		if err == nil && exists && sameSource(current.Source, source) {
			if _, cachedErr := m.readCatalog(name, current); cachedErr == nil {
				continue
			}
		}
		if err == nil {
			var result Result
			result, err = m.addMarketplace(ctx, source, nil, false, name, false, true)
			if err == nil {
				report.Removed = append(report.Removed, result.Pruned...)
				report.Skipped = append(report.Skipped, result.Warnings...)
			}
		}
		if err != nil {
			report.Errors = append(report.Errors, name+": "+err.Error())
			failed[name] = true
		} else {
			report.Registered = append(report.Registered, name)
			registered[name] = true
		}
	}
	rawKnown, err = m.marketplaces()
	if err != nil {
		return report, err
	}
	for name, raw := range rawKnown {
		var market Marketplace
		if json.Unmarshal(raw, &market) != nil {
			return report, fmt.Errorf("invalid marketplace registry")
		}
		known[name] = market
	}
	// Stable ordering makes diagnostics and scripted checks reproducible.
	names = names[:0]
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	updateMarkets := map[string]bool{}
	if updates && plan.updatesAllowed() {
		for _, name := range names {
			if failed[name] || !plan.autoUpdates(name, known[name]) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if known[name].Source.HeadersHelper != "" {
				report.Skipped = append(report.Skipped, name+": marketplace helper requires explicit update")
				continue
			}
			if !registered[name] {
				if result, err := m.UpdateMarketplace(ctx, name, nil); err != nil {
					report.Errors = append(report.Errors, name+": "+err.Error())
					continue
				} else {
					report.Removed = append(report.Removed, result.Pruned...)
					report.Skipped = append(report.Skipped, result.Warnings...)
				}
			}
			updateMarkets[name] = true
		}
	}
	if declarations {
		withdrawals, withdrawalErr := m.ReconcileCatalogRemovals(ctx)
		report.Removed = append(report.Removed, withdrawals.Removed...)
		report.Renamed = append(report.Renamed, withdrawals.Renamed...)
		report.Errors = append(report.Errors, withdrawals.Errors...)
		report.Skipped = append(report.Skipped, withdrawals.Skipped...)
		if withdrawalErr != nil {
			return report, withdrawalErr
		}
		renamed, renameErr := m.RestoreRenamedLocalPlugins(ctx)
		report.Installed = append(report.Installed, renamed.Installed...)
		report.Errors = append(report.Errors, renamed.Errors...)
		report.Skipped = append(report.Skipped, renamed.Skipped...)
		if renameErr != nil {
			return report, renameErr
		}
		restored, restoreErr := m.RestoreCommandSources(ctx)
		report.Installed = append(report.Installed, restored.Installed...)
		report.Updated = append(report.Updated, restored.Updated...)
		report.Errors = append(report.Errors, restored.Errors...)
		report.Skipped = append(report.Skipped, restored.Skipped...)
		if restoreErr != nil {
			return report, restoreErr
		}
		// Withdrawals removed settings keys; do not reinstall stale declarations.
		plan, err = m.startupPlan(ctx)
		if err != nil {
			return report, err
		}
	}
	rawKnown, err = m.marketplaces()
	if err != nil {
		return report, err
	}
	_, installs, err := m.installs()
	if err != nil {
		return report, err
	}
	ids := make([]string, 0, len(plan.Plugins)+len(installs))
	seen := map[string]bool{}
	for id := range plan.Plugins {
		if !declarations {
			break
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for id := range installs {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !ValidID(id) {
			return report, fmt.Errorf("invalid installed plugin identifier")
		}
		aliases, aliasErr := m.pluginAliases()
		if aliasErr != nil {
			return report, aliasErr
		}
		if _, changed, _ := aliases.Resolve(id); changed {
			continue
		}
		market := strings.SplitN(id, "@", 2)[1]
		if market == "inline" || market == "skills-dir" {
			continue // Session/directory discovery owns these, not an installer.
		}
		if failed[market] {
			continue
		}
		declaration, wanted := plan.Plugins[id]
		wanted = wanted && declarations
		var scopes []string
		present := map[string]bool{}
		repair := map[string]bool{}
		for _, raw := range installs[id] {
			var record InstallRecord
			if json.Unmarshal(raw, &record) != nil {
				return report, fmt.Errorf("invalid plugin install record")
			}
			if record.Scope != "user" && (!m.opts.Trusted || !samePath(record.ProjectPath, m.opts.WorkDir)) {
				continue
			}
			wantedScope := declaration.Scope
			if wantedScope == "managed" {
				wantedScope = "user"
			}
			if wanted && declaration.Enabled && wantedScope == record.Scope {
				info, statErr := os.Stat(record.InstallPath)
				repair[record.Scope] = statErr != nil || !info.IsDir()
			}
			if rebased[market] {
				repair[record.Scope] = true
			}
			if required, exists := plan.Markets[market]; exists && required.Scope == "managed" && wanted && declaration.Enabled {
				source, sourceErr := normalizeMarketplaceSource(required.Source, required.Base)
				if sourceErr != nil || record.MarketplaceSource == nil || !sameSource(*record.MarketplaceSource, source) {
					repair[record.Scope] = true
				}
			}
			if (updateMarkets[market] || declarations && repair[record.Scope]) && !present[record.Scope] {
				scopes = append(scopes, record.Scope)
			}
			present[record.Scope] = true
		}
		if !declaration.Enabled && declaration.Scope == "managed" {
			continue
		}
		if wanted && declaration.Enabled {
			scope := declaration.Scope
			if scope == "managed" {
				scope = "user"
			}
			if !present[scope] {
				if aliases.IsTarget(id) {
					_, catalogSource, _, entry, lookupErr := m.findEntry(id, rawKnown)
					var relative string
					if lookupErr != nil || catalogSource.Source.Kind != "directory" && catalogSource.Source.Kind != "file" || json.Unmarshal(entry.Source, &relative) != nil {
						report.Skipped = append(report.Skipped, id+": renamed plugin is not cached; install it explicitly: gokin plugin install "+id+" --scope "+scope)
						continue
					}
				}
				if failed[market] {
					continue
				}
				if scope != "user" {
					_, _, _, entry, lookupErr := m.findEntry(id, rawKnown)
					if lookupErr != nil {
						report.Errors = append(report.Errors, id+": "+lookupErr.Error())
						continue
					}
					var relative string
					if json.Unmarshal(entry.Source, &relative) != nil {
						report.Skipped = append(report.Skipped, id+": external project source requires plugin install --scope "+scope)
						continue
					}
				}
				scopes = append(scopes, scope)
			}
		}
		sort.Strings(scopes)
		for _, scope := range scopes {
			result, err := m.Install(ctx, id, InstallOptions{Scope: scope, Update: present[scope], KeepEnabledSettings: true, Background: true})
			if err != nil {
				if strings.Contains(err.Error(), "explicit install/update") {
					report.Skipped = append(report.Skipped, id+" ["+scope+"]: "+err.Error())
				} else {
					report.Errors = append(report.Errors, id+" ["+scope+"]: "+err.Error())
				}
				continue
			}
			if !present[scope] || repair[scope] {
				report.Installed = append(report.Installed, id+" ["+scope+"]")
			} else if result.Changed {
				report.Updated = append(report.Updated, id+" ["+scope+"]")
			}
			report.Skipped = append(report.Skipped, result.Warnings...)
		}
	}
	if !declarations {
		return report, ctx.Err()
	}
	repaired, repairErr := m.RepairDependencies(ctx)
	report.Installed = append(report.Installed, repaired.Installed...)
	report.Errors = append(report.Errors, repaired.Errors...)
	report.Skipped = append(report.Skipped, repaired.Skipped...)
	return report, repairErr
}

// SetMarketplaceAutoUpdate persists a user's preference without overriding a
// managed field or promoting a project's source to the user settings tier.
func (m *Manager) SetMarketplaceAutoUpdate(ctx context.Context, name string, on bool) (Result, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	plan, err := m.startupPlan(ctx)
	if err != nil {
		return Result{}, err
	}
	if !plan.updatesAllowed() {
		return Result{}, fmt.Errorf("plugin automatic updates are disabled by update controls")
	}
	declaration, declared := plan.Markets[name]
	if declared && declaration.Scope == "managed" && declaration.AutoUpdate != nil {
		return Result{}, fmt.Errorf("auto-update for %q is set by managed settings", name)
	}
	known, err := m.marketplaces()
	if err != nil {
		return Result{}, err
	}
	var entry map[string]json.RawMessage
	if json.Unmarshal(known[name], &entry) != nil || entry == nil {
		return Result{}, fmt.Errorf("marketplace is not registered")
	}
	entry["autoUpdate"], _ = json.Marshal(on)
	known[name], _ = json.Marshal(entry)
	documents := map[string]any{filepath.Join(m.pluginDir, "known_marketplaces.json"): known}
	if declared && declaration.Scope != "managed" {
		path, settings, err := m.settings(declaration.Scope)
		if err != nil {
			return Result{}, err
		}
		key := "extraKnownMarketplaces"
		if _, exists := settings[key]; !exists {
			key = "additionalMarketplaces"
		}
		var declarations map[string]json.RawMessage
		if json.Unmarshal(settings[key], &declarations) != nil {
			return Result{}, fmt.Errorf("invalid marketplace settings")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(declarations[name], &fields) != nil || fields == nil {
			return Result{}, fmt.Errorf("invalid marketplace declaration")
		}
		fields["autoUpdate"], _ = json.Marshal(on)
		declarations[name], _ = json.Marshal(fields)
		settings[key], _ = json.Marshal(declarations)
		documents[path] = settings
	}
	if err := m.save(documents); err != nil {
		return Result{}, err
	}
	return Result{Command: "marketplace-auto-update", Outcome: "ok", Marketplace: name, Changed: true, Message: fmt.Sprintf("Marketplace %s: auto-update=%t.", name, on)}, nil
}
