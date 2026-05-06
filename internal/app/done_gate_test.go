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

func TestRunDoneGateChecks_RecordsSuccessfulVerificationEvidence(t *testing.T) {
	app := &App{}

	results := app.runDoneGateChecks(context.Background(), []doneGateCheck{
		{
			Name:     "go_vet@.",
			Evidence: "go vet .",
			Run: func(context.Context) (tools.ToolResult, error) {
				return tools.NewSuccessResult("ok"), nil
			},
		},
	}, 0)

	if len(results) != 1 || !results[0].Success {
		t.Fatalf("results = %+v, want one successful result", results)
	}

	got := app.snapshotResponseCommands()
	if len(got) != 1 || got[0] != "go vet ." {
		t.Fatalf("response commands = %v, want [go vet .]", got)
	}
}

func TestBuildDoneGateChecks_VerifyCodeUsesReadableEvidence(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(&doneGateStubTool{name: "verify_code"}); err != nil {
		t.Fatalf("Register(verify_code) error = %v", err)
	}
	app := &App{registry: registry}

	checks := app.buildDoneGateChecks("fix bug", []string{"edit"}, doneGateProfile{}, doneGatePolicy{
		Enabled: true,
		Mode:    "strict",
	})
	if len(checks) == 0 {
		t.Fatal("expected verify_code check")
	}
	if checks[0].Name != "verify_code" {
		t.Fatalf("first check name = %q, want verify_code", checks[0].Name)
	}
	if checks[0].Evidence != "verify code" {
		t.Fatalf("verify_code evidence = %q, want readable label", checks[0].Evidence)
	}
	if got := doneGateCheckDisplayName(checks[0]); got != "verify code" {
		t.Fatalf("display name = %q, want verify code", got)
	}
}

func TestRunDoneGateChecks_DoesNotRecordFailedVerificationEvidence(t *testing.T) {
	app := &App{}

	results := app.runDoneGateChecks(context.Background(), []doneGateCheck{
		{
			Name:     "go_vet@.",
			Evidence: "go vet .",
			Run: func(context.Context) (tools.ToolResult, error) {
				return tools.ToolResult{Success: false, Error: "vet failed"}, nil
			},
		},
	}, 0)

	if len(results) != 1 || results[0].Success {
		t.Fatalf("results = %+v, want one failed result", results)
	}
	if got := app.snapshotResponseCommands(); len(got) != 0 {
		t.Fatalf("failed check should not record verification evidence, got %v", got)
	}
}

