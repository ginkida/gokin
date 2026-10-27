package app

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"gokin/internal/workspace"
)

var changeRequest = regexp.MustCompile(`(?i)\b(add|edit|change|update|modify|create|remove|delete|insert|implement|fix|replace|rename|write)\b|добавь|добавьте|измени|измените|обнови|исправь|создай|удали|напиши|реализуй`)
var priorWorkRequest = regexp.MustCompile(`(?i)\b(summarize|summary|review|explain|describe)\b|что ты изменил|что вы изменили|расскажи|объясни|подведи итог`)

func requestedFileChange(prompt string) bool {
	return !priorWorkRequest.MatchString(prompt) && changeRequest.MatchString(prompt)
}

// Unknown/partial probes suppress this advisory heuristic.
type claimWorkspaceSnapshot struct {
	known    bool
	repo     bool
	snapshot *workspace.Snapshot
}

func snapshotClaimWorkspace(parent context.Context, root string) *claimWorkspaceSnapshot {
	snapshot := workspace.Capture(parent, root, workspace.DefaultLimits(), nil)
	return &claimWorkspaceSnapshot{known: !snapshot.Partial, repo: snapshot.Repo, snapshot: snapshot}
}

func (s *claimWorkspaceSnapshot) unchanged(root string, paths []string, shellWrite bool) bool {
	if !s.known {
		return false
	}
	current := workspace.CaptureFresh(context.Background(), root, workspace.DefaultLimits(), s.snapshot)
	delta := workspace.Diff(context.Background(), s.snapshot, current)
	if delta.Partial {
		return false
	}
	if shellWrite || len(paths) == 1 && paths[0] == "the requested files" {
		return len(delta.Paths) == 0
	}
	for _, path := range paths {
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(root, full)
		}
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		for _, changed := range delta.Paths {
			if filepath.Clean(full) == changed {
				return false
			}
		}
	}
	return true
}
