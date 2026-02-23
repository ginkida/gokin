package context

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// SummarizeForPrune produces a compact summary of tool output
// for use when pruning old tool outputs from history.
// Returns a 100-300 char summary preserving key information
// instead of a blank "[truncated]" placeholder.
func (c *ResultCompactor) SummarizeForPrune(toolName, content string) string {
	charCount := len(content)
	if charCount == 0 {
		return fmt.Sprintf("[%s: empty output]", toolName)
	}

	var summary string
	switch toolName {
	case "read":
		summary = summarizeReadOutput(content)
	case "bash":
		summary = summarizeBashOutput(content)
	case "grep":
		summary = summarizeGrepOutput(content)
	case "glob":
		summary = summarizeGlobOutput(content)
	case "git_diff":
		summary = summarizeGitDiffOutput(content)
	case "git_log":
		summary = summarizeGitLogOutput(content)
	case "tree":
		summary = summarizeTreeOutput(content)
	default:
		summary = defaultSummary(toolName, content)
	}

	return fmt.Sprintf("[%s: %s | was %d chars]", toolName, summary, charCount)
}

// summarizeReadOutput extracts line count, package name, and Go signatures.
func summarizeReadOutput(content string) string {
	lines := strings.Split(content, "\n")
	lineCount := len(lines)

	// Detect package name from first lines (cat -n format: "   1\tpackage foo")
	var pkgName string
	limit := 10
	if limit > lineCount {
		limit = lineCount
	}
	for _, line := range lines[:limit] {
		trimmed := stripLineNumber(line)
		if strings.HasPrefix(trimmed, "package ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				pkgName = parts[1]
			}
			break
		}
	}

	// Extract Go signatures (func/type declarations)
	var funcs []string
	var types []string
	for _, line := range lines {
		trimmed := stripLineNumber(line)
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "func (") {
			name := extractFuncName(trimmed)
			if name != "" {
				funcs = append(funcs, name)
			}
		} else if strings.HasPrefix(trimmed, "type ") {
			name := extractTypeName(trimmed)
			if name != "" {
				types = append(types, name)
			}
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d lines", lineCount))
	if pkgName != "" {
		parts = append(parts, "package "+pkgName)
	}
	if len(funcs) > 0 {
		shown := funcs
		if len(shown) > 5 {
			shown = shown[:5]
		}
		parts = append(parts, "funcs: "+strings.Join(shown, ", "))
	}
	if len(types) > 0 {
		shown := types
		if len(shown) > 5 {
			shown = shown[:5]
		}
		parts = append(parts, "types: "+strings.Join(shown, ", "))
	}

	return strings.Join(parts, " | ")
}

