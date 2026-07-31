package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gokin/internal/security"

	"google.golang.org/genai"
)

const (
	// DefaultRunTestsTimeout is intentionally longer than the generic tool
	// timeout: full workspaces commonly compile before running thousands of
	// tests. The executor gives this tool matching outer-context headroom.
	DefaultRunTestsTimeout = 10 * time.Minute
	MaxRunTestsTimeout     = 30 * time.Minute
)

// RunTestsTool runs project tests and parses results.
type RunTestsTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

// NewRunTestsTool creates a new RunTestsTool instance.
func NewRunTestsTool(workDir string) *RunTestsTool {
	workDir = canonicalToolWorkDir(workDir)
	return &RunTestsTool{
		workDir:       workDir,
		pathValidator: newWorkspacePathValidator(workDir, nil),
	}
}

// SetAllowedDirs adds explicitly granted directories to the execution scope.
// run_tests executes project-controlled code, so its path boundary must match
// read/write tools rather than trusting a model-supplied ../ path.
func (t *RunTestsTool) SetAllowedDirs(dirs []string) {
	t.pathValidator = newWorkspacePathValidator(t.workDir, dirs)
}

func (t *RunTestsTool) Name() string { return "run_tests" }

func (t *RunTestsTool) Description() string {
	return "Runs project tests with automatic framework detection (Go, Python, Node, Rust). Parses output, reports failures with context, and provides coverage summary."
}

func (t *RunTestsTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {
					Type:        genai.TypeString,
					Description: "Path to run tests in (default: working directory). Can be a specific file or package.",
				},
				"filter": {
					Type:        genai.TypeString,
					Description: "Test name filter/pattern (e.g., 'TestMyFunc' for Go, '-k test_name' for pytest)",
				},
				"verbose": {
					Type:        genai.TypeBoolean,
					Description: "Show verbose test output (default: false)",
				},
				"coverage": {
					Type:        genai.TypeBoolean,
					Description: "Run with coverage reporting (default: false)",
				},
				"framework": {
					Type:        genai.TypeString,
					Description: "Force specific framework: 'go', 'pytest', 'jest', 'cargo', 'auto' (default: auto-detect)",
					Enum:        []string{"auto", "go", "pytest", "jest", "cargo"},
				},
				"timeout_seconds": {
					Type:        genai.TypeInteger,
					Description: "Maximum test runtime in seconds (default: 600, max: 1800)",
				},
			},
		},
	}
}

func (t *RunTestsTool) Validate(args map[string]any) error {
	for _, key := range []string{"path", "filter", "framework"} {
		if value, present := args[key]; present {
			if _, ok := value.(string); !ok {
				return NewValidationError(key, "must be a string")
			}
		}
	}
	for _, key := range []string{"verbose", "coverage"} {
		if value, present := args[key]; present {
			if _, ok := value.(bool); !ok {
				return NewValidationError(key, "must be a boolean")
			}
		}
	}
	if _, present := args["timeout_seconds"]; present {
		seconds, ok := GetInt(args, "timeout_seconds")
		if !ok {
			return NewValidationError("timeout_seconds", "must be an integer")
		}
		if seconds < 1 || seconds > int(MaxRunTestsTimeout/time.Second) {
			return NewValidationError("timeout_seconds", fmt.Sprintf("must be between 1 and %d", int(MaxRunTestsTimeout/time.Second)))
		}
	}
	framework := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "framework", "auto")))
	if framework == "" {
		framework = "auto"
	}
	switch framework {
	case "auto", "go", "pytest", "jest", "cargo":
		return nil
	default:
		return NewValidationError("framework", "must be one of: auto, go, pytest, jest, cargo")
	}
}

