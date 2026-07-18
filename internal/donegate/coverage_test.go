package donegate

import (
	"os"
	"path/filepath"
	"testing"
)

// ===========================================================================
// pickNodeScript / nodeScriptAliases (0% → full)
// ===========================================================================

func TestPickNodeScript_EmptyMap(t *testing.T) {
	_, _, ok := pickNodeScript(map[string]string{}, "lint")
	if ok {
		t.Error("pickNodeScript with empty map should return false")
	}
}

func TestPickNodeScript_NilMap(t *testing.T) {
	_, _, ok := pickNodeScript(nil, "lint")
	if ok {
		t.Error("pickNodeScript with nil map should return false")
	}
}

func TestPickNodeScript_FoundLint(t *testing.T) {
	scripts := map[string]string{"lint": "eslint ."}
	name, script, ok := pickNodeScript(scripts, "lint")
	if !ok {
		t.Fatal("pickNodeScript should find 'lint'")
	}
	if name != "lint" {
		t.Errorf("name = %q, want 'lint'", name)
	}
	if script != "eslint ." {
		t.Errorf("script = %q, want 'eslint .'", script)
	}
}

func TestPickNodeScript_FoundAlias(t *testing.T) {
	scripts := map[string]string{"lint:ci": "eslint . --ci"}
	_, script, ok := pickNodeScript(scripts, "lint")
	if !ok {
		t.Fatal("pickNodeScript should find 'lint:ci' alias")
	}
	if script != "eslint . --ci" {
		t.Errorf("script = %q, want 'eslint . --ci'", script)
	}
}

func TestPickNodeScript_EmptyScriptValueSkipped(t *testing.T) {
	scripts := map[string]string{
		"lint":    "",
		"lint:ci": "  ",
		"eslint":  "eslint .",
	}
	_, script, ok := pickNodeScript(scripts, "lint")
	if !ok {
		t.Fatal("pickNodeScript should skip empty values and find 'eslint'")
	}
	if script != "eslint ." {
		t.Errorf("script = %q, want 'eslint .'", script)
	}
}

func TestPickNodeScript_NotFound(t *testing.T) {
	scripts := map[string]string{"build": "webpack"}
	_, _, ok := pickNodeScript(scripts, "lint")
	if ok {
		t.Error("pickNodeScript should not find 'lint' in build-only scripts")
	}
}

func TestCoverage_NodeScriptAliases(t *testing.T) {
	cases := []struct {
		category string
		wantLen  int
		mustHave string
	}{
		{"lint", 5, "lint"},
		{"typecheck", 6, "typecheck"},
		{"test", 5, "test"},
		{"build", 4, "build"},
		{"custom", 1, "custom"},
	}
	for _, tc := range cases {
		got := nodeScriptAliases(tc.category)
		if len(got) != tc.wantLen {
			t.Errorf("nodeScriptAliases(%q) len = %d, want %d", tc.category, len(got), tc.wantLen)
		}
		found := false
		for _, a := range got {
			if a == tc.mustHave {
				found = true
			}
		}
		if !found {
			t.Errorf("nodeScriptAliases(%q) should contain %q, got %v", tc.category, tc.mustHave, got)
		}
	}
}

// ===========================================================================
// pickComposerScript / composerScriptAliases (0% → full)
// ===========================================================================

func TestPickComposerScript_EmptyMap(t *testing.T) {
	_, _, ok := pickComposerScript(map[string]string{}, "lint")
	if ok {
		t.Error("pickComposerScript with empty map should return false")
	}
}

func TestPickComposerScript_NilMap(t *testing.T) {
	_, _, ok := pickComposerScript(nil, "lint")
	if ok {
		t.Error("pickComposerScript with nil map should return false")
	}
}

func TestPickComposerScript_FoundLint(t *testing.T) {
	scripts := map[string]string{"lint": "phpcs --standard=PSR12 src"}
	name, script, ok := pickComposerScript(scripts, "lint")
	if !ok {
		t.Fatal("pickComposerScript should find 'lint'")
	}
	if name != "lint" {
		t.Errorf("name = %q, want 'lint'", name)
	}
	if script != "phpcs --standard=PSR12 src" {
		t.Errorf("script = %q", script)
	}
}

