package tools

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

// DockerfileValidator checks Dockerfiles for common model mistakes.
type DockerfileValidator struct{}

func (v *DockerfileValidator) Name() string { return "dockerfile" }

func (v *DockerfileValidator) Matches(filePath string) bool {
	base := strings.ToLower(filepath.Base(filePath))
	return base == "dockerfile" || strings.HasPrefix(base, "dockerfile.")
}

var (
	fromLatestRe  = regexp.MustCompile(`(?i)^FROM\s+\S+:latest\b`)
	fromWithTagRe = regexp.MustCompile(`(?i)^FROM\s+\S+:\S+`)
	fromAsRe       = regexp.MustCompile(`(?i)\bAS\s+\w+`)
	addInsteadCopy = regexp.MustCompile(`(?i)^ADD\s+[^h]`) // ADD for non-URL (should be COPY)
)

func (v *DockerfileValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)
	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// FROM image:latest — unpinned
		if fromLatestRe.MatchString(trimmed) {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      i + 1,
				Message:   "FROM uses :latest tag — pin to a specific version for reproducible builds",
			})
		}

		// FROM image (no tag at all, no AS)
		if strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") &&
			!fromWithTagRe.MatchString(trimmed) &&
			!strings.Contains(trimmed, "${") &&
			!fromAsRe.MatchString(trimmed) {
			// Check it's not just "FROM scratch" or a build arg
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 && parts[1] != "scratch" && !strings.HasPrefix(parts[1], "$") {
				warnings = append(warnings, ValidationWarning{
					Validator: v.Name(),
					Severity:  "warning",
					File:      baseName,
					Line:      i + 1,
					Message:   "FROM without version tag — defaults to :latest, pin to a specific version",
				})
			}
		}

		// ADD instead of COPY for local files
		if addInsteadCopy.MatchString(trimmed) && !strings.Contains(trimmed, "http") {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "info",
				File:      baseName,
				Line:      i + 1,
				Message:   "ADD used for local files — prefer COPY (ADD has implicit tar extraction and URL fetch)",
			})
		}
	}

	return warnings
}
