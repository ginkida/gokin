package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/permission"
)

func effectiveSessionRunPolicy(stored, requested *chat.SessionRunPolicy, reset bool) (*chat.SessionRunPolicy, bool, error) {
	effective := chat.CloneRunPolicy(requested)
	denies, err := permission.CanonicalizeTemporaryToolDenies(effective.DeniedRules)
	if err != nil {
		return nil, false, err
	}
	effective.DeniedRules = denies
	if len(denies) == 0 {
		effective.DeniedRules = nil
	}
	requestedRank, err := permissionModeAuthority(effective.PermissionMode)
	if err != nil {
		return nil, false, err
	}
	if stored == nil || reset {
		return effective, false, nil
	}
	storedRank, err := permissionModeAuthority(stored.PermissionMode)
	if err != nil {
		return nil, false, fmt.Errorf("saved session policy: %w", err)
	}
	narrowed := false
	if stored.ToolCeiling != nil {
		if effective.ToolCeiling == nil {
			effective.ToolCeiling = slices.Clone(stored.ToolCeiling)
			narrowed = true
		} else {
			intersection := make([]string, 0, len(effective.ToolCeiling))
			for _, name := range effective.ToolCeiling {
				if slices.Contains(stored.ToolCeiling, name) {
					intersection = append(intersection, name)
				} else {
					narrowed = true
				}
			}
			effective.ToolCeiling = intersection
		}
	}
	storedDenies, err := permission.CanonicalizeTemporaryToolDenies(stored.DeniedRules)
	if err != nil {
		return nil, false, fmt.Errorf("saved session denies: %w", err)
	}
	for _, rule := range storedDenies {
		if !slices.Contains(denies, rule) {
			narrowed = true
		}
	}
	effective.DeniedRules, err = permission.CanonicalizeTemporaryToolDenies(append(storedDenies, denies...))
	if err != nil {
		return nil, false, err
	}
	if len(effective.DeniedRules) == 0 {
		effective.DeniedRules = nil
	}
	// dontAsk and default have the same authority rank. Retain dontAsk on a
	// tie: it cannot gain an interactive approval when a prompt was forbidden.
	if storedRank < requestedRank || storedRank == requestedRank && stored.PermissionMode == "dontAsk" {
		narrowed = narrowed || effective.PermissionMode != stored.PermissionMode
		effective.PermissionMode = stored.PermissionMode
	}
	return effective, narrowed, nil
}

func permissionModeAuthority(mode string) (int, error) {
	switch cliPermissionMode(mode) {
	case cliPermissionPlan:
		return 0, nil
	case cliPermissionDefault, cliPermissionDontAsk:
		return 1, nil
	case cliPermissionAcceptEdits:
		return 2, nil
	case cliPermissionBypass:
		return 3, nil
	default:
		return 0, fmt.Errorf("unknown permission mode %q", mode)
	}
}

func configuredPermissionMode(cfg *config.Config) cliPermissionMode {
	if cfg.Plan.Enabled {
		return cliPermissionPlan
	}
	if !cfg.Permission.Enabled {
		return cliPermissionBypass
	}
	if cfg.Permission.DontAsk {
		return cliPermissionDontAsk
	}
	return cliPermissionDefault
}

func validateRunToolNames(available, requested []string) error {
	for _, name := range requested {
		// Builder may omit these valid runtimes because the saved ceiling
		// excludes them. They must not turn a safe intersection into an error.
		if !slices.Contains(available, name) && name != "repl_exec" && name != "harness" {
			return fmt.Errorf("unknown tool name: %s", name)
		}
	}
	return nil
}

// Select before app construction so optional runtimes and permission consumers
// are initialized with the saved limits. prepareSessionForRun reloads this exact
// identity under its writer lease; the caller rechecks the envelope afterward.
func loadRunPolicySession(exactID string, latest bool, workDir string) (*chat.SessionState, error) {
	if exactID == "" && !latest {
		return nil, nil
	}
	session := chat.NewSession()
	session.SetWorkDir(workDir)
	manager, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
	if err != nil {
		return nil, err
	}
	var state *chat.SessionState
	if latest {
		state, _, err = manager.LoadLatest()
		if err == nil && state == nil {
			err = fmt.Errorf("no session available to continue")
		}
	} else {
		state, _, err = manager.LoadSession(exactID)
	}
	return state, err
}

func noticeRetainedRunPolicy(w io.Writer, stored *chat.SessionRunPolicy) {
	description := "run restrictions"
	if stored.ToolCeiling != nil {
		description = "--tools " + strings.Join(stored.ToolCeiling, ",")
	} else if len(stored.DeniedRules) > 0 {
		description = "--disallowed-tools " + strings.Join(stored.DeniedRules, ",")
	} else {
		description = "--permission-mode " + stored.PermissionMode
	}
	fmt.Fprintf(w, "Notice: resumed session was started with %s; restrictions kept. Use --reset-session-policy to widen.\n", description)
}

func warnScopedGitDenies(w io.Writer, available []string, policy *chat.SessionRunPolicy) {
	for _, rule := range policy.DeniedRules {
		if !strings.HasPrefix(rule, "bash(") || !strings.HasSuffix(rule, ")") {
			continue
		}
		pattern := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rule, "bash("), ")"))
		for _, pair := range []struct{ command, tool string }{
			{"git commit", "git_commit"}, {"git push", "git_pr"},
			{"git checkout -b", "git_branch"}, {"git switch -c", "git_branch"},
			{"git branch", "git_branch"}, {"git add", "git_add"},
		} {
			if !strings.HasPrefix(pattern, pair.command) || !slices.Contains(available, pair.tool) {
				continue
			}
			if policy.ToolCeiling != nil && !slices.Contains(policy.ToolCeiling, pair.tool) {
				continue
			}
			denied := false
			for _, deny := range policy.DeniedRules {
				denied = denied || permission.ToolDenyRuleMatchesName(deny, pair.tool)
			}
			if !denied {
				fmt.Fprintf(w, "Warning: %s does not restrict the %s tool; add --disallowed-tools %s\n", rule, pair.tool, pair.tool)
			}
		}
	}
}
