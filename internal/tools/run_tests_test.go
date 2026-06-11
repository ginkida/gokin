package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goTestJSON renders go-test JSON events as line-delimited output, matching what
// `go test -json` produces and what parseGoTestResults consumes.
func goTestJSON(t *testing.T, events []goTestEvent) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestParseGoTestResultsPreservesMultiLineAssertion pins the regression fix:
// the continuation lines of a multi-line assertion (testify's expected/actual)
// must survive in the rendered report (they carry the failure reason but do not
// contain a file:line token).
func TestParseGoTestResultsPreservesMultiLineAssertion(t *testing.T) {
	pkg := "example.com/m"
	out := goTestJSON(t, []goTestEvent{
		{Action: "run", Package: pkg, Test: "TestEqual"},
		{Action: "output", Package: pkg, Test: "TestEqual", Output: "=== RUN   TestEqual\n"},
		{Action: "output", Package: pkg, Test: "TestEqual", Output: "    main_test.go:12: \n"},
		{Action: "output", Package: pkg, Test: "TestEqual", Output: "        \tError Trace:\tmain_test.go:12\n"},
		{Action: "output", Package: pkg, Test: "TestEqual", Output: "        \tError:      \tNot equal: \n"},
		{Action: "output", Package: pkg, Test: "TestEqual", Output: "        \t            \texpected: 10\n"},
		{Action: "output", Package: pkg, Test: "TestEqual", Output: "        \t            \tactual  : 5\n"},
		{Action: "fail", Package: pkg, Test: "TestEqual"},
		{Action: "fail", Package: pkg},
	})
	report := parseGoTestResults(out, nil, time.Second)
	for _, want := range []string{"expected: 10", "actual  : 5"} {
		if !strings.Contains(report, want) {
			t.Errorf("report dropped %q:\n%s", want, report)
		}
	}
	if !strings.Contains(report, "Failure locations:") || !strings.Contains(report, "main_test.go:12") {
		t.Errorf("expected assertion site in failure-location summary:\n%s", report)
	}
}

// TestParseGoTestResultsPreservesPanicMessage pins the regression fix: a panic's
// "panic:" message (which has no file:line token) must reach the report, and
// stack-frame file:line tokens must NOT be counted as failure locations.
func TestParseGoTestResultsPreservesPanicMessage(t *testing.T) {
	pkg := "example.com/m"
	out := goTestJSON(t, []goTestEvent{
		{Action: "output", Package: pkg, Test: "TestPanics", Output: "=== RUN   TestPanics\n"},
		{Action: "output", Package: pkg, Test: "TestPanics", Output: "panic: runtime error: index out of range [5] with length 2\n"},
		{Action: "output", Package: pkg, Test: "TestPanics", Output: "goroutine 18 [running]:\n"},
		{Action: "output", Package: pkg, Test: "TestPanics", Output: "\t/repo/panic_test.go:7 +0x1d\n"},
		{Action: "output", Package: pkg, Test: "TestPanics", Output: "\t/usr/local/go/src/testing/testing.go:1690 +0xf4\n"},
		{Action: "fail", Package: pkg, Test: "TestPanics"},
		{Action: "fail", Package: pkg},
	})
	report := parseGoTestResults(out, nil, time.Second)
	if !strings.Contains(report, "panic: runtime error: index out of range") {
		t.Errorf("panic message dropped from report:\n%s", report)
	}
	// Stack-frame tokens (no trailing colon) must not appear as 📍 locations.
	if strings.Contains(report, "📍 ") && (strings.Contains(report, "testing.go:1690 (") || strings.Contains(report, "panic_test.go:7 (")) {
		t.Errorf("stack frame miscounted as failure location:\n%s", report)
	}
}

// TestParseGoTestResultsSortsFailedPackages pins deterministic package ordering.
func TestParseGoTestResultsSortsFailedPackages(t *testing.T) {
	out := goTestJSON(t, []goTestEvent{
		{Action: "output", Package: "z/pkg", Test: "T1", Output: "z_test.go:1: boom\n"},
		{Action: "fail", Package: "z/pkg", Test: "T1"},
		{Action: "fail", Package: "z/pkg"},
		{Action: "output", Package: "a/pkg", Test: "T2", Output: "a_test.go:1: boom\n"},
		{Action: "fail", Package: "a/pkg", Test: "T2"},
		{Action: "fail", Package: "a/pkg"},
	})
	report := parseGoTestResults(out, nil, time.Second)
	if !strings.Contains(report, "Failed packages: a/pkg, z/pkg") {
		t.Errorf("expected alphabetically sorted packages, got:\n%s", report)
	}
}

func TestRunTestsPathCanBeGoTestFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/run-tests-file\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(pkgDir, "foo_test.go")
	if err := os.WriteFile(testFile, []byte(`package pkg

import "testing"

func TestFoo(t *testing.T) {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := NewRunTestsTool(root).Execute(context.Background(), map[string]any{
		"path":      filepath.Join("pkg", "foo_test.go"),
		"framework": "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("run_tests returned failure: %s\n%s", result.Error, result.Content)
	}
	if !strings.Contains(result.Content, "PASS") {
		t.Fatalf("expected PASS output, got: %s", result.Content)
	}
}

// TestParseGoTestResultsKeepsTrailingAssertion pins the head+tail fix: go-test
// appends the assertion at the END of a test's output, so a test that logs >20
// progress lines before failing must still show its got/want in the report
// (the head-only cap elided exactly the failure reason).
func TestParseGoTestResultsKeepsTrailingAssertion(t *testing.T) {
	pkg := "example.com/m"
	events := []goTestEvent{
		{Action: "run", Package: pkg, Test: "TestChatty"},
	}
	for i := 0; i < 30; i++ {
		events = append(events, goTestEvent{Action: "output", Package: pkg, Test: "TestChatty",
			Output: fmt.Sprintf("    progress step %02d\n", i)})
	}
	events = append(events,
		goTestEvent{Action: "output", Package: pkg, Test: "TestChatty", Output: "    chatty_test.go:99: got 5, want 10\n"},
		goTestEvent{Action: "fail", Package: pkg, Test: "TestChatty"},
		goTestEvent{Action: "fail", Package: pkg},
	)
	report := parseGoTestResults(goTestJSON(t, events), nil, time.Second)
	if !strings.Contains(report, "got 5, want 10") {
		t.Errorf("trailing assertion dropped from report:\n%s", report)
	}
	if !strings.Contains(report, "progress step 00") {
		t.Errorf("head of output should be preserved too:\n%s", report)
	}
	if !strings.Contains(report, "middle lines elided") {
		t.Errorf("expected middle-elision marker:\n%s", report)
	}
}
