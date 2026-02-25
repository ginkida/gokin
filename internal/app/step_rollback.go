package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/logging"
	"gokin/internal/plan"
)

const (
	stepRollbackMaxFiles = 160
)

type stepRollbackSnapshot struct {
	PlanID    string
	StepID    int
	Strategy  string
	CreatedAt time.Time
	BackupDir string
	Files     map[string]stepRollbackFileState
	Skipped   map[string]string
}

type stepRollbackFileState struct {
	Existed    bool
	WasDir     bool
	Mode       os.FileMode
	BackupFile string
}

func stepRollbackKey(planID string, stepID int) string {
	return strings.TrimSpace(planID) + ":" + fmt.Sprintf("%d", stepID)
}

func (a *App) startStepRollbackSnapshot(approvedPlan *plan.Plan, step *plan.Step) {
	if approvedPlan == nil || step == nil || step.ID <= 0 {
		return
	}

	key := stepRollbackKey(approvedPlan.ID, step.ID)
	snapshot := &stepRollbackSnapshot{
		PlanID:    approvedPlan.ID,
		StepID:    step.ID,
		Strategy:  strings.TrimSpace(step.Rollback),
		CreatedAt: time.Now(),
		Files:     make(map[string]stepRollbackFileState),
		Skipped:   make(map[string]string),
	}

	backupDir, err := os.MkdirTemp("", "gokin-step-rollback-*")
	if err != nil {
		logging.Warn("failed to create step rollback temp dir",
			"plan_id", approvedPlan.ID,
			"step_id", step.ID,
			"error", err)
		return
	}
	snapshot.BackupDir = backupDir

	a.stepRollbackMu.Lock()
	if existing := a.stepRollbackSnapshots[key]; existing != nil {
		if existing.BackupDir != "" {
			_ = os.RemoveAll(existing.BackupDir)
		}
		delete(a.stepRollbackSnapshots, key)
	}
	if a.stepRollbackSnapshots == nil {
		a.stepRollbackSnapshots = make(map[string]*stepRollbackSnapshot)
	}
	a.stepRollbackSnapshots[key] = snapshot
	a.stepRollbackMu.Unlock()

	// Prime snapshot with explicit contract path hints so rollback is not purely
	// dependent on tool argument extraction.
	hints := collectExpectedArtifactHints(step)
	for _, hint := range hints {
		a.captureRollbackPath(approvedPlan.ID, step.ID, hint)
	}
}

func (a *App) commitStepRollbackSnapshot(planID string, stepID int) {
	snapshot := a.detachStepRollbackSnapshot(planID, stepID)
	if snapshot == nil {
		return
	}
	if snapshot.BackupDir != "" {
		_ = os.RemoveAll(snapshot.BackupDir)
	}
}

func (a *App) rollbackStepSnapshot(_ context.Context, approvedPlan *plan.Plan, step *plan.Step) (bool, string) {
	if approvedPlan == nil || step == nil {
		return false, ""
	}
	snapshot := a.detachStepRollbackSnapshot(approvedPlan.ID, step.ID)
	if snapshot == nil {
		return false, "rollback snapshot not found"
	}
	defer func() {
		if snapshot.BackupDir != "" {
			_ = os.RemoveAll(snapshot.BackupDir)
		}
	}()

	if len(snapshot.Files) == 0 {
		if len(snapshot.Skipped) > 0 {
			return false, fmt.Sprintf("rollback snapshot had only skipped paths (%d)", len(snapshot.Skipped))
		}
		return false, "rollback snapshot has no tracked files"
	}

	paths := make([]string, 0, len(snapshot.Files))
	for p := range snapshot.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var restored, removed int
	failures := make([]string, 0)
	for _, absPath := range paths {
		state := snapshot.Files[absPath]
		if state.Existed {
			if state.WasDir {
				if err := os.MkdirAll(absPath, state.Mode.Perm()); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", absPath, err))
					continue
				}
				_ = os.Chmod(absPath, state.Mode.Perm())
				restored++
				continue
			}
			if err := restoreFileFromBackup(absPath, state); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", absPath, err))
				continue
			}
			restored++
			continue
		}

		// File/dir did not exist before this step; remove it if present now.
		if err := os.RemoveAll(absPath); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("%s: %v", absPath, err))
			continue
		}
		removed++
	}

	if len(failures) > 0 {
		logging.Warn("step rollback completed with failures",
			"plan_id", approvedPlan.ID,
			"step_id", step.ID,
			"restored", restored,
			"removed", removed,
			"errors", strings.Join(failures, "; "))
		return false, fmt.Sprintf("rollback partial: restored=%d removed=%d errors=%d", restored, removed, len(failures))
	}

	summary := fmt.Sprintf("rollback applied: restored=%d removed=%d", restored, removed)
	if len(snapshot.Skipped) > 0 {
		summary += fmt.Sprintf(" skipped=%d", len(snapshot.Skipped))
	}
	return true, summary
}

func restoreFileFromBackup(absPath string, state stepRollbackFileState) error {
	if state.BackupFile == "" {
		return fmt.Errorf("missing backup file")
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}

	src, err := os.Open(state.BackupFile)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, state.Mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Chmod(absPath, state.Mode.Perm())
}

