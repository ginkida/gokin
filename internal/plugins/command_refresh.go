package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gokin/internal/pluginpolicy"
)

var errCommandRefreshSkipped = errors.New("command source refresh skipped")

func (p startupPlan) commandRefreshAllowed() bool {
	value, set := p.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"]
	if !set {
		value = os.Getenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
	}
	return value != "1"
}

// commandCandidate follows runtime scope precedence without requiring a
// working cache: restoring a missing cache must still be possible. All callers
// re-read this selection under the management lease before running any command.
func (m *Manager) commandCandidate(id string, plan startupPlan) (InstallRecord, bool, error) {
	doc, installed, err := m.installs()
	if err != nil {
		return InstallRecord{}, false, err
	}
	aliases, err := pluginpolicy.ReadPluginAliases(doc)
	if err != nil {
		return InstallRecord{}, false, err
	}
	if _, changed, _ := aliases.Resolve(id); changed {
		return InstallRecord{}, false, nil
	}
	best, rank := InstallRecord{}, 0
	for _, raw := range installed[id] {
		var record InstallRecord
		if json.Unmarshal(raw, &record) != nil {
			return best, false, fmt.Errorf("invalid plugin record")
		}
		candidateRank := map[string]int{"user": 1, "project": 2, "local": 3}[record.Scope]
		if candidateRank == 0 || !m.scopeMatches(record, record.Scope) || record.Scope != "user" && !m.opts.Trusted {
			continue
		}
		if candidateRank > rank {
			best, rank = record, candidateRank
		}
	}
	if rank == 0 || best.Source.Kind != "command" || !best.Managed {
		return best, false, nil
	}
	if best.Source.Mode == "link" && pluginpolicy.PathWithin(best.LinkedRoot, m.opts.WorkDir) {
		return best, false, nil
	}
	if declaration, set := plan.Plugins[id]; set {
		return best, declaration.Enabled, nil
	}
	manifest, err := m.installedManifest(best)
	if err == nil {
		var enabled bool
		if raw, set := manifest["defaultEnabled"]; set {
			if json.Unmarshal(raw, &enabled) != nil {
				return best, false, fmt.Errorf("invalid defaultEnabled")
			}
			return best, enabled, nil
		}
	}
	return best, true, nil
}

// RefreshCommandSources runs each enabled, previously accepted command once
// for this manager/session. It never refreshes a marketplace or authorizes a
// changed command/mode. Ordinary auto-update controls do not apply.
func (m *Manager) RefreshCommandSources(ctx context.Context) (StartupReport, error) {
	return m.refreshCommandSources(ctx, false)
}

// RestoreCommandSources retries accepted commands whose enabled cache is
// missing, at startup and explicit reload boundaries. Successful restores also
// satisfy the session's once-only refresh, but an explicit reload may retry a
// failed restore.
func (m *Manager) RestoreCommandSources(ctx context.Context) (StartupReport, error) {
	return m.refreshCommandSources(ctx, true)
}

func (m *Manager) refreshCommandSources(ctx context.Context, missingOnly bool) (StartupReport, error) {
	report := StartupReport{}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	plan, err := m.startupPlan(ctx)
	if err != nil {
		return report, err
	}
	if !plan.commandRefreshAllowed() {
		return report, nil
	}
	_, installed, err := m.installs()
	if err != nil {
		return report, err
	}
	var ids []string
	for id := range installed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		candidate, enabled, err := m.commandCandidate(id, plan)
		if err != nil {
			report.Errors = append(report.Errors, id+": "+err.Error())
			continue
		}
		if !enabled {
			continue
		}
		if info, err := os.Stat(candidate.InstallPath); missingOnly && err == nil && info.IsDir() {
			continue
		}
		result, restored, err := m.refreshCommandSource(ctx, id, missingOnly)
		if errors.Is(err, errCommandRefreshSkipped) {
			continue
		}
		if err != nil {
			issue := id + " [" + candidate.Scope + "]: " + err.Error()
			if strings.Contains(err.Error(), "explicit plugin update") {
				report.Skipped = append(report.Skipped, issue)
			} else {
				report.Errors = append(report.Errors, issue)
			}
			continue
		}
		label := id + " [" + result.Scope + "]"
		if restored {
			report.Installed = append(report.Installed, label)
		} else if result.Changed {
			report.Updated = append(report.Updated, label)
		}
		report.Skipped = append(report.Skipped, result.Warnings...)
	}
	return report, ctx.Err()
}

func (m *Manager) refreshCommandSource(ctx context.Context, id string, missingOnly bool) (Result, bool, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, false, err
	}
	defer unlock()
	plan, err := m.startupPlan(ctx)
	if err != nil {
		return Result{}, false, err
	}
	if !plan.commandRefreshAllowed() {
		return Result{}, false, errCommandRefreshSkipped
	}
	record, enabled, err := m.commandCandidate(id, plan)
	if err != nil {
		return Result{}, false, err
	}
	if !enabled {
		return Result{}, false, errCommandRefreshSkipped
	}
	info, statErr := os.Stat(record.InstallPath)
	restore := statErr != nil || !info.IsDir()
	if missingOnly && !restore {
		return Result{}, false, errCommandRefreshSkipped
	}
	key := id + "\x00" + record.Scope + "\x00" + record.ProjectPath
	m.commandMu.Lock()
	if !missingOnly && m.commandRefreshed[key] {
		m.commandMu.Unlock()
		return Result{}, false, errCommandRefreshSkipped
	}
	if m.commandRefreshed == nil {
		m.commandRefreshed = map[string]bool{}
	}
	m.commandRefreshed[key] = true
	m.commandMu.Unlock()
	result, err := m.installGraph(ctx, id, InstallOptions{Scope: record.Scope, Update: true, Background: true, KeepEnabledSettings: true, commandRefresh: &record.Source})
	return result, restore, err
}
