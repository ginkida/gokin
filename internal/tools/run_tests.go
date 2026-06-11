package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
)

// RunTestsTool runs project tests and parses results.
type RunTestsTool struct {
	workDir string
}

// NewRunTestsTool creates a new RunTestsTool instance.
func NewRunTestsTool(workDir string) *RunTestsTool {
	return &RunTestsTool{workDir: workDir}
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
			},
		},
	}
}

func (t *RunTestsTool) Validate(args map[string]any) error {
	return nil
}

func (t *RunTestsTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	testPath := GetStringDefault(args, "path", "")
	filter := GetStringDefault(args, "filter", "")
	verbose := GetBoolDefault(args, "verbose", false)
	coverage := GetBoolDefault(args, "coverage", false)
	framework := GetStringDefault(args, "framework", "auto")

	workDir := t.workDir
	if testPath != "" {
		if filepath.IsAbs(testPath) {
			workDir = testPath
		} else {
			workDir = filepath.Join(t.workDir, testPath)
		}
		if info, err := os.Stat(workDir); err == nil && !info.IsDir() {
			workDir = filepath.Dir(workDir)
		}
	}

	// Auto-detect framework
	if framework == "auto" {
		framework = detectTestFramework(workDir)
		if framework == "" {
			return NewErrorResult("could not detect test framework. Specify 'framework' parameter."), nil
		}
	}

	// Build command
	cmdName, cmdArgs := buildTestCommand(framework, workDir, filter, verbose, coverage)

	// Execute with timeout
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(testCtx, cmdName, cmdArgs...)
	cmd.Dir = workDir

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	outStr := string(output)

	// Parse results based on framework
	result := parseTestResults(framework, outStr, err, duration)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tests failed (%s)", framework),
			Content: result,
		}, nil
	}

	return NewSuccessResult(result), nil
}

// detectTestFramework auto-detects the test framework from project files.
func detectTestFramework(dir string) string {
	return detectTestFrameworkBounded(dir, 10)
}

func detectTestFrameworkBounded(dir string, depth int) string {
	if depth <= 0 {
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
		if _, err := os.Stat(filepath.Join(dir, check.file)); err == nil {
			return check.framework
		}
	}

	parent := filepath.Dir(dir)
	if parent != dir {
		return detectTestFrameworkBounded(parent, depth-1)
	}

	return ""
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
		args := []string{"test"}
		if !verbose {
			args = append(args, "--quiet")
		}
		if filter != "" {
			args = append(args, filter)
		}
		args = append(args, "--", "--format", "json")
		return "cargo", args

	default:
		return "echo", []string{"unknown framework: " + framework}
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
	default:
		return parseGenericTestResults(output, execErr, duration)
	}
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

	// Truncate long output
	if runes := []rune(output); len(runes) > 5000 {
		// Show first 2000 and last 2000 chars
		result.WriteString(string(runes[:2000]))
		result.WriteString("\n\n... (output truncated) ...\n\n")
		result.WriteString(string(runes[len(runes)-2000:]))
	} else {
		result.WriteString(output)
	}

	return result.String()
}
