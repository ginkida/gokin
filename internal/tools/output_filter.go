package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// FilterBashOutput applies smart filtering to bash command output to reduce
// token consumption. Inspired by rtk's approach:
// - Deduplicates consecutive repeated lines (shows count)
// - Removes noise patterns (progress bars, download indicators, warnings)
// - Collapses blank line runs
//
// Applied before truncation so the head+tail strategy works on clean output.
func FilterBashOutput(output string) string {
	if len(output) < 100 {
		return output // Too short to benefit from filtering
	}

	lines := strings.Split(output, "\n")
	if len(lines) < 3 {
		return output
	}

	var result []string
	var prevLine string
	repeatCount := 0
	totalDeduped := 0
	blankRun := 0
	filtered := 0

	for _, line := range lines {
		// Collapse runs of blank lines to max 1
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun <= 1 {
				result = append(result, line)
			} else {
				filtered++
			}
			continue
		}
		blankRun = 0

		// Skip noise lines
		if isNoiseLine(line) {
			filtered++
			continue
		}

		// Strip progress bar artifacts (overwrite characters)
		if cleaned := cleanProgressArtifacts(line); cleaned != line {
			line = cleaned
			if strings.TrimSpace(line) == "" {
				filtered++
				continue
			}
		}

		// Deduplicate consecutive identical lines
		if line == prevLine {
			repeatCount++
			continue
		}

		// Flush previous repeats
		if repeatCount > 0 {
			result = append(result, fmt.Sprintf("  ... (repeated %d more times)", repeatCount))
			totalDeduped += repeatCount
			repeatCount = 0
		}

		prevLine = line
		result = append(result, line)
	}

	// Flush trailing repeats
	if repeatCount > 0 {
		result = append(result, fmt.Sprintf("  ... (repeated %d more times)", repeatCount))
		totalDeduped += repeatCount
	}

	// Only return filtered version if we actually saved something meaningful
	if filtered == 0 && totalDeduped < 2 {
		return output // Not worth the overhead
	}

	out := strings.Join(result, "\n")
	if filtered > 0 {
		out += fmt.Sprintf("\n[%d noise lines filtered]", filtered)
	}
	return out
}

// noisePatterns are compiled regexes for lines that add no value to LLM context.
var noisePatterns = []*regexp.Regexp{
	// npm/yarn/pnpm download/install progress
	regexp.MustCompile(`^(npm |yarn |pnpm )(WARN|warn|notice)\b`),
	regexp.MustCompile(`^(added|removed|updated) \d+ packages? in \d+`),
	regexp.MustCompile(`^\d+ packages? are looking for funding`),
	regexp.MustCompile(`^  run `), // "run npm fund for details"

	// Cargo fetch/download progress
	regexp.MustCompile(`^\s*(Downloading|Downloaded|Compiling|Updating)\s+\S+\s+v`),
	regexp.MustCompile(`^\s*Fetching\s+`),
	regexp.MustCompile(`^\s*Locking \d+ packages?`),

	// pip/uv download progress
	regexp.MustCompile(`^\s*(Downloading|Collecting|Using cached)\s+\S+`),
	regexp.MustCompile(`^\s*Successfully installed\s+`),

	// Go module download
	regexp.MustCompile(`^go: downloading\s+`),

	// Docker pull progress
	regexp.MustCompile(`^[0-9a-f]{6,}: (Pulling|Waiting|Downloading|Extracting|Verifying|Pull complete)`),
	regexp.MustCompile(`^Digest: sha256:`),
	regexp.MustCompile(`^Status: (Downloaded|Image is up to date)`),

	// General progress indicators
	regexp.MustCompile(`^\s*\[[-=>#\s]+\]\s*\d+%`),              // [=====>    ] 50%
	regexp.MustCompile(`^\s*\d+%\s*\|[█▓▒░\s]*\|`),             // 50% |████    |
	regexp.MustCompile(`^\s*\(\d+/\d+\)\s+(Downloading|Fetching|Installing)`), // (3/10) Downloading

	// Bundle/gem install noise
	regexp.MustCompile(`^(Using|Fetching|Installing)\s+\S+\s+\d+`),

	// Deprecation warnings (keep real errors, filter noise warnings)
	regexp.MustCompile(`^(DEPRECATION|DeprecationWarning):`),
}

// isNoiseLine checks if a line matches any noise pattern.
func isNoiseLine(line string) bool {
	for _, re := range noisePatterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// progressArtifacts matches terminal control sequences and overwrite characters.
var progressArtifacts = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\r[^\n]`)

// cleanProgressArtifacts removes ANSI escape codes and carriage return overwrites.
func cleanProgressArtifacts(line string) string {
	return progressArtifacts.ReplaceAllString(line, "")
}