// summarizeBashOutput extracts exit status, errors, and test results.
func summarizeBashOutput(content string) string {
	lines := strings.Split(content, "\n")
	lineCount := len(lines)

	// Detect exit status
	exitStatus := ""
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.Contains(line, "exit status") {
			// Extract the number
			idx := strings.Index(line, "exit status")
			rest := strings.TrimSpace(line[idx+len("exit status"):])
			if rest != "" {
				exitStatus = strings.Fields(rest)[0]
			}
			break
		}
	}

	// Collect errors (first 2)
	var errors []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		if len(errors) >= 2 {
			break
		}
		if strings.Contains(lower, "error:") || strings.Contains(lower, "undefined:") ||
			strings.Contains(lower, "cannot use") || strings.Contains(lower, "panic:") {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 80 {
				trimmed = trimmed[:80]
			}
			errors = append(errors, trimmed)
		}
	}

	// Count test results
	var passCount, failCount int
	for _, line := range lines {
		if strings.Contains(line, "--- PASS") || strings.HasPrefix(strings.TrimSpace(line), "ok ") {
			passCount++
		}
		if strings.Contains(line, "--- FAIL") || strings.Contains(line, "FAIL\t") {
			failCount++
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d lines", lineCount))
	if exitStatus != "" {
		parts = append(parts, "exit="+exitStatus)
	}
	if len(errors) > 0 {
		quoted := make([]string, len(errors))
		for i, e := range errors {
			quoted[i] = fmt.Sprintf("%q", e)
		}
		parts = append(parts, "errors: "+strings.Join(quoted, ", "))
	}
	if failCount > 0 || passCount > 0 {
		parts = append(parts, fmt.Sprintf("FAIL: %d, PASS: %d", failCount, passCount))
	}

	return strings.Join(parts, " | ")
}

// summarizeGrepOutput parses grep output (path:line:content format) for file stats.
func summarizeGrepOutput(content string) string {
	lines := strings.Split(content, "\n")

	fileSet := make(map[string]int)
	matchCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matchCount++
		// Parse "path:line:content" or "path:line-content"
		if idx := strings.IndexByte(line, ':'); idx > 0 {
			path := line[:idx]
			// Validate it looks like a file path (not a bare number)
			if !strings.ContainsAny(path, " \t") {
				fileSet[path]++
			}
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d matches in %d files", matchCount, len(fileSet)))

	if len(fileSet) > 0 {
		// Show top files by match count
		type fileCount struct {
			name  string
			count int
		}
		var sorted []fileCount
		for f, c := range fileSet {
			sorted = append(sorted, fileCount{filepath.Base(f), c})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].count > sorted[j].count
		})
		shown := sorted
		if len(shown) > 5 {
			shown = shown[:5]
		}
		names := make([]string, len(shown))
		for i, fc := range shown {
			names[i] = fc.name
		}
		parts = append(parts, strings.Join(names, ", "))
	}

	return strings.Join(parts, " | ")
}

// summarizeGlobOutput counts files and groups by directory.
func summarizeGlobOutput(content string) string {
	lines := strings.Split(content, "\n")

	dirCounts := make(map[string]int)
	fileCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fileCount++
		dir := filepath.Dir(line)
		dirCounts[dir]++
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d files", fileCount))

	if len(dirCounts) > 0 {
		type dirCount struct {
			name  string
			count int
		}
		var sorted []dirCount
		for d, c := range dirCounts {
			sorted = append(sorted, dirCount{d, c})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].count > sorted[j].count
		})
		shown := sorted
		if len(shown) > 3 {
			shown = shown[:3]
		}
		dirStrs := make([]string, len(shown))
		for i, dc := range shown {
			dirStrs[i] = fmt.Sprintf("%s (%d)", dc.name, dc.count)
		}
		parts = append(parts, "top dirs: "+strings.Join(dirStrs, ", "))
	}

	return strings.Join(parts, " | ")
}

// summarizeGitDiffOutput extracts file count and +/- stats.
func summarizeGitDiffOutput(content string) string {
	lines := strings.Split(content, "\n")

	var files []string
	var added, removed int

	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			file := strings.TrimPrefix(line, "+++ b/")
			files = append(files, filepath.Base(file))
		} else if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d files (+%d -%d)", len(files), added, removed))

	if len(files) > 0 {
		shown := files
		if len(shown) > 5 {
			shown = shown[:5]
		}
		parts = append(parts, strings.Join(shown, ", "))
	}

	return strings.Join(parts, " | ")
}

