package plugins

import (
	"context"
	"os"
	"sort"
	"strings"
)

// RepairDependencies fills missing declared dependencies without updating the
// installed parent's contents or running command sources and header helpers.
// Explicitly disabled, present dependencies remain disabled until enabled.
func (m *Manager) RepairDependencies(ctx context.Context) (StartupReport, error) {
	report := StartupReport{}
	for _, scope := range []string{"user", "project", "local"} {
		if scope != "user" && !m.opts.Trusted {
			continue
		}
		nodes, err := m.dependencyNodes(scope)
		if err != nil {
			return report, err
		}
		var ids []string
		for id, node := range nodes {
			if node.Enabled && len(node.Dependencies) > 0 {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			// An earlier repair may have filled this entire chain.
			nodes, err = m.dependencyNodes(scope)
			if err != nil {
				return report, err
			}
			seen := map[string]bool{}
			var missing func(string) bool
			missing = func(target string) bool {
				if seen[target] {
					return false
				}
				seen[target] = true
				node, exists := nodes[target]
				if !exists {
					return true
				}
				if info, err := os.Stat(node.Record.InstallPath); err != nil || !info.IsDir() {
					return true
				}
				if !node.Enabled {
					return false
				}
				for _, dep := range node.Dependencies {
					if missing(dep.ID(target)) {
						return true
					}
				}
				return false
			}
			if !missing(id) {
				continue
			}
			result, err := m.Install(ctx, id, InstallOptions{Scope: scope, KeepEnabledSettings: true, Background: true, repairOnly: true})
			if err != nil {
				issue := id + " [" + scope + "]: " + err.Error()
				if strings.Contains(err.Error(), "explicit install/update") {
					report.Skipped = append(report.Skipped, issue)
				} else {
					report.Errors = append(report.Errors, issue)
				}
				continue
			}
			if result.Changed {
				report.Installed = append(report.Installed, id+" ["+scope+"] dependencies")
			}
			report.Skipped = append(report.Skipped, result.Warnings...)
		}
	}
	return report, ctx.Err()
}
