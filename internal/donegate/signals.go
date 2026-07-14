package donegate

import "strings"

func CommandsContainVerificationSignals(commands []string) bool {
	keywords := []string{
		" test", "go test", "pytest", "cargo test", "npm test", "pnpm test", "yarn test", "bun test",
		"lint", "typecheck", " tsc", "check", "verify", "validate", "vet", "build", "compile",
	}
	for _, cmd := range commands {
		lower := " " + strings.ToLower(strings.TrimSpace(cmd))
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	return false
}

func OutputContainsVerificationSignals(output string) bool {
	lower := strings.ToLower(strings.TrimSpace(output))
	if lower == "" {
		return false
	}

	// Check positive signals first — "all tests passed" is conclusive even if
	// test names contain words like "error" or "failure" (e.g. test_error_handling).
	positive := []string{
		"all tests passed", "tests passed", "build succeeded",
		"passed", "successful", "verified", "no issues", "no errors",
		"lint passed", "check passed",
		"успеш", "проверен", "проверка пройдена", "без ошибок",
	}
	for _, marker := range positive {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	// Only reject on negative signals when no positive signal was found.
	negative := []string{" failed", " failure", "panic:", "traceback", "assertionerror"}
	for _, marker := range negative {
		if strings.Contains(" "+lower, marker) {
			return false
		}
	}
	return false
}
