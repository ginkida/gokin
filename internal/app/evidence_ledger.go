package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"gokin/internal/tools"
)

const responseEvidenceMaxEntries = 12

type responseEvidenceLedger struct {
	ReadPaths []string
	Searches  []string
}

type responseEvidenceSnapshot struct {
	ToolsUsed            []string
	ReadPaths            []string
	Searches             []string
	TouchedPaths         []string
	Commands             []string
	VerificationCommands []string
}

func (a *App) recordResponseEvidence(toolName string, args map[string]any, result tools.ToolResult) {
	if !result.Success || len(args) == 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	switch toolName {
	case "read":
		paths := normalizeDoneGateTouchedPaths(a.workDir, extractDoneGateTouchedPaths(args))
		for _, path := range paths {
			a.responseEvidence.ReadPaths = appendUniqueLimited(a.responseEvidence.ReadPaths, path, responseEvidenceMaxEntries)
		}
	case "grep", "glob", "tree", "list_dir":
		if entry := formatEvidenceSearch(toolName, args); entry != "" {
			a.responseEvidence.Searches = appendUniqueLimited(a.responseEvidence.Searches, entry, responseEvidenceMaxEntries)
		}
	}
}

func (a *App) snapshotResponseEvidence() responseEvidenceSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := responseEvidenceSnapshot{
		ToolsUsed:    copyStrings(a.responseToolsUsed),
		ReadPaths:    copyStrings(a.responseEvidence.ReadPaths),
		Searches:     copyStrings(a.responseEvidence.Searches),
		TouchedPaths: copyStrings(a.responseTouchedPaths),
		Commands:     copyStrings(a.responseCommands),
	}
	s.VerificationCommands = pickVerificationCommands(s.Commands, s.ToolsUsed)
	return s
}

func (s responseEvidenceSnapshot) FormatForCompletionReview() string {
	var lines []string
	if len(s.ToolsUsed) > 0 {
		lines = append(lines, "- tools_used: "+joinLimited(s.ToolsUsed, 8))
	}
	if len(s.ReadPaths) > 0 {
		lines = append(lines, "- files_read: "+joinLimited(s.ReadPaths, 8))
	}
	if len(s.Searches) > 0 {
		lines = append(lines, "- searches: "+joinLimited(s.Searches, 8))
	}
	if len(s.TouchedPaths) > 0 {
		lines = append(lines, "- files_changed_by_tools: "+joinLimited(s.TouchedPaths, 8))
	}
	if len(s.Commands) > 0 {
		lines = append(lines, "- commands: "+joinLimited(s.Commands, 5))
	}
	if len(s.VerificationCommands) > 0 {
		lines = append(lines, "- verification: "+joinLimited(s.VerificationCommands, 5))
	}
	return strings.Join(lines, "\n")
}

func formatEvidenceSearch(toolName string, args map[string]any) string {
	switch toolName {
	case "grep":
		pattern := trimArg(args, "pattern")
		if pattern == "" {
			return ""
		}
		target := `grep "` + truncateEvidenceValue(pattern, 80) + `"`
		if glob := trimArg(args, "glob"); glob != "" {
			target += " in " + truncateEvidenceValue(glob, 80)
		} else if path := trimArg(args, "path"); path != "" {
			target += " in " + cleanEvidencePath(path)
		}
		return target
	case "glob":
		pattern := trimArg(args, "pattern")
		if pattern == "" {
			return ""
		}
		return `glob "` + truncateEvidenceValue(pattern, 100) + `"`
	case "tree", "list_dir":
		path := trimArg(args, "path")
		if path == "" {
			path = trimArg(args, "dir")
		}
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("%s %s", toolName, cleanEvidencePath(path))
	}
	return ""
}

func trimArg(args map[string]any, key string) string {
	v, ok := args[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func cleanEvidencePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func truncateEvidenceValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func appendUniqueLimited(items []string, value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, existing := range items {
		if existing == value {
			return items
		}
	}
	if limit > 0 && len(items) >= limit {
		return items
	}
	return append(items, value)
}

func copyStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}
