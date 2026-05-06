package tools

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

// ShellValidator checks shell scripts for common mistakes.
type ShellValidator struct{}

func (v *ShellValidator) Name() string { return "shell" }

func (v *ShellValidator) Matches(filePath string) bool {
	ext := filepath.Ext(filePath)
	return ext == ".sh" || ext == ".bash"
}

var (
	shebangRe    = regexp.MustCompile(`^#!\s*/`)
	setErrExitRe = regexp.MustCompile(`set\s+-[a-z]*e`)       // set -e, set -euo pipefail, etc.
	rmRfRe       = regexp.MustCompile(`rm\s+-rf?\s+\$\{?\w`)  // rm -rf $VAR (unquoted)
	cdUnquotedRe = regexp.MustCompile(`cd\s+\$\{?\w+\}?\s*$`) // cd $VAR (unquoted)
)

func (v *ShellValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)
	lines := strings.Split(string(content), "\n")

	if len(lines) == 0 {
		return nil
	}

	// Check for shebang
	hasShebang := shebangRe.MatchString(lines[0])
	if len(lines) > 3 && !hasShebang {
		warnings = append(warnings, ValidationWarning{
			Validator: v.Name(),
			Severity:  "info",
			File:      baseName,
			Line:      1,
			Message:   "shell script without shebang line (#!/bin/bash or #!/usr/bin/env bash)",
		})
	}

	// Check for set -e / set -euo pipefail in first 10 lines
	hasSetE := false
	limit := 10
	if limit > len(lines) {
		limit = len(lines)
	}
	for _, line := range lines[:limit] {
		if setErrExitRe.MatchString(line) {
			hasSetE = true
			break
		}
	}
	if !hasSetE && len(lines) > 5 {
		warnings = append(warnings, ValidationWarning{
			Validator: v.Name(),
			Severity:  "warning",
			File:      baseName,
			Line:      1,
			Message:   "script lacks 'set -e' or 'set -euo pipefail' — errors will be silently ignored",
		})
	}

	// Check for dangerous unquoted variable expansion
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		if rmRfRe.MatchString(line) {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "error",
				File:      baseName,
				Line:      i + 1,
				Message:   "rm -rf with unquoted variable — if variable is empty, this deletes from current directory",
			})
		}

		if cdUnquotedRe.MatchString(line) {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      i + 1,
				Message:   "cd with unquoted variable — quote it: cd \"$VAR\"",
			})
		}
	}

	return warnings
}