func TestPickComposerScript_FoundStaticAlias(t *testing.T) {
	scripts := map[string]string{"phpstan": "phpstan analyse"}
	_, script, ok := pickComposerScript(scripts, "static")
	if !ok {
		t.Fatal("pickComposerScript should find 'phpstan' alias for 'static'")
	}
	if script != "phpstan analyse" {
		t.Errorf("script = %q", script)
	}
}

func TestPickComposerScript_EmptyValueSkipped(t *testing.T) {
	scripts := map[string]string{
		"lint":  "",
		"phpcs": "phpcs src",
	}
	_, script, ok := pickComposerScript(scripts, "lint")
	if !ok {
		t.Fatal("pickComposerScript should skip empty and find 'phpcs'")
	}
	if script != "phpcs src" {
		t.Errorf("script = %q", script)
	}
}

func TestPickComposerScript_NotFound(t *testing.T) {
	scripts := map[string]string{"test": "phpunit"}
	_, _, ok := pickComposerScript(scripts, "lint")
	if ok {
		t.Error("pickComposerScript should not find 'lint'")
	}
}

func TestCoverage_ComposerScriptAliases(t *testing.T) {
	cases := []struct {
		category string
		wantLen  int
		mustHave string
	}{
		{"lint", 5, "lint"},
		{"static", 6, "phpstan"},
		{"custom", 1, "custom"},
	}
	for _, tc := range cases {
		got := composerScriptAliases(tc.category)
		if len(got) != tc.wantLen {
			t.Errorf("composerScriptAliases(%q) len = %d, want %d", tc.category, len(got), tc.wantLen)
		}
		found := false
		for _, a := range got {
			if a == tc.mustHave {
				found = true
			}
		}
		if !found {
			t.Errorf("composerScriptAliases(%q) should contain %q, got %v", tc.category, tc.mustHave, got)
		}
	}
}

// ===========================================================================
// nodeRunScriptCommand (0% → full)
// ===========================================================================

func TestNodeRunScriptCommand(t *testing.T) {
	cases := []struct {
		runner string
		script string
		want   string
	}{
		{"npm", "lint", "npm run -s 'lint'"},
		{"pnpm", "test", "pnpm -s run 'test'"},
		{"yarn", "build", "yarn -s run 'build'"},
		{"bun", "dev", "bun run 'dev'"},
		{"NPM", "lint", "npm run -s 'lint'"},       // case-insensitive
		{"  pnpm  ", "test", "pnpm -s run 'test'"}, // trimmed
		{"unknown", "x", "npm run -s 'x'"},         // default → npm
		{"", "x", "npm run -s 'x'"},                // empty → npm
	}
	for _, tc := range cases {
		got := nodeRunScriptCommand(tc.runner, tc.script)
		if got != tc.want {
			t.Errorf("nodeRunScriptCommand(%q, %q) = %q, want %q", tc.runner, tc.script, got, tc.want)
		}
	}
}

// ===========================================================================
// isNoopNodeTestScript (indirectly tested, add explicit)
// ===========================================================================

func TestCoverage_IsNoopNodeTestScript(t *testing.T) {
	noop := []string{
		"",
		"  ",
		"no test specified",
		"echo \"Error: no test specified\"",
		"echo 'Error: no test specified'",
		"echo \"no tests\"",
		"echo 'no tests'",
		"echo no tests",
		"exit 1",
	}
	for _, s := range noop {
		if !isNoopNodeTestScript(s) {
			t.Errorf("isNoopNodeTestScript(%q) = false, want true", s)
		}
	}

	real := []string{
		"jest",
		"mocha test/",
		"vitest run",
	}
	for _, s := range real {
		if isNoopNodeTestScript(s) {
			t.Errorf("isNoopNodeTestScript(%q) = true, want false", s)
		}
	}
}

// ===========================================================================
// dirExists / fileExists (indirectly tested, add explicit)
// ===========================================================================