func (t *RunTestsTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	// Executor calls Validate before Execute, but keeping this defensive check
	// prevents direct/internal callers from turning malformed arguments into a
	// real process launch (or the historical false PASS for an unknown runner).
	if err := t.Validate(args); err != nil {
		return NewErrorResult(fmt.Sprintf("run_tests invalid arguments: %v", err)), nil
	}

	testPath := GetStringDefault(args, "path", "")
	filter := GetStringDefault(args, "filter", "")
	verbose := GetBoolDefault(args, "verbose", false)
	coverage := GetBoolDefault(args, "coverage", false)
	framework := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "framework", "auto")))
	if framework == "" {
		framework = "auto"
	}

	pathCandidate := testPath
	if pathCandidate == "" {
		pathCandidate = "."
	}
	validatedPath, err := validateWorkspacePath(t.workDir, pathCandidate, t.pathValidator)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("run_tests rejected path: %v", err)), nil
	}
	info, err := os.Stat(validatedPath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("run_tests cannot access path: %v", err)), nil
	}
	workDir := validatedPath
	if !info.IsDir() {
		workDir = filepath.Dir(validatedPath)
	}

	// Auto-detect framework
	if framework == "auto" {
		framework = detectTestFrameworkInScope(workDir, 10, t.pathValidator)
		if framework == "" {
			return NewErrorResult("could not detect test framework. Specify 'framework' parameter."), nil
		}
	}

	// Build command
	cmdName, cmdArgs := buildTestCommand(framework, workDir, filter, verbose, coverage)
	if cmdName == "" {
		return NewErrorResult(fmt.Sprintf("run_tests has no command for framework %q", framework)), nil
	}

	// Execute with a workload-sized timeout. The executor independently gives
	// run_tests a slightly larger outer budget so this inner deadline can
	// return a classified partial result instead of being cut off first.
	timeout := DefaultRunTestsTimeout
	if seconds, ok := GetInt(args, "timeout_seconds"); ok {
		timeout = time.Duration(seconds) * time.Second
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(testCtx, cmdName, cmdArgs...)
	cmd.Dir = workDir
	// Timeout must kill the whole group: `npm test`/`go test` spawn children
	// that a leader-only SIGKILL orphans forever (the orphaned-`yes` class).
	KillProcessGroupOnCancel(cmd)

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	outStr := string(output)

	if err != nil {
		switch testCtx.Err() {
		case context.DeadlineExceeded:
			return interruptedTestResult(
				fmt.Sprintf("tests timed out before completion (%s)", framework),
				framework, outStr, err, duration,
			), nil
		case context.Canceled:
			return interruptedTestResult(
				fmt.Sprintf("tests cancelled before completion (%s)", framework),
				framework, outStr, err, duration,
			), nil
		}
		var lookupErr *exec.Error
		var pathErr *os.PathError
		if errors.As(err, &lookupErr) || errors.As(err, &pathErr) {
			return NewErrorResult(fmt.Sprintf("test runner could not start (%s): %v", framework, err)), nil
		}
		result := parseTestResults(framework, outStr, err, duration)
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tests failed (%s)", framework),
			Content: result,
		}, nil
	}

	// Parse results based on framework.
	result := parseTestResults(framework, outStr, nil, duration)
	return NewSuccessResult(result), nil
}

func interruptedTestResult(message, framework, output string, execErr error, duration time.Duration) ToolResult {
	result := NewErrorResult(message)
	if strings.TrimSpace(output) != "" {
		result.Content = "Partial output before interruption:\n" +
			parseTestResults(framework, output, execErr, duration)
	}
	return result
}

// detectTestFrameworkInScope is the execution-path variant of framework
// detection. It may walk from a requested package directory to its module
// root, but stops at the workspace/grant boundary instead of inspecting a
// containing sibling project.
func detectTestFrameworkInScope(dir string, depth int, validator *security.PathValidator) string {
	if depth <= 0 || validator == nil {
		return ""
	}
	validatedDir, err := validator.Validate(dir)
	if err != nil {
		return ""
	}

	checks := []struct {
		file      string
		framework string
	}{
		{"go.mod", "go"},
		{"Cargo.toml", "cargo"},
		{"package.json", "jest"},
		{"pytest.ini", "pytest"},
		{"setup.py", "pytest"},
		{"pyproject.toml", "pytest"},
		{"requirements.txt", "pytest"},
	}
	for _, check := range checks {
		if _, err := os.Stat(filepath.Join(validatedDir, check.file)); err == nil {
			return check.framework
		}
	}

	parent := filepath.Dir(validatedDir)
	if parent == validatedDir {
		return ""
	}
	return detectTestFrameworkInScope(parent, depth-1, validator)
}

// buildTestCommand creates the test command for the given framework.
func buildTestCommand(framework, _ string, filter string, verbose, coverage bool) (string, []string) {
	switch framework {
	case "go":
		args := []string{"test"}
		if verbose {
			args = append(args, "-v")
		}
		if coverage {
			args = append(args, "-coverprofile=coverage.out")
		}
		args = append(args, "-json")
		if filter != "" {
			args = append(args, "-run", filter)
		}
		args = append(args, "./...")
		return "go", args

	case "pytest":
		args := []string{"-m", "pytest"}
		if verbose {
			args = append(args, "-v")
		}
		if coverage {
			args = append(args, "--cov", "--cov-report=term-missing")
		}
		if filter != "" {
			args = append(args, "-k", filter)
		}
		args = append(args, "--tb=short", "--no-header", "-q")
		return "python3", args

	case "jest":
		args := []string{"test"}
		if verbose {
			args = append(args, "--verbose")
		}
		if coverage {
			args = append(args, "--coverage")
		}
		if filter != "" {
			args = append(args, "--testNamePattern", filter)
		}
		args = append(args, "--forceExit", "--no-color")
		// Use npx if npm test not available
		return "npx", append([]string{"jest"}, args[1:]...)

	case "cargo":
		// --workspace is valid for a standalone package too and ensures a
		// virtual Cargo workspace doesn't silently test only default-members.
		args := []string{"test", "--workspace"}
		if !verbose {
			args = append(args, "--quiet")
		}
		if filter != "" {
			args = append(args, filter)
		}
		// Do not force libtest's unstable `--format json`: stable Rust rejects
		// it, turning a healthy suite into a tool-induced failure.
		return "cargo", args

	default:
		return "", nil
	}
}