func TestDoneGateCheckEvidence_FormatsReadableVerificationLabels(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{name: "go_vet@.", command: "go vet .", want: "go vet"},
		{name: "node_typecheck@web", command: "npm run typecheck", want: "node typecheck web"},
		{name: "run_tests@go@internal/app", want: "run tests go internal/app"},
		{name: "git_diff_check", want: "git diff --check"},
		{name: "verify_code", want: "verify code"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := doneGateCheckEvidence(c.name, c.command); got != c.want {
				t.Fatalf("doneGateCheckEvidence(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestDoneGateCheckDisplayName_PrefersReadableEvidence(t *testing.T) {
	got := doneGateCheckDisplayName(doneGateCheck{
		Name:     "go_vet@internal/app",
		Evidence: "go vet internal/app",
	})
	if got != "go vet internal/app" {
		t.Fatalf("display name = %q, want readable evidence", got)
	}

	got = doneGateCheckDisplayName(doneGateCheck{Name: "git_unmerged_paths"})
	if got != "git unmerged paths" {
		t.Fatalf("fallback display name = %q", got)
	}
}

func TestDoneGateResultSummaryFormatsCounts(t *testing.T) {
	cases := []struct {
		passed int
		failed int
		want   string
	}{
		{passed: 3, failed: 0, want: "3 passed"},
		{passed: 0, failed: 2, want: "2 failed"},
		{passed: 3, failed: 1, want: "3 passed, 1 failed"},
	}

	for _, c := range cases {
		if got := formatDoneGateResultSummary(c.passed, c.failed); got != c.want {
			t.Fatalf("formatDoneGateResultSummary(%d,%d) = %q, want %q", c.passed, c.failed, got, c.want)
		}
	}
}

func TestDoneGateResultDisplayName_FallsBackCleanly(t *testing.T) {
	if got := doneGateResultDisplayName(doneGateResult{DisplayName: "go vet internal/app"}); got != "go vet internal/app" {
		t.Fatalf("display name = %q", got)
	}
	if got := doneGateResultDisplayName(doneGateResult{Name: "git_unmerged_paths"}); got != "git unmerged paths" {
		t.Fatalf("fallback display name = %q", got)
	}
}

func TestFormatFailedDoneGateSummary_ListsFailedChecksWithDetails(t *testing.T) {
	results := []doneGateResult{
		{DisplayName: "go vet internal/app", Success: true},
		{DisplayName: "node test web", Error: "exit status 1\nFAIL web"},
		{DisplayName: "git diff --check", Content: "trailing whitespace in file.go"},
		{DisplayName: "python compile service", Error: "SyntaxError"},
		{DisplayName: "cargo check crate", Error: "borrow checker"},
	}

	got := formatFailedDoneGateSummary(results, 3)
	for _, want := range []string{
		"node test web (exit status 1 FAIL web)",
		"git diff --check (trailing whitespace in file.go)",
		"python compile service (SyntaxError)",
		"+1 more",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "go vet internal/app") {
		t.Fatalf("passed check leaked into failed summary:\n%s", got)
	}
}

func TestBuildDoneGateFixPrompt_UsesDisplayNames(t *testing.T) {
	prompt := buildDoneGateFixPrompt("fix it", []doneGateResult{
		{Name: "go_vet@internal/app", DisplayName: "go vet internal/app", Error: "vet failed"},
	}, 1, 2)

	if !strings.Contains(prompt, "go vet internal/app failed") {
		t.Fatalf("fix prompt missing display name:\n%s", prompt)
	}
	if strings.Contains(prompt, "go_vet@internal/app failed") {
		t.Fatalf("fix prompt leaked raw check name:\n%s", prompt)
	}
}

func TestCountDoneGateResults(t *testing.T) {
	cases := []struct {
		name           string
		results        []doneGateResult
		wantPassed     int
		wantFailed     int
	}{
		{
			name:       "empty",
			results:    nil,
			wantPassed: 0,
			wantFailed: 0,
		},
		{
			name: "all pass",
			results: []doneGateResult{
				{Name: "a", Success: true},
				{Name: "b", Success: true},
				{Name: "c", Success: true},
			},
			wantPassed: 3,
			wantFailed: 0,
		},
		{
			name: "all fail",
			results: []doneGateResult{
				{Name: "a", Success: false},
				{Name: "b", Success: false},
			},
			wantPassed: 0,
			wantFailed: 2,
		},
		{
			name: "mixed",
			results: []doneGateResult{
				{Name: "a", Success: true},
				{Name: "b", Success: false},
				{Name: "c", Success: true},
			},
			wantPassed: 2,
			wantFailed: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			passed, failed := countDoneGateResults(tc.results)
			if passed != tc.wantPassed || failed != tc.wantFailed {
				t.Errorf("countDoneGateResults: got (%d, %d), want (%d, %d)",
					passed, failed, tc.wantPassed, tc.wantFailed)
			}
		})
	}
}

func TestFormatDoneGateResultSummary(t *testing.T) {
	cases := []struct {
		passed, failed int
		want           string
	}{
		{3, 0, "3 passed"},
		{0, 2, "2 failed"},
		{2, 1, "2 passed, 1 failed"},
		{0, 0, "0 passed"}, // edge: no results — falls into "failed == 0" branch
	}
	for _, tc := range cases {
		got := formatDoneGateResultSummary(tc.passed, tc.failed)
		if got != tc.want {
			t.Errorf("formatDoneGateResultSummary(%d, %d) = %q, want %q",
				tc.passed, tc.failed, got, tc.want)
		}
	}
}
