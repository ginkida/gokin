package tools

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

// TestQualityValidator checks Go test files for common anti-patterns
// that produce tests which always pass but verify nothing.
type TestQualityValidator struct{}

func (v *TestQualityValidator) Name() string { return "test_quality" }

func (v *TestQualityValidator) Matches(filePath string) bool {
	return strings.HasSuffix(filePath, "_test.go")
}

var (
	// Patterns for test function boundaries
	testFuncRe = regexp.MustCompile(`^func\s+(Test\w+|Benchmark\w+)\s*\(`)

	// Patterns for real assertions inside a test
	assertionPatterns = []string{
		"t.Error", "t.Fatal", "t.Fail", "t.Errorf", "t.Fatalf", "t.FailNow",
		"b.Error", "b.Fatal", "b.Fail", "b.Errorf", "b.Fatalf", "b.FailNow",
		"assert.", "require.",
		"if ", // conditional checks (heuristic)
	}

	// HTTP call patterns that need testing.Short() guard
	httpCallRe = regexp.MustCompile(`\bhttp\.(Get|Post|Do|NewRequest|DefaultClient)|net\.Dial|net\.Listen`)

	// Ignored error pattern: _ = err or _ = someFunc(...)
	ignoredErrRe = regexp.MustCompile(`_\s*=\s*err\b`)
)

func (v *TestQualityValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)

	functions := extractTestFunctions(content)

	for _, fn := range functions {
		// Check for ignored errors: _ = err
		for _, loc := range fn.ignoredErrors {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      loc,
				Message:   fn.name + ": error result is discarded with '_ = err' — assert or check it",
			})
		}

		// Check for tests with no assertions (benchmarks excluded)
		if strings.HasPrefix(fn.name, "Test") && !fn.hasAssertions {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      fn.startLine,
				Message:   fn.name + ": test function has no assertions (t.Error, t.Fatal, assert, require, or conditionals)",
			})
		}

		// Check for HTTP calls without testing.Short() guard
		if fn.hasHTTPCalls && !fn.hasShortGuard {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      fn.startLine,
				Message:   fn.name + ": makes real HTTP/network calls without testing.Short() guard — will slow CI and fail offline",
			})
		}
	}

	return warnings
}

type testFuncInfo struct {
	name          string
	startLine     int
	hasAssertions bool
	hasHTTPCalls  bool
	hasShortGuard bool
	ignoredErrors []int // line numbers
}

// extractTestFunctions splits content into test/benchmark functions and analyzes each.
func extractTestFunctions(content []byte) []testFuncInfo {
	var functions []testFuncInfo
	var current *testFuncInfo
	braceDepth := 0

	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Detect new test function
		if testFuncRe.MatchString(trimmed) {
			// Save previous function if any
			if current != nil {
				functions = append(functions, *current)
			}
			name := trimmed
			if idx := strings.Index(name, "("); idx > 0 {
				name = strings.TrimPrefix(name[:idx], "func ")
				name = strings.TrimSpace(name)
			}
			current = &testFuncInfo{
				name:      name,
				startLine: lineNum,
			}
			braceDepth = 0
		}

		if current == nil {
			continue
		}

		// Track brace depth to know when function ends
		braceDepth += strings.Count(line, "{") - strings.Count(line, "}")

		// Check for assertions
		if !current.hasAssertions {
			for _, pat := range assertionPatterns {
				if strings.Contains(line, pat) {
					current.hasAssertions = true
					break
				}
			}
		}

		// Check for HTTP/network calls
		if !current.hasHTTPCalls && httpCallRe.MatchString(line) {
			current.hasHTTPCalls = true
		}

		// Check for testing.Short() guard
		if !current.hasShortGuard && strings.Contains(line, "testing.Short()") {
			current.hasShortGuard = true
		}

		// Check for ignored errors
		if ignoredErrRe.MatchString(line) {
			current.ignoredErrors = append(current.ignoredErrors, lineNum)
		}

		// Function ended
		if braceDepth <= 0 && lineNum > current.startLine {
			functions = append(functions, *current)
			current = nil
		}
	}

	// Don't forget last function if file doesn't end with closing brace on its own line
	if current != nil {
		functions = append(functions, *current)
	}

	return functions
}
