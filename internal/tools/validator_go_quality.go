package tools

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

// GoQualityValidator checks Go source files for common model mistakes
// that compile but indicate poor practices or latent bugs.
type GoQualityValidator struct{}

func (v *GoQualityValidator) Name() string { return "go_quality" }

func (v *GoQualityValidator) Matches(filePath string) bool {
	return strings.HasSuffix(filePath, ".go") && !strings.HasSuffix(filePath, "_test.go")
}

var (
	// os.Open/os.OpenFile without a matching defer .Close() nearby
	osOpenRe     = regexp.MustCompile(`\b(os\.Open|os\.OpenFile|os\.Create)\s*\(`)
	deferCloseRe = regexp.MustCompile(`defer\s+\w+\.Close\(\)`)

	// Deprecated ioutil usage (Go 1.16+)
	ioutilRe = regexp.MustCompile(`\bioutil\.(ReadAll|ReadFile|WriteFile|ReadDir|TempFile|TempDir|NopCloser|Discard)\b`)

	// fmt.Errorf with %s/%v for error wrapping instead of %w
	errfNoWrapRe = regexp.MustCompile(`fmt\.Errorf\([^)]*(%s|%v)\s*"\s*,\s*\w*[Ee]rr\b`)

	// Naked goroutine without context/done channel (heuristic)
	nakedGoroutineRe = regexp.MustCompile(`go\s+func\s*\(`)
)

func (v *GoQualityValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)

	lines := strings.Split(string(content), "\n")

	// Track os.Open calls and whether defer Close follows within 5 lines
	for i, line := range lines {
		if osOpenRe.MatchString(line) && !strings.Contains(line, "//") {
			hasDefer := false
			end := i + 6
			if end > len(lines) {
				end = len(lines)
			}
			for _, next := range lines[i+1 : end] {
				if deferCloseRe.MatchString(next) {
					hasDefer = true
					break
				}
			}
			if !hasDefer {
				warnings = append(warnings, ValidationWarning{
					Validator: v.Name(),
					Severity:  "warning",
					File:      baseName,
					Line:      i + 1,
					Message:   "os.Open/Create without defer Close() within 5 lines — potential resource leak",
				})
			}
		}
	}

	// Deprecated ioutil
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if m := ioutilRe.FindString(line); m != "" {
			replacement := strings.Replace(m, "ioutil.", "io.", 1)
			if strings.HasPrefix(m, "ioutil.ReadFile") || strings.HasPrefix(m, "ioutil.WriteFile") || strings.HasPrefix(m, "ioutil.ReadDir") || strings.HasPrefix(m, "ioutil.TempFile") || strings.HasPrefix(m, "ioutil.TempDir") {
				replacement = strings.Replace(m, "ioutil.", "os.", 1)
			}
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      lineNum,
				Message:   m + " is deprecated since Go 1.16 — use " + replacement,
			})
		}

		if errfNoWrapRe.MatchString(line) {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "info",
				File:      baseName,
				Line:      lineNum,
				Message:   "fmt.Errorf wraps error with %s/%v instead of %w — error chain will be lost",
			})
		}
	}

	return warnings
}
