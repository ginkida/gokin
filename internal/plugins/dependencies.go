package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gokin/internal/plugindeps"
	"gokin/internal/pluginpolicy"
)

type installedDependencyNode struct {
	plugindeps.Node
	Record InstallRecord
}

func (m *Manager) dependencyNodes(scope string) (map[string]installedDependencyNode, error) {
	doc, installed, err := m.installs()
	if err != nil {
		return nil, err
	}
	_, settings, err := m.settings(scope)
	if err != nil {
		return nil, err
	}
	enabled, err := enabledEntries(settings)
	if err != nil {
		return nil, err
	}
	policy, err := m.policy()
	if err != nil {
		return nil, err
	}
	aliases, err := pluginpolicy.ReadPluginAliases(doc)
	if err != nil {
		return nil, err
	}
	result := map[string]installedDependencyNode{}
	for id, records := range installed {
		if _, changed, _ := aliases.Resolve(id); changed {
			continue
		}
		for _, raw := range records {
			var record InstallRecord
			if json.Unmarshal(raw, &record) != nil {
				return nil, fmt.Errorf("invalid plugin record")
			}
			if !m.scopeMatches(record, scope) {
				continue
			}
			manifest, err := m.installedManifest(record)
			unavailable := os.IsNotExist(err)
			if err != nil && !unavailable {
				return nil, err
			}
			if unavailable {
				manifest = map[string]json.RawMessage{}
			}
			var name, version string
			json.Unmarshal(manifest["name"], &name)
			json.Unmarshal(manifest["version"], &version)
			if name == "" {
				name, _, _ = strings.Cut(id, "@")
			}
			if record.ResolvedVersion != "" {
				version = record.ResolvedVersion
			}
			dependencies, err := plugindeps.Parse(manifest["dependencies"])
			if err != nil {
				return nil, fmt.Errorf("plugin %s: %w", id, err)
			}
			for i := range dependencies {
				dependencies[i].Name, dependencies[i].Marketplace = renamedDependency(aliases, id, dependencies[i].Name, dependencies[i].Marketplace)
			}
			on := !unavailable
			if raw, exists := manifest["defaultEnabled"]; exists {
				json.Unmarshal(raw, &on)
			}
			if value, set := enabled[id]; set {
				on = value
			}
			if value, set := policy.Enabled[id]; set {
				on = value
			}
			if record.Source.Kind == "command" && record.Source.Mode == "link" && pluginpolicy.PathWithin(record.LinkedRoot, m.opts.WorkDir) {
				on = false
			}
			if info, statErr := os.Stat(record.InstallPath); unavailable || statErr != nil || !info.IsDir() {
				on = false
			}
			result[id] = installedDependencyNode{Node: plugindeps.Node{ID: id, Name: name, Version: version, Enabled: on, Dependencies: dependencies}, Record: record}
			break
		}
	}
	return result, nil
}

func dependencyIssues(nodes map[string]installedDependencyNode) map[string]string {
	graph := map[string]plugindeps.Node{}
	for id, node := range nodes {
		graph[id] = node.Node
	}
	return plugindeps.Check(graph)
}

func incomingRanges(nodes map[string]installedDependencyNode, target string) []string {
	var ranges []string
	seen := map[string]bool{}
	for id, node := range nodes {
		for _, dependency := range node.Dependencies {
			if dependency.ID(id) == target && dependency.Version != "" && !seen[dependency.Version] {
				seen[dependency.Version] = true
				ranges = append(ranges, dependency.Version)
			}
		}
	}
	sort.Strings(ranges)
	return ranges
}