func (a *App) captureStepRollbackFromToolArgs(approvedPlan *plan.Plan, stepID int, toolName string, args map[string]any) {
	if approvedPlan == nil || stepID <= 0 {
		return
	}
	for _, candidate := range extractRollbackPathCandidates(toolName, args) {
		a.captureRollbackPath(approvedPlan.ID, stepID, candidate)
	}
}

func (a *App) captureRollbackPath(planID string, stepID int, candidate string) {
	absPath, ok := a.resolvePathInWorkDir(candidate)
	if !ok {
		return
	}

	key := stepRollbackKey(planID, stepID)
	a.stepRollbackMu.Lock()
	snapshot := a.stepRollbackSnapshots[key]
	if snapshot == nil {
		a.stepRollbackMu.Unlock()
		return
	}
	if _, exists := snapshot.Files[absPath]; exists {
		a.stepRollbackMu.Unlock()
		return
	}
	if _, skipped := snapshot.Skipped[absPath]; skipped {
		a.stepRollbackMu.Unlock()
		return
	}
	if len(snapshot.Files) >= stepRollbackMaxFiles {
		snapshot.Skipped[absPath] = "snapshot file limit reached"
		a.stepRollbackMu.Unlock()
		return
	}

	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			snapshot.Files[absPath] = stepRollbackFileState{Existed: false}
			a.stepRollbackMu.Unlock()
			return
		}
		snapshot.Skipped[absPath] = err.Error()
		a.stepRollbackMu.Unlock()
		return
	}

	state := stepRollbackFileState{
		Existed: true,
		WasDir:  info.IsDir(),
		Mode:    info.Mode(),
	}
	if !state.WasDir {
		backupFile := filepath.Join(snapshot.BackupDir, fmt.Sprintf("file_%d.bak", len(snapshot.Files)+1))
		if copyErr := copyFileForRollback(absPath, backupFile); copyErr != nil {
			snapshot.Skipped[absPath] = copyErr.Error()
			a.stepRollbackMu.Unlock()
			return
		}
		state.BackupFile = backupFile
	}

	snapshot.Files[absPath] = state
	a.stepRollbackMu.Unlock()
}

func copyFileForRollback(source, dest string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func (a *App) detachStepRollbackSnapshot(planID string, stepID int) *stepRollbackSnapshot {
	key := stepRollbackKey(planID, stepID)
	a.stepRollbackMu.Lock()
	defer a.stepRollbackMu.Unlock()

	if a.stepRollbackSnapshots == nil {
		return nil
	}
	snapshot := a.stepRollbackSnapshots[key]
	delete(a.stepRollbackSnapshots, key)
	return snapshot
}

func extractRollbackPathCandidates(toolName string, args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}

	candidates := make([]string, 0, 8)
	seen := make(map[string]bool)
	appendCandidate := func(raw string) {
		raw = normalizePathToken(raw)
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		candidates = append(candidates, raw)
	}

	for _, key := range []string{
		"file_path", "path", "source", "destination", "src", "dst",
		"from", "to", "old_path", "new_path", "target", "target_path",
		"files", "file_paths",
	} {
		value, ok := args[key]
		if !ok {
			continue
		}
		for _, candidate := range flattenPathArg(value) {
			appendCandidate(candidate)
		}
	}

	if command, ok := args["command"].(string); ok {
		for _, candidate := range extractCommandPathHints(command) {
			appendCandidate(candidate)
		}
	}

	if strings.EqualFold(toolName, "batch") {
		if p, ok := args["pattern"].(string); ok {
			appendCandidate(p)
		}
	}

	return candidates
}

func flattenPathArg(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func extractCommandPathHints(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}

	hints := make([]string, 0, 4)
	seen := make(map[string]bool)
	for _, part := range parts {
		token := normalizePathToken(part)
		if token == "" || seen[token] {
			continue
		}
		if !looksLikePath(token) {
			continue
		}
		seen[token] = true
		hints = append(hints, token)
	}
	return hints
}

func normalizePathToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.Trim(token, "\"'`()[]{}<>;,")
	token = strings.TrimPrefix(token, "cd ")
	token = strings.TrimPrefix(token, "2>")
	token = strings.TrimPrefix(token, "1>")
	token = strings.TrimPrefix(token, ">")
	token = strings.TrimPrefix(token, "<")
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if strings.Contains(token, "://") || strings.HasPrefix(token, "$") {
		return ""
	}
	if strings.ContainsAny(token, "*?[]{}") {
		return ""
	}
	return token
}

func looksLikePath(token string) bool {
	if token == "" {
		return false
	}
	if token == "." || token == ".." {
		return false
	}
	if strings.HasPrefix(token, "-") {
		return false
	}
	if strings.Contains(token, string(os.PathSeparator)) || strings.Contains(token, "/") || strings.Contains(token, "\\") {
		return true
	}
	if strings.HasPrefix(token, ".") {
		return true
	}
	return false
}

func (a *App) resolvePathInWorkDir(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	root := strings.TrimSpace(a.workDir)
	if root == "" {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rootAbs = filepath.Clean(rootAbs)

	pathValue := raw
	if !filepath.IsAbs(pathValue) {
		pathValue = filepath.Join(rootAbs, pathValue)
	}
	pathAbs, err := filepath.Abs(pathValue)
	if err != nil {
		return "", false
	}
	pathAbs = filepath.Clean(pathAbs)

	if pathAbs == rootAbs || strings.HasPrefix(pathAbs, rootAbs+string(os.PathSeparator)) {
		return pathAbs, true
	}
	return "", false
}
