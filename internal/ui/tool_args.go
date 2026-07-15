package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func toolStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return strings.TrimSpace(safeInlineDisplayText(value))
}

func toolBoolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	value, _ := args[key].(bool)
	return value
}

func toolIntArg(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func toolStringListArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch values := args[key].(type) {
	case []string:
		result := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(safeInlineDisplayText(value))
			if value != "" {
				result = append(result, value)
			}
		}
		return result
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				s = strings.TrimSpace(safeInlineDisplayText(s))
				if s != "" {
					result = append(result, s)
				}
			}
		}
		return result
	default:
		return nil
	}
}

func compactInline(text string, maxLen int) string {
	text = strings.TrimSpace(safeInlineDisplayText(text))
	if maxLen <= 0 {
		return text
	}
	if lipgloss.Width(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return truncateForWidth(text, maxLen) // use the compact single-cell ellipsis
	}
	return displayCellPrefix(text, maxLen-3) + "..."
}

func formatPathPair(source, destination string, maxLen int) string {
	source = strings.TrimSpace(safeInlineDisplayText(source))
	destination = strings.TrimSpace(safeInlineDisplayText(destination))
	if source == "" && destination == "" {
		return ""
	}
	if source == "" {
		return shortenPath(destination, maxLen)
	}
	if destination == "" {
		return shortenPath(source, maxLen)
	}
	combined := source + " -> " + destination
	if maxLen <= 0 || lipgloss.Width(combined) <= maxLen {
		return combined
	}
	const separator = " -> "
	separatorWidth := lipgloss.Width(separator)
	if maxLen <= separatorWidth {
		return truncateForWidth("→", maxLen)
	}
	pathBudget := maxLen - separatorWidth
	sourceBudget := pathBudget / 2
	destinationBudget := pathBudget - sourceBudget
	return shortenPath(source, sourceBudget) + separator + shortenPath(destination, destinationBudget)
}

func formatRunTestsTarget(args map[string]any, pathLimit int) string {
	path := toolStringArg(args, "path")
	if path == "" {
		path = "."
	}
	var parts []string
	if framework := toolStringArg(args, "framework"); framework != "" && framework != "auto" {
		parts = append(parts, framework)
	}
	parts = append(parts, shortenPath(path, pathLimit))
	if filter := toolStringArg(args, "filter"); filter != "" {
		parts = append(parts, "filter="+compactInline(filter, 28))
	}
	if toolBoolArg(args, "coverage") {
		parts = append(parts, "coverage")
	}
	return strings.Join(parts, " ")
}

func formatGitDiffTarget(args map[string]any, maxLen int) string {
	var parts []string
	if file := toolStringArg(args, "file"); file != "" {
		parts = append(parts, shortenPath(file, maxLen))
	} else {
		from := toolStringArg(args, "from")
		to := toolStringArg(args, "to")
		switch {
		case from != "" && to != "":
			parts = append(parts, compactInline(from+".."+to, maxLen))
		case from != "":
			parts = append(parts, compactInline(from+"..working tree", maxLen))
		case toolBoolArg(args, "staged"):
			parts = append(parts, "staged changes")
		default:
			parts = append(parts, "working tree")
		}
	}
	if toolBoolArg(args, "name_status") {
		parts = append(parts, "--name-status")
	}
	return strings.Join(parts, " ")
}

func formatGitAddTarget(args map[string]any, maxLen int) string {
	switch {
	case toolBoolArg(args, "all"):
		return "-A"
	case toolBoolArg(args, "update"):
		return "-u"
	}
	paths := toolStringListArg(args, "paths")
	if len(paths) == 1 {
		return shortenPath(paths[0], maxLen)
	}
	if len(paths) > 1 {
		return fmt.Sprintf("%d paths", len(paths))
	}
	return ""
}

func displayToolName(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(safeInlineDisplayText(name)), "-", "_")
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func statusToolLabel(name string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(safeInlineDisplayText(name)), "-", "_"))
	switch normalized {
	case "":
		return ""
	case "run_tests":
		return "TESTS"
	case "verify_code":
		return "VERIFY"
	default:
		return strings.ToUpper(strings.ReplaceAll(normalized, "_", " "))
	}
}
