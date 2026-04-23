package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/tools"

	"google.golang.org/genai"
)

type doneGateStubTool struct {
	name string
}

func (t *doneGateStubTool) Name() string { return t.name }

func (t *doneGateStubTool) Description() string { return "stub tool" }

func (t *doneGateStubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: "stub tool"}
}

func (t *doneGateStubTool) Validate(args map[string]any) error { return nil }

func (t *doneGateStubTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult("ok"), nil
}

func TestDetectDoneGateProfileWithTouchedPaths_PrioritizesCurrentTurnTouches(t *testing.T) {
	dir := t.TempDir()
	serviceA := filepath.Join(dir, "service-a")
	serviceB := filepath.Join(dir, "service-b")

	for _, moduleDir := range []string{serviceA, serviceB} {
		if err := os.MkdirAll(moduleDir, 0755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", moduleDir, err)
		}
		if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/test\n\ngo 1.23\n"), 0644); err != nil {
			t.Fatalf("WriteFile(go.mod) error = %v", err)
		}
	}

	profile := detectDoneGateProfileWithTouchedPaths(dir, []string{
		filepath.Join(serviceB, "internal", "handler.go"),
	})

	if len(profile.GoModules) < 2 {
		t.Fatalf("GoModules = %v, want both modules discovered", profile.GoModules)
	}
	if got := profile.GoModules[0]; got != serviceB {
		t.Fatalf("GoModules[0] = %q, want %q", got, serviceB)
	}
}

func TestShouldRunDoneGateToolTests_SkipsDocsOnlyTouches(t *testing.T) {
	profile := doneGateProfile{
		GoModules:    []string{"/repo"},
		TouchedPaths: []string{"README.md", "docs/usage.md"},
	}

	if shouldRunDoneGateToolTests("update documentation", []string{"edit"}, profile) {
		t.Fatal("shouldRunDoneGateToolTests() = true, want false for docs-only touches")
	}
}

func TestShouldRunDoneGateToolTests_RequiresTestsForCodeTouches(t *testing.T) {
	profile := doneGateProfile{
		GoModules:    []string{"/repo"},
		TouchedPaths: []string{"internal/app/app.go"},
	}

	if !shouldRunDoneGateToolTests("fix runtime bug", []string{"edit"}, profile) {
		t.Fatal("shouldRunDoneGateToolTests() = false, want true for code touches")
	}
}

func TestBuildDoneGateChecks_AddsRunTestsForMultipleTouchedModules(t *testing.T) {
	dir := t.TempDir()
	registry := tools.NewRegistry()
	if err := registry.Register(&doneGateStubTool{name: "run_tests"}); err != nil {
		t.Fatalf("Register(run_tests) error = %v", err)
	}

	app := &App{
		workDir:  dir,
		registry: registry,
	}

	profile := doneGateProfile{
		GoModules: []string{
			filepath.Join(dir, "service-b"),
			filepath.Join(dir, "service-a"),
		},
		TouchedPaths: []string{
			"service-b/internal/handler.go",
			"service-a/internal/store.go",
		},
		Monorepo: true,
	}

	checks := app.buildDoneGateChecks("fix services", []string{"edit"}, profile, doneGatePolicy{
		Enabled: true,
		Mode:    "strict",
	})

	var runTestChecks []string
	for _, check := range checks {
		if strings.HasPrefix(check.Name, "run_tests@") {
			runTestChecks = append(runTestChecks, check.Name)
		}
	}

	if len(runTestChecks) != 2 {
		t.Fatalf("run test checks = %v, want 2 touched-module checks", runTestChecks)
	}
	if runTestChecks[0] != "run_tests@go@service-b" {
		t.Fatalf("runTestChecks[0] = %q, want %q", runTestChecks[0], "run_tests@go@service-b")
	}
	if runTestChecks[1] != "run_tests@go@service-a" {
		t.Fatalf("runTestChecks[1] = %q, want %q", runTestChecks[1], "run_tests@go@service-a")
	}
}

func TestDoneGateRunTestsTargets_GoTargetsTouchedPackageDirs(t *testing.T) {
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "service")
	profile := doneGateProfile{
		WorkDir:   dir,
		GoModules: []string{moduleDir},
		TouchedPaths: []string{
			"service/internal/handler.go",
			"service/internal/store.go",
		},
	}

	targets := doneGateRunTestsTargets(profile)
	if len(targets) != 1 {
		t.Fatalf("targets = %v, want one targeted package", targets)
	}
	want := filepath.Join(moduleDir, "internal")
	if targets[0].Path != want {
		t.Fatalf("target path = %q, want %q", targets[0].Path, want)
	}
	if targets[0].Framework != "go" {
		t.Fatalf("target framework = %q, want go", targets[0].Framework)
	}
}

func TestDoneGateRunTestsTargets_GoModFallsBackToModuleRoot(t *testing.T) {
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "service")
	profile := doneGateProfile{
		WorkDir:      dir,
		GoModules:    []string{moduleDir},
		TouchedPaths: []string{"service/go.mod"},
	}

	targets := doneGateRunTestsTargets(profile)
	if len(targets) != 1 {
		t.Fatalf("targets = %v, want one module-root target", targets)
	}
	if targets[0].Path != moduleDir {
		t.Fatalf("target path = %q, want %q", targets[0].Path, moduleDir)
	}
}

func TestAppRecordResponseTouchedPaths_TracksSuccessfulMutationsOnly(t *testing.T) {
	dir := t.TempDir()
	app := &App{workDir: dir}

	app.recordResponseTouchedPaths("read", map[string]any{
		"file_path": filepath.Join(dir, "README.md"),
	}, tools.ToolResult{Success: true})
	app.recordResponseTouchedPaths("edit", map[string]any{
		"file_path": filepath.Join(dir, "internal", "app.go"),
	}, tools.ToolResult{Success: false})
	app.recordResponseTouchedPaths("edit", map[string]any{
		"file_path": filepath.Join(dir, "internal", "app.go"),
	}, tools.ToolResult{Success: true})
	app.recordResponseTouchedPaths("write", map[string]any{
		"file_path": filepath.Join(dir, "internal", "app.go"),
	}, tools.ToolResult{Success: true})
	app.recordResponseTouchedPaths("move", map[string]any{
		"source":      filepath.Join(dir, "internal", "old.go"),
		"destination": filepath.Join(dir, "internal", "new.go"),
	}, tools.ToolResult{Success: true})

	got := app.snapshotResponseTouchedPaths()
	want := []string{"internal/app.go", "internal/new.go", "internal/old.go"}
	if len(got) != len(want) {
		t.Fatalf("touched paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("touched paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
