package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type recordingGoDiagnosticsProvider struct {
	files  []string
	report GoDiagnosticsReport
	err    error
	calls  int
}

type blockingGoDiagnosticsProvider struct {
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (p *blockingGoDiagnosticsProvider) Diagnose(context.Context, []string) (GoDiagnosticsReport, error) {
	close(p.started)
	<-p.release // deliberately ignore context to verify the executor's hard bound
	close(p.returned)
	return GoDiagnosticsReport{Clean: true}, nil
}

func (p *recordingGoDiagnosticsProvider) Diagnose(_ context.Context, files []string) (GoDiagnosticsReport, error) {
	p.calls++
	p.files = append([]string(nil), files...)
	return p.report, p.err
}

func TestGoDiagnosticsAfterSuccessfulGoMutation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectedFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingGoDiagnosticsProvider{report: GoDiagnosticsReport{
		Source:  "gopls_mcp",
		Content: "main.go:1:1: expected declaration",
	}}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)
	result := ToolResult{Success: true, Content: "written"}

	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "write",
		Args: map[string]any{"file_path": "main.go"},
	}, goMutationSnapshot{}, &result)

	if provider.calls != 1 || len(provider.files) != 1 || provider.files[0] != expectedFile {
		t.Fatalf("diagnostics calls=%d files=%v, want one call for %q", provider.calls, provider.files, expectedFile)
	}
	if !strings.Contains(result.Content, "Go diagnostics (gopls_mcp)") || !strings.Contains(result.Content, "expected declaration") {
		t.Fatalf("diagnostic was not returned to tool loop: %q", result.Content)
	}
}

func TestGoDiagnosticsDeletionRunsWorkspacePass(t *testing.T) {
	root := t.TempDir()
	provider := &recordingGoDiagnosticsProvider{report: GoDiagnosticsReport{Clean: true}}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)
	result := ToolResult{Success: true, Content: "deleted"}

	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "delete",
		Args: map[string]any{"file_path": "removed.go"},
	}, goMutationSnapshot{}, &result)

	if provider.calls != 1 || len(provider.files) != 0 {
		t.Fatalf("deleted Go file should trigger workspace diagnostics; calls=%d files=%v", provider.calls, provider.files)
	}
	if result.Content != "deleted" {
		t.Fatalf("clean diagnostics should stay silent, got %q", result.Content)
	}
}

func TestGoDiagnosticsUsesDeclaredBatchWritesAndBoundsScope(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "changed.go")
	outside := filepath.Join(t.TempDir(), "outside.go")
	for _, file := range []string{inside, outside} {
		if err := os.WriteFile(file, []byte("package p\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expectedInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingGoDiagnosticsProvider{report: GoDiagnosticsReport{Clean: true}}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)
	result := ToolResult{Success: true, Data: map[string]any{
		"written_paths": []any{inside, outside, filepath.Join(root, "note.txt")},
	}}

	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{Name: "batch"}, goMutationSnapshot{
		Paths:         []string{inside, outside, filepath.Join(root, "note.txt")},
		PathsDeclared: true,
	}, &result)

	if provider.calls != 1 || len(provider.files) != 1 || provider.files[0] != expectedInside {
		t.Fatalf("diagnostics escaped foreground workspace or missed declared write: calls=%d files=%v", provider.calls, provider.files)
	}
}

func TestGoDiagnosticsFailureIsExplicit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &recordingGoDiagnosticsProvider{err: errors.New("transport closed")}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)
	result := ToolResult{Success: true}

	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "edit", Args: map[string]any{"file_path": file},
	}, goMutationSnapshot{}, &result)

	if !strings.Contains(result.Content, "not type-checked by gopls") || !strings.Contains(result.Content, "transport closed") {
		t.Fatalf("provider failure was hidden: %q", result.Content)
	}
}

func TestGoDiagnosticsSkipsNonGoAndFailedMutation(t *testing.T) {
	root := t.TempDir()
	provider := &recordingGoDiagnosticsProvider{}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)

	for _, call := range []*genai.FunctionCall{
		{Name: "write", Args: map[string]any{"file_path": "README.md"}},
		{Name: "mkdir", Args: map[string]any{"path": "pkg"}},
	} {
		result := ToolResult{Success: true}
		executor.runGoDiagnosticsAfterMutation(context.Background(), call, goMutationSnapshot{}, &result)
	}
	if provider.calls != 0 {
		t.Fatalf("irrelevant mutations triggered diagnostics %d times", provider.calls)
	}

	failed := ToolResult{Success: false, Error: "write failed"}
	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "write", Args: map[string]any{"file_path": "failed.go"},
	}, goMutationSnapshot{}, &failed)
	if provider.calls != 0 {
		t.Fatalf("failed mutation triggered diagnostics %d times", provider.calls)
	}
}

