package donegate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type doneGateStubTool struct {
	name     string
	lastArgs map[string]any
}

func doneGateCheckNames(checks []Check) []string {
	names := make([]string, len(checks))
	for i, check := range checks {
		names[i] = check.Name
	}
	return names
}

func (t *doneGateStubTool) Name() string { return t.name }

func (t *doneGateStubTool) Description() string { return "stub tool" }

func (t *doneGateStubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: "stub tool"}
}

func (t *doneGateStubTool) Validate(args map[string]any) error { return nil }

func (t *doneGateStubTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	t.lastArgs = args
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

	checks := BuildChecks(registry, "/repo", "fix bug", []string{"edit"}, Profile{
		WorkDir:   "/repo",
		GoModules: []string{"/repo"},
	}, Policy{
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

func TestBuildDoneGateChecks_SkipsVerifyCodeForUnsupportedProjects(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		profile Profile
	}{
		{
			name: "java",
			profile: Profile{
				WorkDir:      dir,
				JavaProjects: []JavaProject{{Dir: dir, Runner: "maven"}},
				TouchedPaths: []string{"src/Main.java"},
			},
		},
		{
			name: "php",
			profile: Profile{
				WorkDir:      dir,
				PHPProjects:  []PHPProject{{Dir: dir}},
				TouchedPaths: []string{"src/App.php"},
			},
		},
		{
			name: "cmake",
			profile: Profile{
				WorkDir:       dir,
				CMakeProjects: []string{dir},
				TouchedPaths:  []string{"src/main.cpp"},
			},
		},
		{
			name: "bazel",
			profile: Profile{
				WorkDir:      dir,
				BazelRoots:   []string{dir},
				TouchedPaths: []string{"BUILD.bazel"},
			},
		},
		{
			name: "make",
			profile: Profile{
				WorkDir:      dir,
				MakeProjects: []string{dir},
				TouchedPaths: []string{"Makefile"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			if err := registry.Register(&doneGateStubTool{name: "verify_code"}); err != nil {
				t.Fatalf("Register(verify_code) error = %v", err)
			}

			checks := BuildChecks(registry, dir, "fix project", []string{"edit"}, tc.profile, Policy{Enabled: true, Mode: "strict"})
			for _, check := range checks {
				if check.Name == "verify_code" {
					t.Fatalf("unsupported %s profile unexpectedly added verify_code", tc.name)
				}
			}
		})
	}
}

func TestBuildDoneGateChecks_VerifyCodeCoversNativeFileInSupportedProject(t *testing.T) {
	dir := t.TempDir()
	verify := &doneGateStubTool{name: "verify_code"}
	registry := tools.NewRegistry()
	if err := registry.Register(verify); err != nil {
		t.Fatal(err)
	}
	checks := BuildChecks(registry, dir, "update cgo bridge", []string{"edit"}, Profile{
		WorkDir:      dir,
		GoModules:    []string{dir},
		MakeProjects: []string{dir}, // Generic build marker at the same depth.
		TouchedPaths: []string{"native/bridge.h"},
	}, Policy{Enabled: true, Mode: "strict"})
	if len(checks) != 1 || checks[0].Name != "verify_code" {
		t.Fatalf("checks = %v, want verify_code for native file under Go root", doneGateCheckNames(checks))
	}
	if _, err := checks[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if verify.lastArgs["path"] != dir {
		t.Fatalf("verify path = %v, want %s", verify.lastArgs["path"], dir)
	}
}

func TestDetectProfile_PreservesAndPrioritizesNestedGoModule(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{filepath.Join(root, "go.mod"), filepath.Join(nested, "go.mod")} {
		if err := os.WriteFile(marker, []byte("module example.com/test\n\ngo 1.24\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	profile := DetectProfileWithTouchedPaths(root, []string{"services/api/main.go"})
	if len(profile.GoModules) < 2 || profile.GoModules[0] != nested {
		t.Fatalf("GoModules = %v, want nested module first and root retained", profile.GoModules)
	}
	if target, ok := doneGateVerifyCodeTarget(root, profile); !ok || target != nested {
		t.Fatalf("verify target = %q/%v, want nested module %q", target, ok, nested)
	}
}

func TestDetectProfile_PreservesNestedSupportedModuleBoundaries(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	markers := map[string]string{
		filepath.Join(root, "Cargo.toml"):       "[package]\nname = \"root\"\nversion = \"0.1.0\"\n",
		filepath.Join(nested, "Cargo.toml"):     "[package]\nname = \"api\"\nversion = \"0.1.0\"\n",
		filepath.Join(root, "package.json"):     "{}\n",
		filepath.Join(nested, "package.json"):   "{}\n",
		filepath.Join(root, "pyproject.toml"):   "[project]\nname = \"root\"\nversion = \"0.1.0\"\n",
		filepath.Join(nested, "pyproject.toml"): "[project]\nname = \"api\"\nversion = \"0.1.0\"\n",
	}
	for path, content := range markers {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	profile := DetectProfileWithTouchedPaths(root, []string{"services/api/main.py"})
	if len(profile.RustModules) < 2 || profile.RustModules[0] != nested {
		t.Fatalf("RustModules = %v, want nested module first and root retained", profile.RustModules)
	}
	if len(profile.PythonRoots) < 2 || profile.PythonRoots[0] != nested {
		t.Fatalf("PythonRoots = %v, want nested module first and root retained", profile.PythonRoots)
	}
	if len(profile.NodeProjects) < 2 || profile.NodeProjects[0].Dir != nested {
		t.Fatalf("NodeProjects = %v, want nested module first and root retained", profile.NodeProjects)
	}
}

func TestDetectProfile_PrunesNestedHierarchicalBuildMarkers(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "subproject")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		filepath.Join(root, "CMakeLists.txt"), filepath.Join(nested, "CMakeLists.txt"),
		filepath.Join(root, "WORKSPACE.bazel"), filepath.Join(nested, "BUILD.bazel"),
		filepath.Join(root, "Makefile"), filepath.Join(nested, "Makefile"),
		filepath.Join(root, "pom.xml"), filepath.Join(nested, "pom.xml"),
	} {
		if err := os.WriteFile(marker, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	profile := DetectProfileWithTouchedPaths(root, []string{"subproject/source.cpp"})
	if len(profile.CMakeProjects) != 1 || profile.CMakeProjects[0] != root {
		t.Fatalf("CMakeProjects = %v, want only hierarchical root", profile.CMakeProjects)
	}
	if len(profile.BazelRoots) != 1 || profile.BazelRoots[0] != root {
		t.Fatalf("BazelRoots = %v, want only hierarchical root", profile.BazelRoots)
	}
	if len(profile.MakeProjects) != 1 || profile.MakeProjects[0] != root {
		t.Fatalf("MakeProjects = %v, want only hierarchical root", profile.MakeProjects)
	}
	if len(profile.JavaProjects) != 1 || profile.JavaProjects[0].Dir != root {
		t.Fatalf("JavaProjects = %v, want only hierarchical root", profile.JavaProjects)
	}
}

func TestBuildDoneGateChecks_MixedMonorepoSkipsUnrelatedSupportedStack(t *testing.T) {
	dir := t.TempDir()
	javaDir := filepath.Join(dir, "services", "java-api")
	registry := tools.NewRegistry()
	if err := registry.Register(&doneGateStubTool{name: "verify_code"}); err != nil {
		t.Fatalf("Register(verify_code) error = %v", err)
	}

	profile := Profile{
		WorkDir:      dir,
		GoModules:    []string{dir}, // A root module contains every workspace path.
		JavaProjects: []JavaProject{{Dir: javaDir, Runner: "maven"}},
		TouchedPaths: []string{"services/java-api/src/Main.java"},
		Monorepo:     true,
	}
	checks := BuildChecks(registry, dir, "fix java service", []string{"edit"}, profile, Policy{Enabled: true, Mode: "strict"})
	for _, check := range checks {
		if check.Name == "verify_code" {
			t.Fatal("Java-only touch selected the unrelated root Go module for verify_code")
		}
	}
}

func TestBuildDoneGateChecks_MixedMonorepoTargetsTouchedSupportedStack(t *testing.T) {
	dir := t.TempDir()
	nodeDir := filepath.Join(dir, "web")
	verify := &doneGateStubTool{name: "verify_code"}
	registry := tools.NewRegistry()
	if err := registry.Register(verify); err != nil {
		t.Fatalf("Register(verify_code) error = %v", err)
	}

	profile := Profile{
		WorkDir:   dir,
		GoModules: []string{dir},
		NodeProjects: []NodeProject{{
			Dir:    nodeDir,
			Runner: "npm",
		}},
		TouchedPaths: []string{"web/src/app.ts"},
		Monorepo:     true,
	}
	checks := BuildChecks(registry, dir, "fix web app", []string{"edit"}, profile, Policy{Enabled: true, Mode: "strict"})
	if len(checks) != 1 || checks[0].Name != "verify_code" {
		t.Fatalf("checks = %v, want one verify_code check", checkNames(checks))
	}
	if _, err := checks[0].Run(context.Background()); err != nil {
		t.Fatalf("verify_code check error = %v", err)
	}
	if got, _ := verify.lastArgs["path"].(string); got != nodeDir {
		t.Fatalf("verify_code path = %q, want touched Node project %q", got, nodeDir)
	}
}

func checkNames(checks []Check) []string {
	names := make([]string, 0, len(checks))
	for _, check := range checks {
		names = append(names, check.Name)
	}
	return names
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

// TestNewBashCheckWithDir_DoesNotMoveSessionCwd pins the v0.100.73 #3 fix: a
// done-gate check anchored to a subdirectory runs through the model's LIVE
// shared BashTool, whose pwd probe commits the final cwd as the persistent
// session cwd. A bare `cd subdir && cmd` permanently moved the model's cwd into
// the checked subpackage — even when the check FAILED — so the model's next-turn
// relative commands silently ran in the wrong directory. The subshell wrap makes
// the cd invisible to the top-level pwd probe.
func TestNewBashCheckWithDir_DoesNotMoveSessionCwd(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	sub := filepath.Join(work, "internal", "foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	bash := tools.NewBashTool(work)

	// A SUCCEEDING check in the subdirectory: this is the path that reliably
	// commits cwd — the bash tool appends a pwd probe after the command, so a
	// bare `cd subdir && ok` reports the subdir as the new session cwd. (A
	// FAILING check exits the script before the probe, so success is the
	// guaranteed-reproduction case.)
	check := newBashCheckWithDir(bash, "subcheck", sub, "echo checked")
	if _, err := check.Run(context.Background()); err != nil {
		t.Fatalf("check run error: %v", err)
	}

	// The model's session cwd must still be the workspace root — probe it the
	// same way the bug does (a subsequent `pwd` through the same tool).
	pwdRes, err := bash.Execute(context.Background(), map[string]any{
		"command":     "pwd",
		"description": "probe cwd",
	})
	if err != nil {
		t.Fatalf("pwd probe error: %v", err)
	}
	got := strings.TrimSpace(pwdRes.Content)
	// Resolve symlinks on both sides (macOS /var -> /private/var).
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	wantRoot := work
	if resolved, rerr := filepath.EvalSymlinks(work); rerr == nil {
		wantRoot = resolved
	}
	if got != wantRoot {
		t.Fatalf("session cwd moved to %q after a subdir check; want workspace root %q", got, wantRoot)
	}
}

// TestDetectProfile_BareBuildFileNotBazel pins the v0.100.73 #4 marker
// hardening: a plain-text file literally named BUILD (or WORKSPACE) with no
// workspace-level bazel marker at the root must NOT register a BazelRoot —
// otherwise a stray text file forces a permanently-failing `bazel build` that
// blocks every mutating turn (default enabled+strict+fail-closed policy).
func TestDetectProfile_BareBuildFileNotBazel(t *testing.T) {
	dir := t.TempDir()
	// A README-style BUILD file, no WORKSPACE/MODULE.bazel at the root.
	if err := os.WriteFile(filepath.Join(dir, "BUILD"), []byte("build instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := DetectProfile(dir)
	if len(profile.BazelRoots) != 0 {
		t.Fatalf("a bare BUILD file with no workspace marker registered BazelRoots=%v", profile.BazelRoots)
	}

	// With a real workspace marker at the root, the same BUILD file DOES count.
	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte("module(name='x')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile = DetectProfile(dir)
	if len(profile.BazelRoots) == 0 {
		t.Fatal("with a MODULE.bazel at root, the BUILD file should register a BazelRoot")
	}
}

// TestBuildChecks_BazelSkipsWhenToolAbsent: even when a bazel root is genuinely
// registered, the check must be a no-op skip (exit 0, actionable message) when
// bazel isn't installed — never a hard failure that blocks the gate.
func TestBuildChecks_BazelSkipsWhenToolAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte("module(name='x')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "BUILD"), []byte("# bazel build file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int main(){return 0;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	profile := DetectProfileWithTouchedPaths(dir, []string{filepath.Join(dir, "main.c")})
	if len(profile.BazelRoots) == 0 {
		t.Skip("bazel root not detected in this environment; skip probe assertion")
	}

	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewBashTool(dir))
	checks := BuildChecks(registry, dir, "edit build", []string{"edit"}, profile, Policy{
		Enabled: true, Mode: "strict",
	})
	var bazelCheck *Check
	for i := range checks {
		if strings.HasPrefix(checks[i].Name, "bazel_nobuild") {
			bazelCheck = &checks[i]
			break
		}
	}
	if bazelCheck == nil {
		t.Fatal("expected a bazel_nobuild check for the touched bazel root")
	}
	// Run it: with bazel absent (the common host state) the existence probe
	// must skip cleanly (exit 0), NOT hard-fail. If bazel happens to be
	// installed here, running the real command against a stub workspace is not
	// a deterministic assertion — skip in that case.
	res, err := bazelCheck.Run(context.Background())
	if err != nil {
		t.Fatalf("bazel check run error: %v", err)
	}
	if strings.Contains(res.Content, "bazel not found — skipping") {
		if !res.Success {
			t.Fatalf("absent-bazel skip must report Success, got failure: %q", res.Content)
		}
		return
	}
	t.Skip("bazel appears installed in this environment; skip the absent-tool assertion")
}
