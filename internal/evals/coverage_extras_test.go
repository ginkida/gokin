package evals

import (
	"testing"
)

// --- HasBehavioralAssertion / HasPositiveBehavioralAssertion (0%) ---

func TestHasBehavioralAssertion(t *testing.T) {
	if (&Scenario{}).HasBehavioralAssertion() {
		t.Fatal("empty scenario should not have behavioral assertion")
	}
	s := Scenario{AnswerMustContain: []string{"foo"}}
	if !s.HasBehavioralAssertion() {
		t.Fatal("AnswerMustContain should count")
	}
	s = Scenario{FileMustChange: []string{"x.go"}}
	if !s.HasBehavioralAssertion() {
		t.Fatal("FileMustChange should count")
	}
	s = Scenario{FileMustNotChange: []string{"y.go"}}
	if !s.HasBehavioralAssertion() {
		t.Fatal("FileMustNotChange should count")
	}
}

func TestHasPositiveBehavioralAssertion(t *testing.T) {
	if (&Scenario{}).HasPositiveBehavioralAssertion() {
		t.Fatal("empty should not have positive assertion")
	}
	s := Scenario{FileMustNotChange: []string{"y.go"}}
	if s.HasPositiveBehavioralAssertion() {
		t.Fatal("FileMustNotChange alone is not positive")
	}
	s = Scenario{FileMustChange: []string{"x.go"}}
	if !s.HasPositiveBehavioralAssertion() {
		t.Fatal("FileMustChange should be positive")
	}
	s = Scenario{AnswerMustContain: []string{"foo"}}
	if !s.HasPositiveBehavioralAssertion() {
		t.Fatal("AnswerMustContain should be positive")
	}
}

// --- normalizeJournalPath (54.5% → 100%) ---

