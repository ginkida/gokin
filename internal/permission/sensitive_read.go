package permission

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gokin/internal/security"
)

// sensitiveReadPaths names only file targets that can return credential-like
// content. Directory searches apply their own traversal exclusions instead.
func sensitiveReadPaths(tool string, args map[string]any, workDir string) []string {
	var keys []string
	switch tool {
	case "read":
		keys = []string{"file_path"}
	case "diff":
		keys = []string{"file1", "file2"}
	case "git_diff", "git_blame", "go_to_definition", "find_references", "go_search":
		keys = []string{"file"}
	case "lsp":
		keys = []string{"filePath"}
	case "grep":
		keys = []string{"path"}
	default:
		return nil
	}
	var paths []string
	for _, key := range keys {
		raw, _ := args[key].(string)
		if raw == "" {
			continue
		}
		path := raw
		if !filepath.IsAbs(path) && workDir != "" {
			path = filepath.Join(workDir, path)
		}
		path = filepath.Clean(path)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			continue
		}
		sensitive, _ := security.ClassifySensitivePath(path)
		resolved, err := resolvePermissionPath(path)
		if err == nil {
			if matched, _ := security.ClassifySensitivePath(resolved); matched {
				sensitive = true
			}
		}
		if sensitive {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func sensitiveReadHint(paths []string, workDir string) string {
	if len(paths) == 0 {
		return ""
	}
	path := paths[0]
	specifier := filepath.ToSlash(path)
	if filepath.IsAbs(path) {
		specifier = "/" + specifier // Claude absolute path rules start with //.
		if rel, err := filepath.Rel(workDir, path); workDir != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			specifier = "./" + filepath.ToSlash(rel)
		}
	} else {
		specifier = "./" + specifier
	}
	return fmt.Sprintf("credential-like file %s withheld; pass --allowed-tools 'Read(%s)' to allow", filepath.Base(path), specifier)
}

// A bare tool allow is insufficient: every sensitive file must have a matching
// path-scoped run rule. Skill/config grants do not enter this process-only set.
func sensitiveReadRunAllowed(rules []string, tool string, paths []string, workDir string) bool {
	for _, path := range paths {
		matched := false
		for _, rule := range rules {
			open := strings.IndexByte(rule, '(')
			if open <= 0 || !strings.HasSuffix(rule, ")") {
				continue
			}
			name, pattern := rule[:open], rule[open+1:len(rule)-1]
			if pattern != "" && toolRuleNameMatches(name, tool) && isPathRuleName(name) && permissionPathPatternMatches(pattern, path, workDir, false) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return len(paths) > 0
}
