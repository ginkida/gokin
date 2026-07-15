package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	defaultGoDiagnosticsTimeout = 20 * time.Second
	maxGoDiagnosticTargets      = 32
	maxGoDiagnosticsOutputChars = 4000
)

// GoDiagnosticsReport is the provider-neutral result of a Go workspace
// diagnostic pass. Clean distinguishes an honest empty result from transport
// failure; Content contains actionable diagnostics when Clean is false.
type GoDiagnosticsReport struct {
	Content string
	Clean   bool
	Source  string
}

// GoDiagnosticsProvider supplies workspace-aware diagnostics, normally from a
// managed gopls MCP process. Implementations must honor ctx.
type GoDiagnosticsProvider interface {
	Diagnose(ctx context.Context, files []string) (GoDiagnosticsReport, error)
}

type goDiagnosticsOutcome struct {
	report GoDiagnosticsReport
	err    error
}

type goMutationSnapshot struct {
	Paths            []string
	PathsDeclared    bool
	WorkspaceChanged bool
	Changed          bool
	ChangedKnown     bool
}

func snapshotGoMutation(result ToolResult) goMutationSnapshot {
	snapshot := goMutationSnapshot{}
	data, ok := result.Data.(map[string]any)
	if !ok {
		return snapshot
	}
	if changed, ok := data["changed"].(bool); ok {
		snapshot.Changed = changed
		snapshot.ChangedKnown = true
	}
	if workspaceChanged, ok := data["workspace_changed"].(bool); ok {
		snapshot.WorkspaceChanged = workspaceChanged
	}
	if raw, exists := data["written_paths"]; exists {
		switch paths := raw.(type) {
		case []string:
			snapshot.PathsDeclared = true
			for _, path := range paths {
				if strings.TrimSpace(path) != "" {
					snapshot.Paths = append(snapshot.Paths, path)
				}
			}
		case []any:
			snapshot.PathsDeclared = true
			for _, item := range paths {
				if path, ok := item.(string); ok && strings.TrimSpace(path) != "" {
					snapshot.Paths = append(snapshot.Paths, path)
				}
			}
		}
	}
	return snapshot
}

func (e *Executor) runGoDiagnosticsAfterMutation(ctx context.Context, call *genai.FunctionCall, mutation goMutationSnapshot, result *ToolResult) {
	if e == nil || call == nil || result == nil || !result.Success {
		return
	}
	if mutation.ChangedKnown && !mutation.Changed {
		return
	}

	e.goDiagnosticsMu.RLock()
	provider := e.goDiagnosticsProvider
	gate := e.goDiagnosticsGate
	timeout := e.goDiagnosticsTimeout
	e.goDiagnosticsMu.RUnlock()
	if provider == nil || gate == nil {
		return
	}
	if timeout <= 0 {
		timeout = defaultGoDiagnosticsTimeout
	}

	files, relevant := goDiagnosticTargets(e.workDir, call, mutation)
	if !relevant {
		return
	}
	if e.goDiagnosticsStalled.Load() {
		message := e.appendGoDiagnosticMessage(result, "Go diagnostics circuit is open after a timed-out gopls check; this change was not type-checked")
		if message != "" && e.handler != nil && e.handler.OnWarning != nil {
			e.handler.OnWarning(message)
		}
		return
	}

	diagnosticCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Serialize diagnostics for parallel mutations. When a provider ignores
	// cancellation, its goroutine retains the gate until it eventually returns;
	// later mutations time out while waiting instead of spawning an unbounded
	// number of stuck calls.
	select {
	case <-gate:
	case <-diagnosticCtx.Done():
		if ctx.Err() != nil {
			return
		}
		e.goDiagnosticsStalled.Store(true)
		message := e.appendGoDiagnosticMessage(result, "Go diagnostics timed out waiting for a previous gopls check")
		if message != "" && e.handler != nil && e.handler.OnWarning != nil {
			e.handler.OnWarning(message)
		}
		return
	}

	done := make(chan goDiagnosticsOutcome, 1)
	go func() {
		outcome := goDiagnosticsOutcome{}
		defer func() {
			if recovered := recover(); recovered != nil {
				outcome.err = fmt.Errorf("provider panic: %v", recovered)
			}
			done <- outcome
			gate <- struct{}{}
			e.goDiagnosticsMu.RLock()
			sameProviderGate := e.goDiagnosticsGate == gate
			e.goDiagnosticsMu.RUnlock()
			if sameProviderGate {
				e.goDiagnosticsStalled.Store(false)
			}
		}()
		outcome.report, outcome.err = provider.Diagnose(diagnosticCtx, files)
	}()

	var report GoDiagnosticsReport
	var err error
	select {
	case outcome := <-done:
		report, err = outcome.report, outcome.err
	case <-diagnosticCtx.Done():
		if ctx.Err() != nil {
			return
		}
		e.goDiagnosticsStalled.Store(true)
		message := e.appendGoDiagnosticMessage(result, "Go diagnostics timed out; this change was not type-checked by gopls")
		if message != "" && e.handler != nil && e.handler.OnWarning != nil {
			e.handler.OnWarning(message)
		}
		return
	}
	if err != nil {
		message := "Go diagnostics unavailable; this change was not type-checked by gopls"
		if detail := strings.TrimSpace(err.Error()); detail != "" {
			message += ": " + detail
		}
		message = e.appendGoDiagnosticMessage(result, message)
		if message != "" && e.handler != nil && e.handler.OnWarning != nil {
			e.handler.OnWarning(message)
		}
		return
	}
	if report.Clean {
		return
	}

	content := strings.TrimSpace(report.Content)
	if content == "" {
		content = "gopls reported a non-clean workspace without diagnostic details"
	}
	if source := strings.TrimSpace(report.Source); source != "" {
		content = fmt.Sprintf("Go diagnostics (%s):\n%s", source, content)
	} else {
		content = "Go diagnostics:\n" + content
	}
	e.appendGoDiagnosticMessage(result, content)
	if e.handler != nil && e.handler.OnWarning != nil {
		e.handler.OnWarning("Go diagnostics found errors after " + call.Name)
	}
}

