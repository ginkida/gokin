package hooks

import (
	"regexp"
	"strings"
)

// Claude's ordinary event matchers use exact names/lists unless the pattern
// contains regex metacharacters. Regex patterns remain unanchored.
func compileClaudeMatcher(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || pattern == "*" {
		return nil, nil
	}
	exact := true
	for _, c := range pattern {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_- ,|", c)) {
			exact = false
			break
		}
	}
	if exact {
		values := strings.FieldsFunc(pattern, func(c rune) bool { return c == '|' || c == ',' })
		for i := range values {
			values[i] = regexp.QuoteMeta(strings.TrimSpace(values[i]))
		}
		pattern = "^(?:" + strings.Join(values, "|") + ")$"
	}
	return regexp.Compile(pattern)
}