// Installation stages all state changes behind one lease. Immutable copied
// artifacts can remain cached on failure; no registry/settings changes escape.
func (m *Manager) installGraph(ctx context.Context, input string, opts InstallOptions) (Result, error) {
	known, err := m.marketplaces()
	if err != nil {
		return Result{}, err
	}
	_, installed, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	if opts.Update {
		input, err = m.resolveID(input, installed)
		if err != nil {
			return Result{}, err
		}
		opts.Scope, err = m.resolveScope(input, opts.Scope, installed)
		if err != nil {
			return Result{}, err
		}
	} else if opts.Scope == "" {
		opts.Scope = "user"
	}
	rootID, _, rootCatalog, _, err := m.findEntry(input, known)
	if err != nil {
		return Result{}, err
	}
	before, err := m.dependencyNodes(opts.Scope)
	if err != nil {
		return Result{}, err
	}
	beforeIssues := dependencyIssues(before)
	opts.promote = !opts.Update && !opts.dependency && !opts.KeepEnabledSettings
	transaction, err := NewManager(m.opts)
	if err != nil {
		return Result{}, err
	}
	transaction.transaction = map[string]map[string]json.RawMessage{}
	opts.versionRanges = incomingRanges(before, rootID)
	result, err := transaction.installOne(ctx, rootID, opts)
	if err != nil {
		return Result{}, err
	}
	rootMarket := strings.SplitN(rootID, "@", 2)[1]
	allowed := map[string]bool{rootMarket: true}
	for _, market := range rootCatalog.AllowCrossMarketplaceDependenciesOn {
		allowed[market] = true
	}
	resolved := map[string]string{}
	skipped := map[string]bool{}
	var added []string
	converged := false
	graphChanged := false
	needed := map[string]bool{rootID: true}
	for pass := 0; pass < 128; pass++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		nodes, err := transaction.dependencyNodes(opts.Scope)
		if err != nil {
			return Result{}, err
		}
		reachable := map[string]bool{}
		parents := map[string]string{}
		state := map[string]int{}
		var visit func(string, int) error
		visit = func(id string, depth int) error {
			if state[id] == 1 || depth > 64 {
				return fmt.Errorf("circular or excessively deep plugin dependencies at %s", id)
			}
			if state[id] == 2 {
				return nil
			}
			state[id], reachable[id] = 1, true
			for _, dep := range nodes[id].Dependencies {
				target := dep.ID(id)
				if parents[target] == "" {
					parents[target] = id
				}
				if err := visit(target, depth+1); err != nil {
					return err
				}
			}
			state[id] = 2
			return nil
		}
		if err := visit(rootID, 0); err != nil {
			return Result{}, err
		}
		needed = reachable
		if len(reachable) > 64 {
			return Result{}, fmt.Errorf("plugin dependency graph exceeds 64 plugins")
		}
		ids := make([]string, 0, len(reachable))
		for id := range reachable {
			if id != rootID {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		changed := false
		for _, id := range ids {
			if skipped[id] {
				continue
			}
			market := strings.SplitN(id, "@", 2)[1]
			original, previouslyInstalled := before[id]
			if !allowed[market] && (!previouslyInstalled || !original.Enabled) {
				issue := fmt.Sprintf("Dependency %q (required by %s) is in marketplace %q, which is not in allowCrossMarketplaceDependenciesOn", id, parents[id], market)
				_, _, _, parentEntry, entryErr := transaction.findEntry(parents[id], known)
				if entryErr == nil {
					entryDeps, _ := plugindeps.Parse(parentEntry.Raw["dependencies"])
					for _, entryDep := range entryDeps {
						if entryDep.ID(parents[id]) == id {
							return Result{}, fmt.Errorf("%s", issue)
						}
					}
				}
				result.Warnings = append(result.Warnings, issue)
				skipped[id] = true
				continue
			}
			ranges := incomingRanges(nodes, id)
			signature := strings.Join(ranges, "\n")
			node, exists := nodes[id]
			info, statErr := os.Stat(node.Record.InstallPath)
			available := exists && statErr == nil && info.IsDir()
			needsVersion := !available || !satisfiesRanges(node.Version, ranges)
			previousSignature, alreadyResolved := resolved[id]
			previousRanges := strings.Join(incomingRanges(before, id), "\n")
			_, rootExisted := before[rootID]
			refresh := opts.Update || len(ranges) > 0 && (!rootExisted || previousRanges != signature)
			if !needsVersion && (!refresh || alreadyResolved && previousSignature == signature) {
				if !node.Enabled && nodes[rootID].Enabled && !opts.repairOnly {
					if err := transaction.enableDependency(id, opts.Scope); err != nil {
						return Result{}, err
					}
					changed = true
					graphChanged = true
				}
				continue
			}
			if value, done := resolved[id]; done && value == signature {
				continue
			}
			_, _, _, entry, lookupErr := transaction.findEntry(id, known)
			if lookupErr == nil {
				source, sourceErr := entrySource(entry, "")
				if sourceErr == nil && (source.Kind == "command" || entry.HeadersHelper != "") {
					if available && !needsVersion {
						resolved[id] = signature
						continue
					}
					return Result{}, fmt.Errorf("dependency %s requires an explicit plugin install/update before installing %s", id, rootID)
				}
			}
			dependencyOptions := InstallOptions{Scope: opts.Scope, Update: exists, Background: opts.Background, KeepEnabledSettings: opts.repairOnly && exists, dependency: true, versionRanges: ranges}
			depResult, err := transaction.installOne(ctx, id, dependencyOptions)
			if err != nil {
				return Result{}, fmt.Errorf("dependency %s required by %s: %w", id, parents[id], err)
			}
			if !exists {
				added = append(added, id)
			}
			result.Warnings = append(result.Warnings, depResult.Warnings...)
			graphChanged = graphChanged || depResult.Changed || !available
			resolved[id] = signature
			changed = true
		}
		if !changed {
			converged = true
			break
		}
	}
	if !converged {
		return Result{}, fmt.Errorf("plugin dependency version resolution did not converge")
	}
	after, err := transaction.dependencyNodes(opts.Scope)
	if err != nil {
		return Result{}, err
	}
	issues := dependencyIssues(after)
	for id, node := range before {
		if id != rootID {
			for _, dep := range node.Dependencies {
				target := dep.ID(id)
				if dep.Version != "" && satisfiesRanges(before[target].Version, []string{dep.Version}) && !satisfiesRanges(after[target].Version, []string{dep.Version}) {
					return Result{}, fmt.Errorf("dependency has conflicting version requirements: %s requires %s %s", id, target, dep.Version)
				}
			}
		}
		if id != rootID && node.Enabled && beforeIssues[id] == "" && issues[id] != "" {
			return Result{}, fmt.Errorf("dependency has conflicting version requirements: %s: %s", id, issues[id])
		}
	}
	if issue := issues[rootID]; issue != "" {
		result.Warnings = append(result.Warnings, rootID+": "+issue)
	}
	if err := m.verifySnapshot(); err != nil {
		return Result{}, err
	}
	// Recheck every source against policy at the final publication boundary.
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	for id, node := range after {
		if !needed[id] {
			continue
		}
		if _, touched := transaction.transaction[filepath.Join(m.pluginDir, "installed_plugins.json")]; !touched {
			break
		}
		if policy.DisabledCommands && node.Record.Source.Kind == "command" {
			return Result{}, fmt.Errorf("plugin %s uses a disabled command source", id)
		}
		if node.Record.MarketplaceSource != nil {
			if err := policy.Check(*node.Record.MarketplaceSource); err != nil {
				return Result{}, fmt.Errorf("plugin %s: %w", id, err)
			}
		}
		_, market, _ := strings.Cut(id, "@")
		if required, set := policy.Required[market]; set && policy.Enabled[id] {
			required = pluginpolicy.ResolveLocalSource(required, m.opts.WorkDir)
			if node.Record.MarketplaceSource == nil || !pluginpolicy.EqualSource(*node.Record.MarketplaceSource, required) {
				return Result{}, fmt.Errorf("plugin %s requires reinstallation from its managed marketplace", id)
			}
		}
		if on, set := policy.Enabled[id]; set && !on {
			return Result{}, fmt.Errorf("plugin %s was disabled during installation", id)
		}
	}
	documents := map[string]any{}
	for path, fields := range transaction.transaction {
		documents[path] = fields
	}
	if err := m.save(documents); err != nil {
		return Result{}, err
	}
	sort.Strings(added)
	result.Changed = result.Changed || graphChanged
	if len(added) > 0 {
		result.Dependencies = added
		result.Changed = true
		result.Message += " Installed dependencies: " + strings.Join(added, ", ") + "."
	}
	return result, nil
}

func (m *Manager) enableDependency(id, scope string) error {
	policy, err := m.policy()
	if err != nil {
		return err
	}
	if on, set := policy.Enabled[id]; set && !on {
		return fmt.Errorf("dependency %s is disabled by managed policy", id)
	}
	path, fields, err := m.settings(scope)
	if err != nil {
		return err
	}
	enabled, err := enabledEntries(fields)
	if err != nil {
		return err
	}
	enabled[id] = true
	fields["enabledPlugins"], _ = json.Marshal(enabled)
	return m.save(map[string]any{path: fields})
}