// ===========================================================================
// readFileHead (indirectly tested, add explicit)
// ===========================================================================

func TestCoverage_ReadFileHead_Nonexistent(t *testing.T) {
	got := readFileHead("nonexistent-xyz-123.go", 100)
	if got != "" {
		t.Errorf("readFileHead on nonexistent should return empty, got %q", got)
	}
}

// ===========================================================================
// signals.go — CommandsContainVerificationSignals (42.9% → full)
// ===========================================================================

func TestCommandsContainVerificationSignals_Empty(t *testing.T) {
	if CommandsContainVerificationSignals(nil) {
		t.Error("nil commands should return false")
	}
	if CommandsContainVerificationSignals([]string{}) {
		t.Error("empty commands should return false")
	}
}

func TestCommandsContainVerificationSignals_NoMatch(t *testing.T) {
	cmds := []string{"echo hello", "cat file.txt", "ls -la"}
	if CommandsContainVerificationSignals(cmds) {
		t.Error("non-verification commands should return false")
	}
}

func TestCommandsContainVerificationSignals_Matches(t *testing.T) {
	// Only keywords that appear in the keyword list of signals.go match.
	// "tsc" alone doesn't match — it needs "typecheck" or "check".
	cases := []string{
		"go test ./...",
		"pytest",
		"cargo test",
		"npm test",
		"pnpm test",
		"yarn test",
		"bun test",
		"npm run check",
		"go vet ./...",
		"go build ./...",
		"make compile",
		"make verify",
		"make validate",
		"npm run typecheck",
	}
	for _, cmd := range cases {
		if !CommandsContainVerificationSignals([]string{cmd}) {
			t.Errorf("CommandsContainVerificationSignals(%q) = false, want true", cmd)
		}
	}
}

func TestCommandsContainVerificationSignals_Mixed(t *testing.T) {
	// One matching command in a list of non-matching should return true.
	cmds := []string{"echo hello", "ls -la", "go test ./..."}
	if !CommandsContainVerificationSignals(cmds) {
		t.Error("mixed commands with one match should return true")
	}
}

// ===========================================================================
// signals.go — OutputContainsVerificationSignals (75% → full)
// ===========================================================================

func TestOutputContainsVerificationSignals_Empty(t *testing.T) {
	if OutputContainsVerificationSignals("") {
		t.Error("empty output should return false")
	}
	if OutputContainsVerificationSignals("   ") {
		t.Error("whitespace-only output should return false")
	}
}

func TestOutputContainsVerificationSignals_PositiveEnglish(t *testing.T) {
	cases := []string{
		"all tests passed",
		"tests passed",
		"build succeeded",
		"successful",
		"verified",
		"no issues found",
		"no errors",
		"lint passed",
		"check passed",
	}
	for _, out := range cases {
		if !OutputContainsVerificationSignals(out) {
			t.Errorf("OutputContainsVerificationSignals(%q) = false, want true", out)
		}
	}
}

func TestOutputContainsVerificationSignals_PositiveRussian(t *testing.T) {
	cases := []string{
		"тесты успешно пройдены",
		"код проверен",
		"проверка пройдена",
		"без ошибок",
	}
	for _, out := range cases {
		if !OutputContainsVerificationSignals(out) {
			t.Errorf("OutputContainsVerificationSignals(%q) = false, want true", out)
		}
	}
}

func TestOutputContainsVerificationSignals_NegativeSignals(t *testing.T) {
	cases := []string{
		"test failed",
		"build failure",
		"panic: runtime error",
		"traceback (most recent call last)",
		"AssertionError: expected 5, got 3",
	}
	for _, out := range cases {
		if OutputContainsVerificationSignals(out) {
			t.Errorf("OutputContainsVerificationSignals(%q) = true, want false", out)
		}
	}
}

func TestOutputContainsVerificationSignals_NeutralNoMatch(t *testing.T) {
	if OutputContainsVerificationSignals("running compilation...") {
		t.Error("neutral output without signals should return false")
	}
}