// goTestEvent represents a single Go test JSON event.
type goTestEvent struct {
	Time    string  `json:"Time"`
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

// assertionLineRe matches the Go testing assertion format at the start of a
// (trimmed) output line: "file.go:line:". Anchoring at the start + requiring the
// trailing colon excludes stack-frame tokens ("foo.go:42 +0x1d"), mid-line
// logged references ("failed at server.go:42"), and panic messages — so the
// "Failure locations" summary points at real assertion sites, not noise.
var assertionLineRe = regexp.MustCompile(`^\S+\.go:\d+:`)

// parseTestResults parses test output and generates a structured report.
func parseTestResults(framework, output string, execErr error, duration time.Duration) string {
	switch framework {
	case "go":
		return parseGoTestResults(output, execErr, duration)
	case "cargo":
		return parseCargoTestResults(output, execErr, duration)
	default:
		return parseGenericTestResults(output, execErr, duration)
	}
}

var cargoTestResultRE = regexp.MustCompile(`(?m)^test result: (?:ok|FAILED)\. ([0-9]+) passed; ([0-9]+) failed; ([0-9]+) ignored; ([0-9]+) measured; ([0-9]+) filtered out`)

// parseCargoTestResults aggregates every libtest harness in a workspace.
// Cargo prints one "test result" line per crate/target/doc-test; a tail-only
// view commonly contains only the final zero-test doc harness and loses the
// main totals.
func parseCargoTestResults(output string, execErr error, duration time.Duration) string {
	matches := cargoTestResultRE.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return parseGenericTestResults(output, execErr, duration)
	}

	var passed, failed, ignored, measured, filtered int
	for _, match := range matches {
		values := []*int{&passed, &failed, &ignored, &measured, &filtered}
		for i, dst := range values {
			n, err := strconv.Atoi(match[i+1])
			if err == nil {
				*dst += n
			}
		}
	}

	var result strings.Builder
	if execErr != nil || failed > 0 {
		fmt.Fprintf(&result, "FAIL - %d passed, %d failed", passed, failed)
	} else {
		fmt.Fprintf(&result, "PASS - %d tests passed", passed)
	}
	if ignored > 0 {
		fmt.Fprintf(&result, ", %d ignored", ignored)
	}
	if measured > 0 {
		fmt.Fprintf(&result, ", %d measured", measured)
	}
	if filtered > 0 {
		fmt.Fprintf(&result, ", %d filtered out", filtered)
	}
	fmt.Fprintf(&result, " across %d test harnesses (%.1fs)", len(matches), duration.Seconds())

	// Successful quiet runs need only the trustworthy aggregate. On failure,
	// retain bounded head+tail diagnostics for the model to act on.
	if execErr != nil || failed > 0 {
		result.WriteString("\n\n")
		result.WriteString(truncateTestOutput(output))
	}
	return result.String()
}

