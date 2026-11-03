package tools

import (
	"fmt"
	"strings"
)

// Presets are capability ceilings, not permission grants. Keep membership
// explicit so adding a tool to the registry never silently widens a preset.
var toolPresets = map[string][]string{
	"@read": {"read", "grep", "glob", "list_dir", "tree", "diff", "git_status", "git_diff", "git_log", "git_blame", "review_changes", "go_to_definition", "find_references", "go_search", "todo"},
	"@test": {"@read", "bash", "run_tests", "verify_code"},
	"@edit": {"@test", "edit", "write", "mkdir"},
}

// ExpandToolPresets expands reserved --tools tokens while preserving the
// nil/empty distinction and the first occurrence of each tool.
func ExpandToolPresets(names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}
	result := make([]string, 0, len(names))
	seen := make(map[string]bool)
	var expand func(string) error
	expand = func(name string) error {
		name = strings.TrimSpace(name)
		if strings.HasPrefix(name, "@") {
			members, ok := toolPresets[name]
			if !ok {
				return fmt.Errorf("unknown --tools preset %q (want @read, @test, or @edit)", name)
			}
			for _, member := range members {
				if err := expand(member); err != nil {
					return err
				}
			}
		} else if name != "" && !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
		return nil
	}
	for _, name := range names {
		if err := expand(name); err != nil {
			return nil, err
		}
	}
	return result, nil
}