func TestOutputContainsVerificationSignals_PositiveOverridesNegative(t *testing.T) {
	// "all tests passed" should win even if "failed" appears in test name.
	out := "test_error_handling failed — all tests passed"
	if !OutputContainsVerificationSignals(out) {
		t.Error("positive signal should override negative in output")
	}
}

// ===========================================================================
// review.go — filepathBase (50% → full)
// ===========================================================================

func TestFilepathBase(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"  ", ""},
		{"file.txt", "file.txt"},
		{"path/to/file.go", "file.go"},
		{"/absolute/path/file.py", "file.py"},
		{`windows\path\file.js`, "file.js"},
		{"trailing/", ""},
	}
	for _, tc := range cases {
		got := filepathBase(tc.input)
		if got != tc.want {
			t.Errorf("filepathBase(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ===========================================================================
// review.go — ShouldRunCompletionReview (60% → full)
// ===========================================================================

func TestShouldRunCompletionReview_NoTouchedPaths(t *testing.T) {
	if ShouldRunCompletionReview("fix bug", "done", []string{"edit"}, nil, nil) {
		t.Error("ShouldRunCompletionReview with no touched paths should be false")
	}
}

func TestShouldRunCompletionReview_NonCodingNonCodePaths(t *testing.T) {
	// Non-coding task + non-code paths → false.
	if ShouldRunCompletionReview("hello", "hi", []string{"edit"}, []string{"data.json"}, nil) {
		t.Error("non-coding task with non-code paths should be false")
	}
}

// ===========================================================================
// review.go — completionReviewHasDiffProof (62.5% → full)
// ===========================================================================

func TestCompletionReviewHasDiffProof_GitDiffCommand(t *testing.T) {
	if !completionReviewHasDiffProof(nil, []string{"git diff HEAD"}) {
		t.Error("git diff command should count as diff proof")
	}
}

func TestCompletionReviewHasDiffProof_GitStatusCommand(t *testing.T) {
	if !completionReviewHasDiffProof(nil, []string{"git status"}) {
		t.Error("git status command should count as diff proof")
	}
}

func TestCompletionReviewHasDiffProof_GitShowCommand(t *testing.T) {
	if !completionReviewHasDiffProof(nil, []string{"git show HEAD"}) {
		t.Error("git show command should count as diff proof")
	}
}

func TestCompletionReviewHasDiffProof_NoProof(t *testing.T) {
	if completionReviewHasDiffProof([]string{"edit"}, []string{"echo hello"}) {
		t.Error("no diff-related tools or commands should return false")
	}
}

// ===========================================================================
// review.go — completionReviewHasVerificationProof (83.3% → full)
// ===========================================================================

func TestCompletionReviewHasVerificationProof_VerifyCodeTool(t *testing.T) {
	if !completionReviewHasVerificationProof([]string{"verify_code"}, nil, "") {
		t.Error("verify_code tool should count as verification proof")
	}
}

func TestCompletionReviewHasVerificationProof_RunTestsTool(t *testing.T) {
	if !completionReviewHasVerificationProof([]string{"run_tests"}, nil, "") {
		t.Error("run_tests tool should count as verification proof")
	}
}

func TestCompletionReviewHasVerificationProof_CommandSignal(t *testing.T) {
	if !completionReviewHasVerificationProof(nil, []string{"go test ./..."}, "") {
		t.Error("go test command should count as verification proof")
	}
}

func TestCompletionReviewHasVerificationProof_OutputSignal(t *testing.T) {
	if !completionReviewHasVerificationProof(nil, nil, "all tests passed") {
		t.Error("positive output should count as verification proof")
	}
}

func TestCompletionReviewHasVerificationProof_NoProof(t *testing.T) {
	if completionReviewHasVerificationProof([]string{"edit"}, []string{"echo hi"}, "done") {
		t.Error("no verification signals should return false")
	}
}

// ===========================================================================
// review.go — completionResponseMentionsTouchedPaths (78.6% → full)
// ===========================================================================

func TestCompletionResponseMentionsTouchedPaths_EmptyResponse(t *testing.T) {
	if completionResponseMentionsTouchedPaths("", []string{"file.go"}) {
		t.Error("empty response should return false")
	}
}

func TestCompletionResponseMentionsTouchedPaths_EmptyPaths(t *testing.T) {
	if completionResponseMentionsTouchedPaths("some response", nil) {
		t.Error("nil touched paths should return false")
	}
}

func TestCompletionResponseMentionsTouchedPaths_ExactMatch(t *testing.T) {
	resp := "Updated internal/handler.go with new logic"
	if !completionResponseMentionsTouchedPaths(resp, []string{"internal/handler.go"}) {
		t.Error("exact path match should return true")
	}
}

func TestCompletionResponseMentionsTouchedPaths_BasenameMatch(t *testing.T) {
	resp := "Fixed the bug in handler.go"
	if !completionResponseMentionsTouchedPaths(resp, []string{"internal/handler.go"}) {
		t.Error("basename match should return true")
	}
}

func TestCompletionResponseMentionsTouchedPaths_NoMatch(t *testing.T) {
	resp := "Updated the implementation."
	if completionResponseMentionsTouchedPaths(resp, []string{"internal/handler.go"}) {
		t.Error("no match should return false")
	}
}

func TestCompletionResponseMentionsTouchedPaths_ShortBasenameIgnored(t *testing.T) {
	// Basenames <= 3 chars are too short to match reliably.
	resp := "Updated a.c"
	if completionResponseMentionsTouchedPaths(resp, []string{"path/a.c"}) {
		t.Error("short basename (<=3 chars) should not match")
	}
}

// ===========================================================================
// loadPHPProjects / readComposerScripts (25% → full)
// ===========================================================================

func TestLoadPHPProjects_Empty(t *testing.T) {
	if got := loadPHPProjects(nil); got != nil {
		t.Errorf("loadPHPProjects(nil) = %v, want nil", got)
	}
	if got := loadPHPProjects([]string{}); got != nil {
		t.Errorf("loadPHPProjects(empty) = %v, want nil", got)
	}
}

func TestLoadPHPProjects_SortsByDir(t *testing.T) {
	base := t.TempDir()
	dirB := filepath.Join(base, "b")
	dirA := filepath.Join(base, "a")
	for _, d := range []string{dirB, dirA} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projects := loadPHPProjects([]string{dirB, dirA})
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if projects[0].Dir != dirA || projects[1].Dir != dirB {
		t.Errorf("projects not sorted by Dir: %q, %q", projects[0].Dir, projects[1].Dir)
	}
	// No composer.json → empty script map.
	if len(projects[0].Scripts) != 0 || len(projects[1].Scripts) != 0 {
		t.Errorf("expected empty Scripts without composer.json, got %v / %v",
			projects[0].Scripts, projects[1].Scripts)
	}
}

func TestLoadPHPProjects_ReadsComposerScripts(t *testing.T) {
	dir := t.TempDir()
	composer := `{
		"scripts": {
			"Test": "phpunit",
			" lint ": "phpcs .",
			"   ": "ignored-empty-name",
			"watch": ["ignored", "non-string"]
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0o644); err != nil {
		t.Fatal(err)
	}
	projects := loadPHPProjects([]string{dir})
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	scripts := projects[0].Scripts
	if len(scripts) != 2 {
		t.Fatalf("got %d scripts, want 2: %v", len(scripts), scripts)
	}
	if scripts["test"] != "phpunit" {
		t.Errorf("scripts[test] = %q, want %q", scripts["test"], "phpunit")
	}
	if scripts["lint"] != "phpcs ." {
		t.Errorf("scripts[lint] = %q, want %q", scripts["lint"], "phpcs .")
	}
	if _, ok := scripts["watch"]; ok {
		t.Error("non-string script value should be skipped")
	}
}

func TestLoadPHPProjects_InvalidComposerJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	projects := loadPHPProjects([]string{dir})
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	if len(projects[0].Scripts) != 0 {
		t.Errorf("invalid composer.json should yield empty Scripts, got %v", projects[0].Scripts)
	}
}