// parseGoTestResults parses Go's JSON test output.
func parseGoTestResults(output string, execErr error, duration time.Duration) string {
	var (
		passed     int
		failed     int
		skipped    int
		failures   []string
		packages   = make(map[string]string)   // package -> status
		failOutput = make(map[string][]string) // test -> output lines
	)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		var event goTestEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Action {
		case "pass":
			if event.Test != "" {
				passed++
			} else {
				packages[event.Package] = "pass"
			}
		case "fail":
			if event.Test != "" {
				failed++
				key := event.Package + "/" + event.Test
				failures = append(failures, key)
			} else {
				packages[event.Package] = "fail"
			}
		case "skip":
			if event.Test != "" {
				skipped++
			}
		case "output":
			if event.Test != "" {
				key := event.Package + "/" + event.Test
				failOutput[key] = append(failOutput[key], strings.TrimRight(event.Output, "\n"))
			}
		}
	}

	// If JSON parsing failed, fall back to generic
	if passed == 0 && failed == 0 && skipped == 0 {
		return parseGenericTestResults(output, execErr, duration)
	}

	var result strings.Builder
	total := passed + failed + skipped

	// Status header
	if failed > 0 {
		fmt.Fprintf(&result, "FAIL - %d/%d tests failed", failed, total)
	} else {
		fmt.Fprintf(&result, "PASS - %d tests passed", passed)
	}
	if skipped > 0 {
		fmt.Fprintf(&result, ", %d skipped", skipped)
	}
	fmt.Fprintf(&result, " (%.1fs)\n", duration.Seconds())

	// Failure location summary — extract assertion sites from all failures.
	if len(failures) > 0 {
		locationCounts := make(map[string]int) // "file.go:line" -> failure count
		for _, f := range failures {
			lines := failOutput[f]
			for _, l := range lines {
				if loc := assertionLineRe.FindString(strings.TrimSpace(l)); loc != "" {
					locationCounts[strings.TrimSuffix(loc, ":")]++
				}
			}
		}
		if len(locationCounts) > 0 {
			// Sort by descending count, then by location name
			type locCount struct {
				loc   string
				count int
			}
			sortedLocs := make([]locCount, 0, len(locationCounts))
			for loc, count := range locationCounts {
				sortedLocs = append(sortedLocs, locCount{loc, count})
			}
			sort.Slice(sortedLocs, func(i, j int) bool {
				if sortedLocs[i].count != sortedLocs[j].count {
					return sortedLocs[i].count > sortedLocs[j].count
				}
				return sortedLocs[i].loc < sortedLocs[j].loc
			})
			result.WriteString("\nFailure locations:\n")
			for _, lc := range sortedLocs {
				fmt.Fprintf(&result, "  📍 %s (%d failure(s))\n", lc.loc, lc.count)
			}
		}
	}

	// Failed test details
	if len(failures) > 0 {
		result.WriteString("\nFailed tests:\n")
		for _, f := range failures {
			fmt.Fprintf(&result, "  ✗ %s\n", f)
			if lines, ok := failOutput[f]; ok {
				// Show the meaningful output lines in their original order,
				// capped. Order matters: a panic's "panic:" message and the
				// continuation lines of a multi-line assertion (e.g. testify's
				// expected/actual) carry the actual failure reason but do NOT
				// contain a file:line token — filtering to only file:line lines
				// (the previous behaviour) dropped them. The "Failure locations"
				// summary above already provides the file:line navigation.
				meaningful := make([]string, 0, len(lines))
				for _, l := range lines {
					l = strings.TrimSpace(l)
					if l == "" ||
						strings.HasPrefix(l, "=== RUN") || strings.HasPrefix(l, "=== PAUSE") ||
						strings.HasPrefix(l, "=== CONT") || strings.HasPrefix(l, "=== NAME") ||
						strings.HasPrefix(l, "--- FAIL") || strings.HasPrefix(l, "--- PASS") ||
						strings.HasPrefix(l, "--- SKIP") {
						continue
					}
					meaningful = append(meaningful, l)
				}
				// Head+tail split: a panic's "panic:" message leads the output,
				// while go-test assertions (got/want) are appended at the END —
				// a head-only cap drops the failure reason for chatty tests and
				// a tail-only cap drops the panic message under a long stack.
				const maxDetailLines = 20
				const detailHeadLines = 8
				if len(meaningful) <= maxDetailLines {
					for _, l := range meaningful {
						fmt.Fprintf(&result, "    %s\n", l)
					}
				} else {
					for _, l := range meaningful[:detailHeadLines] {
						fmt.Fprintf(&result, "    %s\n", l)
					}
					fmt.Fprintf(&result, "    ... (%d middle lines elided)\n", len(meaningful)-maxDetailLines)
					for _, l := range meaningful[len(meaningful)-(maxDetailLines-detailHeadLines):] {
						fmt.Fprintf(&result, "    %s\n", l)
					}
				}
			}
		}
	}

	// Package summary
	var failedPkgs []string
	for pkg, status := range packages {
		if status == "fail" {
			failedPkgs = append(failedPkgs, pkg)
		}
	}
	sort.Strings(failedPkgs)
	if len(failedPkgs) > 0 {
		fmt.Fprintf(&result, "\nFailed packages: %s\n", strings.Join(failedPkgs, ", "))
	}

	return result.String()
}

// parseGenericTestResults handles non-JSON test output.
func parseGenericTestResults(output string, execErr error, duration time.Duration) string {
	var result strings.Builder

	if execErr != nil {
		fmt.Fprintf(&result, "FAIL (%.1fs)\n\n", duration.Seconds())
	} else {
		fmt.Fprintf(&result, "PASS (%.1fs)\n\n", duration.Seconds())
	}

	result.WriteString(truncateTestOutput(output))

	return result.String()
}

func truncateTestOutput(output string) string {
	runes := []rune(output)
	if len(runes) <= 5000 {
		return output
	}
	return string(runes[:2000]) +
		"\n\n... (output truncated) ...\n\n" +
		string(runes[len(runes)-2000:])
}