// summarizeGitLogOutput extracts commit count and latest message.
func summarizeGitLogOutput(content string) string {
	lines := strings.Split(content, "\n")

	commitCount := 0
	latestMsg := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Detect commit lines: hash-like prefix (7+ hex chars) or "commit <hash>"
		if strings.HasPrefix(trimmed, "commit ") {
			commitCount++
			continue
		}
		// Short log format: "abc1234 message"
		if len(trimmed) >= 7 && isHexPrefix(trimmed[:7]) {
			commitCount++
			if latestMsg == "" {
				if spaceIdx := strings.IndexByte(trimmed, ' '); spaceIdx > 0 {
					latestMsg = strings.TrimSpace(trimmed[spaceIdx:])
				}
			}
			continue
		}
		// Full log format: capture first non-empty line after "commit" as message
		if commitCount > 0 && latestMsg == "" && !strings.HasPrefix(trimmed, "Author:") &&
			!strings.HasPrefix(trimmed, "Date:") && !strings.HasPrefix(trimmed, "Merge:") {
			latestMsg = trimmed
		}
	}

	if commitCount == 0 {
		// Fallback: count non-empty lines as entries
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				commitCount++
			}
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d commits", commitCount))
	if latestMsg != "" {
		if len(latestMsg) > 60 {
			latestMsg = latestMsg[:60]
		}
		parts = append(parts, fmt.Sprintf("latest: %q", latestMsg))
	}

	return strings.Join(parts, " | ")
}

// summarizeTreeOutput counts entries and extracts top-level directories.
func summarizeTreeOutput(content string) string {
	lines := strings.Split(content, "\n")

	entryCount := 0
	var topDirs []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entryCount++
		// Top-level entries have minimal indentation (tree uses ├── └── │)
		// A top-level item has the tree connector at the start
		trimmed := strings.TrimLeft(line, " ")
		if len(trimmed) == len(line) || len(line)-len(trimmed) <= 0 {
			// No leading spaces — could be the root or a top-level entry
			// Check for tree connectors: ├── └── │
			cleaned := strings.TrimLeft(line, "├└│── ")
			if cleaned != "" && strings.HasSuffix(cleaned, "/") {
				topDirs = append(topDirs, strings.TrimSuffix(cleaned, "/"))
			}
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d entries", entryCount))
	if len(topDirs) > 0 {
		shown := topDirs
		if len(shown) > 5 {
			shown = shown[:5]
		}
		parts = append(parts, "top: "+strings.Join(shown, ", "))
	}

	return strings.Join(parts, " | ")
}

// defaultSummary provides a generic summary with the first line of content.
func defaultSummary(toolName, content string) string {
	firstLine := content
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		firstLine = content[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)
	if len(firstLine) > 100 {
		firstLine = firstLine[:100]
	}
	return firstLine
}

// --- helpers ---

// stripLineNumber removes "cat -n" style line number prefix ("   42\t" → content).
func stripLineNumber(line string) string {
	// cat -n format: spaces + digits + tab + content
	trimmed := strings.TrimLeft(line, " ")
	if idx := strings.IndexByte(trimmed, '\t'); idx >= 0 {
		prefix := trimmed[:idx]
		if isAllDigits(prefix) {
			return trimmed[idx+1:]
		}
	}
	return line
}

// extractFuncName extracts the function name from a Go func declaration.
func extractFuncName(line string) string {
	// "func Name(" or "func (r *T) Name("
	s := line
	if strings.HasPrefix(s, "func (") {
		// Skip receiver
		closeIdx := strings.IndexByte(s, ')')
		if closeIdx < 0 {
			return ""
		}
		s = strings.TrimSpace(s[closeIdx+1:])
	} else {
		s = strings.TrimPrefix(s, "func ")
	}
	// Now s starts with "Name(" or "Name[T]("
	if parenIdx := strings.IndexAny(s, "(["); parenIdx > 0 {
		return s[:parenIdx]
	}
	if spaceIdx := strings.IndexByte(s, ' '); spaceIdx > 0 {
		return s[:spaceIdx]
	}
	return ""
}

// extractTypeName extracts the type name from a Go type declaration.
func extractTypeName(line string) string {
	// "type Name struct {" or "type Name interface {"
	s := strings.TrimPrefix(line, "type ")
	if spaceIdx := strings.IndexByte(s, ' '); spaceIdx > 0 {
		return s[:spaceIdx]
	}
	return ""
}

// isHexPrefix checks if a string looks like hex characters (for commit hash detection).
func isHexPrefix(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isAllDigits checks if a string consists entirely of digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
