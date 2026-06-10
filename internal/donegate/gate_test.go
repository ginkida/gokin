package donegate

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

	profile := DetectProfileWithTouchedPaths(dir, []string{
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
	profile := Profile{
		GoModules:    []string{"/repo"},
		TouchedPaths: []string{"README.md", "docs/usage.md"},
	}

	if shouldRunDoneGateToolTests("update documentation", []string{"edit"}, profile) {
		t.Fatal("shouldRunDoneGateToolTests() = true, want false for docs-only touches")
	}
}

func TestShouldRunDoneGateToolTests_RequiresTestsForCodeTouches(t *testing.T) {
	profile := Profile{
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

	profile := Profile{
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

	checks := BuildChecks(registry, dir, "fix services", []string{"edit"}, profile, Policy{
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
	profile := Profile{
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
	profile := Profile{
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

func TestBuildDoneGateChecks_VerifyCodeUsesReadableEvidence(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(&doneGateStubTool{name: "verify_code"}); err != nil {
		t.Fatalf("Register(verify_code) error = %v", err)
	}

	checks := BuildChecks(registry, "", "fix bug", []string{"edit"}, Profile{}, Policy{
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
	got := doneGateCheckDisplayName(Check{
		Name:     "go_vet@internal/app",
		Evidence: "go vet internal/app",
	})
	if got != "go vet internal/app" {
		t.Fatalf("display name = %q, want readable evidence", got)
	}

	got = doneGateCheckDisplayName(Check{Name: "git_unmerged_paths"})
	if got != "git unmerged paths" {
		t.Fatalf("fallback display name = %q", got)
	}
}

func TestDoneGateResultDisplayName_FallsBackCleanly(t *testing.T) {
	if got := ResultDisplayName(Result{DisplayName: "go vet internal/app"}); got != "go vet internal/app" {
		t.Fatalf("display name = %q", got)
	}
	if got := ResultDisplayName(Result{Name: "git_unmerged_paths"}); got != "git unmerged paths" {
		t.Fatalf("fallback display name = %q", got)
	}
}

func TestFormatFailedDoneGateSummary_ListsFailedChecksWithDetails(t *testing.T) {
	results := []Result{
		{DisplayName: "go vet internal/app", Success: true},
		{DisplayName: "node test web", Error: "exit status 1\nFAIL web"},
		{DisplayName: "git diff --check", Content: "trailing whitespace in file.go"},
		{DisplayName: "python compile service", Error: "SyntaxError"},
		{DisplayName: "cargo check crate", Error: "borrow checker"},
	}

	got := FormatFailedSummary(results, 3)
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
	prompt := BuildFixPrompt("fix it", []Result{
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
		name       string
		results    []Result
		wantPassed int
		wantFailed int
	}{
		{
			name:       "empty",
			results:    nil,
			wantPassed: 0,
			wantFailed: 0,
		},
		{
			name: "all pass",
			results: []Result{
				{Name: "a", Success: true},
				{Name: "b", Success: true},
				{Name: "c", Success: true},
			},
			wantPassed: 3,
			wantFailed: 0,
		},
		{
			name: "all fail",
			results: []Result{
				{Name: "a", Success: false},
				{Name: "b", Success: false},
			},
			wantPassed: 0,
			wantFailed: 2,
		},
		{
			name: "mixed",
			results: []Result{
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
			passed, failed := CountResults(tc.results)
			if passed != tc.wantPassed || failed != tc.wantFailed {
				t.Errorf("CountResults: got (%d, %d), want (%d, %d)",
					passed, failed, tc.wantPassed, tc.wantFailed)
			}
		})
	}
}