func (e *Executor) appendGoDiagnosticMessage(result *ToolResult, message string) string {
	message = trimDeltaOutput(strings.TrimSpace(message), maxGoDiagnosticsOutputChars)
	if message == "" {
		return ""
	}
	if e.redactor != nil {
		message = e.redactor.Redact(message)
		message = trimDeltaOutput(strings.TrimSpace(message), maxGoDiagnosticsOutputChars)
	}
	if result.Content != "" {
		result.Content += "\n\n" + message
	} else {
		result.Content = message
	}
	return message
}

// goDiagnosticTargets returns existing, in-workspace .go paths to mark active
// for deeper diagnostics. relevant remains true for a deleted/moved-away Go
// file, causing a workspace-only diagnostic pass with an empty files slice.
func goDiagnosticTargets(workDir string, call *genai.FunctionCall, mutation goMutationSnapshot) (files []string, relevant bool) {
	root := strings.TrimSpace(workDir)
	if root == "" || call == nil {
		return nil, false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}
	lexicalRoot := filepath.Clean(root)
	realRoot := lexicalRoot
	if resolved, err := filepath.EvalSymlinks(lexicalRoot); err == nil {
		realRoot = resolved
	}
	if mutation.WorkspaceChanged {
		return nil, true
	}

	var rawPaths []string
	if mutation.PathsDeclared {
		rawPaths = append(rawPaths, mutation.Paths...)
	} else {
		rawPaths = append(rawPaths, extractFilePaths(call)...)
	}
	seen := make(map[string]struct{}, len(rawPaths))
	for _, raw := range rawPaths {
		raw = strings.TrimSpace(raw)
		if raw == "" || !strings.EqualFold(filepath.Ext(raw), ".go") {
			continue
		}
		candidate := raw
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(lexicalRoot, candidate)
		}
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			continue
		}
		candidate = filepath.Clean(candidate)

		lexicallyInside := pathWithinGoDiagnosticsRoot(lexicalRoot, candidate)
		canonicalCandidate, canonicalErr := canonicalizeGoDiagnosticPath(candidate)
		reallyInside := canonicalErr == nil && pathWithinGoDiagnosticsRoot(realRoot, canonicalCandidate)
		info, statErr := os.Stat(candidate)
		if statErr != nil || info.IsDir() {
			// A missing in-workspace .go path represents a delete/move and still
			// requires workspace diagnostics. An out-of-workspace missing path is
			// ignored entirely.
			if lexicallyInside || reallyInside {
				relevant = true
			}
			continue
		}
		if canonicalErr != nil {
			if lexicallyInside || reallyInside {
				relevant = true
			}
			continue
		}
		if !pathWithinGoDiagnosticsRoot(realRoot, canonicalCandidate) {
			// Do not send a symlink target outside the foreground workspace. We
			// still run workspace-only diagnostics for an in-root mutation.
			if lexicallyInside {
				relevant = true
			}
			continue
		}
		relevant = true
		if _, ok := seen[canonicalCandidate]; ok {
			continue
		}
		seen[canonicalCandidate] = struct{}{}
		files = append(files, canonicalCandidate)
		if len(files) > maxGoDiagnosticTargets {
			// A full workspace pass is deterministic and safer than checking an
			// arbitrary first page of a large batch mutation.
			return nil, true
		}
	}
	sort.Strings(files)
	return files, relevant
}

func pathWithinGoDiagnosticsRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalizeGoDiagnosticPath resolves symlinks even when the final path was
// deleted or moved away: EvalSymlinks cannot resolve a missing leaf, so walk to
// the nearest existing ancestor and append the unresolved suffix.
func canonicalizeGoDiagnosticPath(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cannot resolve existing ancestor for %s", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
