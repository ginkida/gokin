package tools

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

// SecurityValidator checks for hardcoded secrets and credentials
// in non-test source files.
type SecurityValidator struct{}

func (v *SecurityValidator) Name() string { return "security" }

func (v *SecurityValidator) Matches(filePath string) bool {
	// Skip test files, config examples, and documentation
	if strings.HasSuffix(filePath, "_test.go") {
		return false
	}
	ext := filepath.Ext(filePath)
	return ext == ".go" || ext == ".py" || ext == ".js" || ext == ".ts" ||
		ext == ".yml" || ext == ".yaml" || ext == ".json" || ext == ".toml" ||
		ext == ".env" || ext == ".sh"
}

var (
	// Patterns for hardcoded secrets (high confidence)
	secretPatterns = []struct {
		re      *regexp.Regexp
		message string
	}{
		{
			re:      regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[:=]\s*["']([a-zA-Z0-9_\-]{20,})["']`),
			message: "hardcoded API key detected",
		},
		{
			re:      regexp.MustCompile(`(?i)(secret|password|passwd|pwd)\s*[:=]\s*["']([^\s"']{8,})["']`),
			message: "hardcoded password/secret detected",
		},
		{
			re:      regexp.MustCompile(`(?i)(bearer|token)\s+[a-zA-Z0-9_\-\.]{20,}`),
			message: "hardcoded bearer token detected",
		},
		{
			re:      regexp.MustCompile(`(?i)-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`),
			message: "private key embedded in source code",
		},
		{
			re:      regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),
			message: "GitHub personal access token detected",
		},
		{
			re:      regexp.MustCompile(`sk-[a-zA-Z0-9]{32,}`),
			message: "OpenAI/Anthropic API key detected",
		},
		{
			re:      regexp.MustCompile(`AIza[a-zA-Z0-9_\-]{35}`),
			message: "Google API key detected",
		},
	}

	// Lines to skip (common false positives)
	skipLinePatterns = []string{
		"example", "placeholder", "xxx", "your-", "test", "fake", "mock", "dummy",
		"TODO", "FIXME", "os.Getenv", "env.", "config.", "viper.",
	}
)

func (v *SecurityValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)
	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		// Skip comments
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		// Skip lines with common false-positive indicators
		lower := strings.ToLower(line)
		skip := false
		for _, pat := range skipLinePatterns {
			if strings.Contains(lower, strings.ToLower(pat)) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		for _, sp := range secretPatterns {
			if sp.re.MatchString(line) {
				warnings = append(warnings, ValidationWarning{
					Validator: v.Name(),
					Severity:  "error",
					File:      baseName,
					Line:      i + 1,
					Message:   sp.message + " — use environment variables or config files",
				})
				break // one warning per line
			}
		}
	}

	return warnings
}