func TestGoDiagnosticsHardTimeoutBoundsIgnoringProvider(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &blockingGoDiagnosticsProvider{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)
	executor.goDiagnosticsMu.Lock()
	executor.goDiagnosticsTimeout = 25 * time.Millisecond
	executor.goDiagnosticsMu.Unlock()
	result := ToolResult{Success: true}

	startedAt := time.Now()
	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "edit", Args: map[string]any{"file_path": file},
	}, goMutationSnapshot{}, &result)
	elapsed := time.Since(startedAt)
	if elapsed > time.Second {
		t.Fatalf("context-ignoring diagnostics blocked the turn for %s", elapsed)
	}
	select {
	case <-provider.started:
	default:
		t.Fatal("provider was never called")
	}
	if !strings.Contains(result.Content, "timed out") {
		t.Fatalf("timeout was hidden from tool loop: %q", result.Content)
	}

	// The still-blocked call owns the provider gate. A later mutation must see
	// the open circuit immediately instead of paying the full timeout again.
	second := ToolResult{Success: true}
	secondStartedAt := time.Now()
	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "write", Args: map[string]any{"file_path": file},
	}, goMutationSnapshot{}, &second)
	if secondElapsed := time.Since(secondStartedAt); secondElapsed > 100*time.Millisecond {
		t.Fatalf("open diagnostics circuit delayed later mutation for %s", secondElapsed)
	}
	if !strings.Contains(second.Content, "circuit is open") {
		t.Fatalf("open diagnostics circuit was hidden: %q", second.Content)
	}

	close(provider.release)
	select {
	case <-provider.returned:
	case <-time.After(time.Second):
		t.Fatal("blocking provider did not unwind after release")
	}
}

func TestGoDiagnosticTargetsPreferAuthoritativeMutationMetadata(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.go")
	if err := os.WriteFile(source, []byte("package source\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A copy from Go to non-Go content changed only destination.txt. Looking at
	// generic call args would incorrectly diagnose the unchanged source.go.
	files, relevant := goDiagnosticTargets(root, &genai.FunctionCall{
		Name: "copy",
		Args: map[string]any{"source": source, "destination": filepath.Join(root, "destination.txt")},
	}, goMutationSnapshot{PathsDeclared: true, Paths: []string{filepath.Join(root, "destination.txt")}})
	if relevant || len(files) != 0 {
		t.Fatalf("authoritative non-Go destination triggered diagnostics: relevant=%v files=%v", relevant, files)
	}

	// An explicit empty path list is an authoritative no-op (batch/refactor
	// matched nothing), not permission to fall back to broad call arguments.
	files, relevant = goDiagnosticTargets(root, &genai.FunctionCall{
		Name: "refactor", Args: map[string]any{"file_path": source},
	}, goMutationSnapshot{PathsDeclared: true})
	if relevant || len(files) != 0 {
		t.Fatalf("authoritative no-op triggered diagnostics: relevant=%v files=%v", relevant, files)
	}

	files, relevant = goDiagnosticTargets(root, &genai.FunctionCall{Name: "move"}, goMutationSnapshot{WorkspaceChanged: true})
	if !relevant || len(files) != 0 {
		t.Fatalf("directory mutation should request workspace pass: relevant=%v files=%v", relevant, files)
	}
}

func TestGoDiagnosticTargetsLargeBatchUsesWorkspacePass(t *testing.T) {
	root := t.TempDir()
	paths := make([]string, 0, maxGoDiagnosticTargets+1)
	for i := 0; i <= maxGoDiagnosticTargets; i++ {
		path := filepath.Join(root, fmt.Sprintf("file_%02d.go", i))
		if err := os.WriteFile(path, []byte("package batch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	files, relevant := goDiagnosticTargets(root, &genai.FunctionCall{Name: "batch"}, goMutationSnapshot{
		Paths: paths, PathsDeclared: true,
	})
	if !relevant || len(files) != 0 {
		t.Fatalf("large batch should use full workspace pass: relevant=%v files=%d", relevant, len(files))
	}
}

func TestGoDiagnosticTargetsCanonicalizesSymlinkWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkedRoot := filepath.Join(linkParent, "workspace")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Absolute real paths remain in-scope even when the configured workspace
	// was reached through a symlink. The missing path models a deleted Go file.
	missingRealPath := filepath.Join(realRoot, "removed.go")
	files, relevant := goDiagnosticTargets(linkedRoot, &genai.FunctionCall{
		Name: "delete", Args: map[string]any{"file_path": missingRealPath},
	}, goMutationSnapshot{})
	if !relevant || len(files) != 0 {
		t.Fatalf("canonical deleted path was skipped: relevant=%v files=%v", relevant, files)
	}

	// Conversely, an in-root symlink to an out-of-root Go file is never sent as
	// an active-file argument. A workspace-only pass is safe.
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(linkedRoot, "escape.go")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	files, relevant = goDiagnosticTargets(linkedRoot, &genai.FunctionCall{
		Name: "write", Args: map[string]any{"file_path": escape},
	}, goMutationSnapshot{})
	if !relevant || len(files) != 0 {
		t.Fatalf("symlink escape reached active diagnostics: relevant=%v files=%v", relevant, files)
	}
}

func TestGoDiagnosticsProviderErrorWarningIsRedactedAndBounded(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "sk-" + "abcdefghijklmnopqrstuvwxyz123456" // split so the literal is never key-shaped (Push Protection rule)
	provider := &recordingGoDiagnosticsProvider{err: errors.New("api_key=" + secret + strings.Repeat("x", maxGoDiagnosticsOutputChars*2))}
	executor := NewExecutor(NewRegistry(), nil, 0)
	executor.SetWorkDir(root)
	executor.SetGoDiagnosticsProvider(provider)
	var warning string
	executor.SetHandler(&ExecutionHandler{OnWarning: func(message string) { warning = message }})
	result := ToolResult{Success: true}

	executor.runGoDiagnosticsAfterMutation(context.Background(), &genai.FunctionCall{
		Name: "write", Args: map[string]any{"file_path": file},
	}, goMutationSnapshot{}, &result)

	for label, value := range map[string]string{"result": result.Content, "warning": warning} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s leaked provider secret: %q", label, value)
		}
		if len(value) > maxGoDiagnosticsOutputChars+64 {
			t.Fatalf("%s was not bounded: len=%d", label, len(value))
		}
	}
}
