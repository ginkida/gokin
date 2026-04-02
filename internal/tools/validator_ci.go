package tools

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ---------- CIWorkflowValidator ----------

// CIWorkflowValidator checks GitHub Actions workflow files for common mistakes.
type CIWorkflowValidator struct{}

func (v *CIWorkflowValidator) Name() string { return "ci_workflow" }

func (v *CIWorkflowValidator) Matches(filePath string) bool {
	return isGitHubWorkflow(filePath)
}

var (
	// Quoted heredoc: << 'DELIM' or << "DELIM" — kills shell variable interpolation
	quotedHeredocRe = regexp.MustCompile(`<<\s*['"](\w+)['"]`)
	// Shell variable usage inside heredoc body (only problematic with quoted heredocs)
	shellVarRe = regexp.MustCompile(`\$\{?\w+`)
	// Unpinned action: uses: owner/repo@master or @main
	unpinnedActionRe = regexp.MustCompile(`uses:\s*\S+@(master|main)\s*$`)
	// Step output write: >> $GITHUB_OUTPUT
	stepOutputWriteRe = regexp.MustCompile(`>>\s*\$GITHUB_OUTPUT`)
	// Step output variable name: echo "NAME=value" >> $GITHUB_OUTPUT
	stepOutputNameRe = regexp.MustCompile(`echo\s+"?(\w+)=`)
)

func (v *CIWorkflowValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)
	lines := strings.Split(string(content), "\n")

	// Detect quoted heredocs with shell vars inside
	warnings = append(warnings, checkQuotedHeredocs(baseName, lines)...)

	// Detect unpinned actions
	for i, line := range lines {
		if unpinnedActionRe.MatchString(line) {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      i + 1,
				Message:   "action pinned to master/main branch — pin to a tag or SHA for reproducibility",
			})
		}
	}

	// Detect step outputs used as shell vars instead of ${{ steps.*.outputs.* }}
	warnings = append(warnings, checkStepOutputUsage(baseName, lines)...)

	return warnings
}

// checkQuotedHeredocs detects << 'DELIM' followed by ${VAR} usage inside the body.
func checkQuotedHeredocs(file string, lines []string) []ValidationWarning {
	var warnings []ValidationWarning
	var heredocDelim string
	heredocStart := 0

	for i, line := range lines {
		if heredocDelim == "" {
			// Look for quoted heredoc start
			if m := quotedHeredocRe.FindStringSubmatch(line); m != nil {
				heredocDelim = m[1]
				heredocStart = i + 1
			}
		} else {
			// Inside heredoc body
			trimmed := strings.TrimSpace(line)
			if trimmed == heredocDelim {
				heredocDelim = ""
				continue
			}
			// Check for shell variable usage that won't be interpolated
			if shellVarRe.MatchString(line) && !strings.Contains(line, "#{") {
				warnings = append(warnings, ValidationWarning{
					Validator: "ci_workflow",
					Severity:  "error",
					File:      file,
					Line:      heredocStart,
					Message:   "quoted heredoc (<< 'DELIM') prevents shell variable interpolation — use unquoted (<< DELIM) or ${{ }} expressions",
				})
				heredocDelim = "" // one warning per heredoc is enough
			}
		}
	}
	return warnings
}

// checkStepOutputUsage detects step output names written to $GITHUB_OUTPUT
// but later referenced as bare shell variables instead of ${{ steps.*.outputs.* }}.
func checkStepOutputUsage(file string, lines []string) []ValidationWarning {
	var warnings []ValidationWarning

	// Collect output names written in "id: xxx" steps
	outputNames := make(map[string]bool)
	for _, line := range lines {
		if stepOutputWriteRe.MatchString(line) {
			if m := stepOutputNameRe.FindStringSubmatch(line); m != nil {
				outputNames[m[1]] = true
			}
		}
	}

	if len(outputNames) == 0 {
		return nil
	}

	// Look for bare ${NAME} usage outside ${{ }} expressions
	for i, line := range lines {
		// Skip lines that are writing outputs
		if stepOutputWriteRe.MatchString(line) {
			continue
		}
		// Skip lines that use ${{ }} properly
		if strings.Contains(line, "${{") {
			continue
		}
		for name := range outputNames {
			// Check for ${NAME} or $NAME used as shell variable
			if strings.Contains(line, "${"+name+"}") || strings.Contains(line, "${"+name+"#") {
				warnings = append(warnings, ValidationWarning{
					Validator: "ci_workflow",
					Severity:  "error",
					File:      file,
					Line:      i + 1,
					Message:   name + " is a step output — use ${{ steps.<step_id>.outputs." + name + " }} instead of ${" + name + "}",
				})
			}
		}
	}

	return warnings
}

// ---------- VersionConsistencyValidator ----------

// VersionConsistencyValidator checks that Go/Node/Python versions are consistent
// between project manifest files and CI workflow files.
type VersionConsistencyValidator struct{}

func (v *VersionConsistencyValidator) Name() string { return "version_consistency" }

func (v *VersionConsistencyValidator) Matches(filePath string) bool {
	base := filepath.Base(filePath)
	return isGitHubWorkflow(filePath) ||
		base == "go.mod" ||
		base == "package.json" ||
		base == "pyproject.toml"
}

var goVersionInWorkflowRe = regexp.MustCompile(`go-version:\s*['"]?([0-9]+\.[0-9]+[0-9.]*)['"]?`)

func (v *VersionConsistencyValidator) Validate(_ context.Context, filePath string, _ []byte, workDir string) []ValidationWarning {
	var warnings []ValidationWarning

	// Check Go version consistency
	goModVersion := readGoModVersion(workDir)
	if goModVersion == "" {
		return nil
	}

	// Scan all workflow files for go-version
	workflowDir := filepath.Join(workDir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		wfPath := filepath.Join(workflowDir, entry.Name())
		data, err := os.ReadFile(wfPath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if m := goVersionInWorkflowRe.FindStringSubmatch(line); m != nil {
				wfVersion := m[1]
				if !versionsCompatible(goModVersion, wfVersion) {
					warnings = append(warnings, ValidationWarning{
						Validator: v.Name(),
						Severity:  "error",
						File:      entry.Name(),
						Line:      lineNum,
						Message:   "go-version '" + wfVersion + "' does not match go.mod '" + goModVersion + "'",
					})
				}
			}
		}
	}

	return warnings
}

// readGoModVersion extracts the Go version from go.mod (e.g. "1.25.7").
func readGoModVersion(workDir string) string {
	data, err := os.ReadFile(filepath.Join(workDir, "go.mod"))
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "go ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	return ""
}

// versionsCompatible checks if two version strings are compatible.
// "1.25.7" matches "1.25.7" or "1.25". "1.23" does NOT match "1.25.7".
func versionsCompatible(canonical, workflow string) bool {
	if canonical == workflow {
		return true
	}
	// Allow workflow to use major.minor when canonical has patch
	// e.g. go.mod "1.25.7", workflow "1.25" → OK
	if strings.HasPrefix(canonical, workflow+".") {
		return true
	}
	// Allow workflow to have patch when canonical is major.minor
	if strings.HasPrefix(workflow, canonical+".") {
		return true
	}
	return false
}

func isGitHubWorkflow(filePath string) bool {
	return strings.Contains(filePath, ".github/workflows/") &&
		(strings.HasSuffix(filePath, ".yml") || strings.HasSuffix(filePath, ".yaml"))
}