func TestNormalizeJournalPath(t *testing.T) {
	ws := "/ws"
	cases := []struct {
		path string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{".", ""},
		{"./src/main.go", "src/main.go"},
		{"/ws/src/main.go", "src/main.go"},
		{"src/main.go", "src/main.go"},
	}
	for _, tc := range cases {
		got := normalizeJournalPath(ws, tc.path)
		if got != tc.want {
			t.Errorf("normalizeJournalPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	// Absolute path outside workspace — should return cleaned absolute, not empty
	got := normalizeJournalPath(ws, "/etc/passwd")
	if got == "" {
		t.Fatal("absolute outside-workspace path should not be empty")
	}
}

// --- appendPathValue (50% → 100%) ---

func TestAppendPathValue(t *testing.T) {
	var paths []string

	// String
	appendPathValue(&paths, "/ws", "src/main.go")
	if len(paths) != 1 || paths[0] != "src/main.go" {
		t.Fatalf("string: %v", paths)
	}

	// []any
	paths = nil
	appendPathValue(&paths, "/ws", []any{"a.go", "b.go"})
	if len(paths) != 2 {
		t.Fatalf("[]any: %v", paths)
	}

	// []string
	paths = nil
	appendPathValue(&paths, "/ws", []string{"x.go", "y.go"})
	if len(paths) != 2 {
		t.Fatalf("[]string: %v", paths)
	}

	// non-string (int) — no-op
	paths = nil
	appendPathValue(&paths, "/ws", 42)
	if len(paths) != 0 {
		t.Fatalf("int should add nothing: %v", paths)
	}
}

// --- appendArgPaths (75% → 100%) ---

func TestAppendArgPaths_NilArgs(t *testing.T) {
	var paths []string
	appendArgPaths(&paths, "/ws", nil, "file_path")
	if len(paths) != 0 {
		t.Fatalf("nil args should add nothing: %v", paths)
	}
}

// --- commandLooksLikeVerification (92.3% → 100%) ---

func TestCommandLooksLikeVerification(t *testing.T) {
	if commandLooksLikeVerification("") {
		t.Fatal("empty should not be verification")
	}
	verified := []string{
		"go test", "go vet", "go build", "pytest", "cargo test", "cargo check",
		"npm test", "npm run lint", "pnpm typecheck", "yarn test", "bun test",
		"dotnet test", "mvn test", "gradle test", "swift test", "zig test",
		"make test", "make check", "ruff", "mypy", "tsc", "eslint", "golangci-lint",
	}
	for _, cmd := range verified {
		if !commandLooksLikeVerification(cmd) {
			t.Errorf("commandLooksLikeVerification(%q) = false, want true", cmd)
		}
	}
	// Node test-file patterns
	nodeCases := []string{
		"node --test",
		"node foo.test.js",
		"node bar_test.js",
		"npm run && node x.test.js",
	}
	for _, cmd := range nodeCases {
		if !commandLooksLikeVerification(cmd) {
			t.Errorf("commandLooksLikeVerification(%q) = false, want true", cmd)
		}
	}
	// Non-verification
	if commandLooksLikeVerification("echo hello") {
		t.Error("echo hello should not be verification")
	}
	if commandLooksLikeVerification("cat file.test.js") {
		t.Error("cat file.test.js should not be verification (not a node invocation)")
	}
}

// --- appendUniqueString (85.7% → 100%) ---

func TestAppendUniqueString(t *testing.T) {
	var items []string
	appendUniqueString(&items, "a")
	appendUniqueString(&items, "b")
	appendUniqueString(&items, "a") // dup
	if len(items) != 2 {
		t.Fatalf("after dups: %v, want [a b]", items)
	}
	appendUniqueString(&items, "  ") // whitespace-only
	if len(items) != 2 {
		t.Fatalf("whitespace-only should not add: %v", items)
	}
}

// --- detailString (71.4% → 100%) ---

func TestDetailString(t *testing.T) {
	details := map[string]any{"error": "boom", "count": 42}
	if got := detailString(details, "error"); got != "boom" {
		t.Fatalf("detailString(error) = %q, want boom", got)
	}
	// Non-string value — should skip
	if got := detailString(details, "count"); got != "" {
		t.Fatalf("detailString(count) = %q, want empty (non-string)", got)
	}
	// Missing key
	if got := detailString(details, "missing"); got != "" {
		t.Fatalf("detailString(missing) = %q, want empty", got)
	}
	// nil map
	if got := detailString(nil, "x"); got != "" {
		t.Fatalf("detailString(nil) = %q, want empty", got)
	}
}

// --- sortScenarioIdentities (25% → 100%) ---

func TestSortScenarioIdentities(t *testing.T) {
	ids := []ScenarioIdentity{
		{ID: "c", Variant: "2"},
		{ID: "a", Variant: "1"},
		{ID: "b", Variant: "1"},
		{ID: "a", Variant: "2"},
	}
	sortScenarioIdentities(ids)
	if ids[0].ID != "a" || ids[0].Variant != "1" {
		t.Fatalf("first = %+v, want a/1", ids[0])
	}
	if ids[1].ID != "a" || ids[1].Variant != "2" {
		t.Fatalf("second = %+v, want a/2", ids[1])
	}
	if ids[2].ID != "b" {
		t.Fatalf("third = %+v, want b", ids[2])
	}
	if ids[3].ID != "c" {
		t.Fatalf("fourth = %+v, want c", ids[3])
	}
}

func TestSortScenarioIdentities_Empty(t *testing.T) {
	ids := []ScenarioIdentity{}
	sortScenarioIdentities(ids)
	if len(ids) != 0 {
		t.Fatalf("empty sort should stay empty: %v", ids)
	}
}

// --- ParseMetricThresholds (83.3% → 100%) ---

func TestParseMetricThresholds_Errors(t *testing.T) {
	// No "="
	if _, err := ParseMetricThresholds([]string{"noequals"}); err == nil {
		t.Fatal("missing '=' should error")
	}
	// Empty name
	if _, err := ParseMetricThresholds([]string{"=0.5"}); err == nil {
		t.Fatal("empty name should error")
	}
	// Invalid ratio
	if _, err := ParseMetricThresholds([]string{"metric=abc"}); err == nil {
		t.Fatal("invalid ratio should error")
	}
}

func TestParseMetricThresholds_Nil(t *testing.T) {
	got, err := ParseMetricThresholds(nil)
	if err != nil || got != nil {
		t.Fatalf("nil input = %v, %v; want nil, nil", got, err)
	}
}

func TestParseMetricThresholds_WhitespaceSkip(t *testing.T) {
	got, err := ParseMetricThresholds([]string{"", "  "})
	if err != nil {
		t.Fatalf("whitespace-only input error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("whitespace-only should produce empty map: %v", got)
	}
}

// --- Validate (61.8% → higher) ---

func TestValidate_Errors(t *testing.T) {
	var nilManifest *Manifest
	if err := nilManifest.Validate(); err == nil {
		t.Fatal("nil manifest should error")
	}
	if err := (&Manifest{Version: 0, Name: "x", Metrics: []string{"m"}, Scenarios: []Scenario{{ID: "s"}}}).Validate(); err == nil {
		t.Fatal("zero version should error")
	}
	if err := (&Manifest{Version: 1, Name: "", Metrics: []string{"m"}, Scenarios: []Scenario{{ID: "s"}}}).Validate(); err == nil {
		t.Fatal("empty name should error")
	}
	if err := (&Manifest{Version: 1, Name: "x", Metrics: nil, Scenarios: []Scenario{{ID: "s"}}}).Validate(); err == nil {
		t.Fatal("empty metrics should error")
	}
	if err := (&Manifest{Version: 1, Name: "x", Metrics: []string{"m"}, Scenarios: nil}).Validate(); err == nil {
		t.Fatal("empty scenarios should error")
	}
}

func TestValidate_Valid(t *testing.T) {
	m := &Manifest{
		Version: 1, Name: "test", Metrics: []string{"task_completed"},
		Scenarios: []Scenario{{
			ID:                   "s1",
			Category:             "cat",
			Difficulty:           "easy",
			Prompt:               "p",
			Fixture:              "f",
			ExpectedBehaviors:    []string{"does thing"},
			VerificationCommands: []string{"go test"},
			SuccessCriteria:      []string{"tests pass"},
			FailureSignals:       []string{"panic"},
			MaxToolCalls:         10,
		}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest error: %v", err)
	}
}

// --- recommendationForMetric (33.3% → higher) ---
// Direct call covers switch branches not reachable through DiagnoseReport alone.

func TestRecommendationForMetric_AllBranches(t *testing.T) {
	metricNames := []string{
		"verification_passed",
		"final_answer_mentions_verification",
		"files_read_recorded",
		"files_edited_recorded",
		"verification_recorded",
		"no_false_file_claims",
		"tool_calls_reasonable",
		"touched_files_scoped",
		"journal_present",
		"task_completed",
		"answer_contains_required",
		"required_files_changed",
		"protected_files_unchanged",
		"custom_unknown_metric",
	}
	for _, name := range metricNames {
		rec := recommendationForMetric(MetricSummary{Name: name, Ratio: 0.0})
		if rec.Area == "" {
			t.Errorf("recommendationForMetric(%q) returned empty area", name)
		}
	}
}
