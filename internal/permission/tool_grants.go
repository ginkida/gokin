package permission

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

const (
	MaxTemporaryToolGrantRules = 64
	MaxTemporaryToolGrantBytes = 8 * 1024
	MaxTemporaryToolGrantRule  = 512
)

// ParseTemporaryToolGrantList parses Claude-compatible allowed-tools text.
// Whitespace and commas separate rules only outside (...) so a rule such as
// Bash(git status --short) remains one entry.
func ParseTemporaryToolGrantList(value string) ([]string, error) {
	raw, err := splitTemporaryToolRules(value, "allowed-tools")
	if err != nil {
		return nil, err
	}
	return CanonicalizeTemporaryToolGrants(raw)
}

// ParseTemporaryToolDenyList parses run/skill deny rules. Unlike allows, deny
// names may contain '*' so mcp__* and * can block late-added tools too.
func ParseTemporaryToolDenyList(value string) ([]string, error) {
	raw, err := splitTemporaryToolRules(value, "disallowed-tools")
	if err != nil {
		return nil, err
	}
	return CanonicalizeTemporaryToolDenies(raw)
}

func splitTemporaryToolRules(value, field string) ([]string, error) {
	var raw []string
	start := -1
	depth := 0
	for index, r := range value {
		switch {
		case r == '(':
			if start < 0 {
				start = index
			}
			depth++
		case r == ')':
			if depth == 0 {
				return nil, fmt.Errorf("%s contains an unmatched ')'", field)
			}
			depth--
		case depth == 0 && (unicode.IsSpace(r) || r == ','):
			if start >= 0 {
				raw = append(raw, value[start:index])
				start = -1
			}
		default:
			if start < 0 {
				start = index
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%s contains an unterminated '('", field)
	}
	if start >= 0 {
		raw = append(raw, value[start:])
	}
	return raw, nil
}

// CanonicalizeTemporaryToolGrants validates, normalizes, deduplicates and
// bounds one temporary skill permission grant list.
func CanonicalizeTemporaryToolGrants(raw []string) ([]string, error) {
	if len(raw) > MaxTemporaryToolGrantRules {
		return nil, fmt.Errorf("allowed-tools exceeds %d rules", MaxTemporaryToolGrantRules)
	}
	seen := make(map[string]bool, len(raw))
	result := make([]string, 0, len(raw))
	total := 0
	for _, value := range raw {
		canonical, err := canonicalTemporaryToolGrant(value)
		if err != nil {
			return nil, err
		}
		if canonical == "" || seen[canonical] {
			continue
		}
		total += len(canonical)
		if total > MaxTemporaryToolGrantBytes {
			return nil, fmt.Errorf("allowed-tools exceeds %d bytes", MaxTemporaryToolGrantBytes)
		}
		seen[canonical] = true
		result = append(result, canonical)
	}
	return result, nil
}

// CanonicalizeTemporaryToolDenies validates and bounds deny rules while
// permitting '*' in the tool-name portion.
func CanonicalizeTemporaryToolDenies(raw []string) ([]string, error) {
	if len(raw) > MaxTemporaryToolGrantRules {
		return nil, fmt.Errorf("disallowed-tools exceeds %d rules", MaxTemporaryToolGrantRules)
	}
	seen := make(map[string]bool, len(raw))
	result := make([]string, 0, len(raw))
	total := 0
	for _, value := range raw {
		canonical, err := canonicalTemporaryToolRule(value, true, "disallowed-tools")
		if err != nil {
			return nil, err
		}
		if canonical == "" || seen[canonical] {
			continue
		}
		total += len(canonical)
		if total > MaxTemporaryToolGrantBytes {
			return nil, fmt.Errorf("disallowed-tools exceeds %d bytes", MaxTemporaryToolGrantBytes)
		}
		seen[canonical] = true
		result = append(result, canonical)
	}
	return result, nil
}

func canonicalTemporaryToolGrant(value string) (string, error) {
	return canonicalTemporaryToolRule(value, true, "allowed-tools")
}

func canonicalTemporaryToolRule(value string, allowWildcardName bool, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > MaxTemporaryToolGrantRule {
		return "", fmt.Errorf("%s rule exceeds %d bytes", field, MaxTemporaryToolGrantRule)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%s rule contains a control character", field)
	}

	name := value
	pattern := ""
	if open := strings.IndexByte(value, '('); open >= 0 {
		if open == 0 || !strings.HasSuffix(value, ")") {
			return "", fmt.Errorf("invalid %s rule %q", field, value)
		}
		name = strings.TrimSpace(value[:open])
		pattern = value[open+1 : len(value)-1]
		if strings.TrimSpace(pattern) == "" {
			return "", fmt.Errorf("%s rule %q has an empty argument pattern", field, value)
		}
		if strings.ContainsAny(pattern, "()") {
			return "", fmt.Errorf("%s rule %q has nested parentheses", field, value)
		}
	}

	name = canonicalGrantToolNameWithWildcards(name, allowWildcardName)
	if name == "" {
		return "", fmt.Errorf("invalid %s tool name in %q", field, value)
	}
	if pattern != "" {
		if strings.ContainsRune(name, '*') {
			return "", fmt.Errorf("argument-scoped %s rule %q cannot wildcard the tool name", field, value)
		}
		if err := validateScopedToolRule(name, pattern); err != nil {
			return "", fmt.Errorf("invalid %s rule %q: %w", field, value, err)
		}
		if name == "bash" && pattern == "*" {
			return name, nil
		}
		return name + "(" + pattern + ")", nil
	}
	return name, nil
}

func canonicalGrantToolName(value string) string {
	return canonicalGrantToolNameWithWildcards(value, false)
}

func canonicalGrantToolNameWithWildcards(value string, allowWildcards bool) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	var previous rune
	hasPrevious := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			if unicode.IsUpper(r) && hasPrevious {
				if unicode.IsLower(previous) || unicode.IsDigit(previous) {
					builder.WriteByte('_')
				}
			}
			builder.WriteRune(unicode.ToLower(r))
		case r == '_', r == '-', r == ':', allowWildcards && r == '*':
			builder.WriteRune(r)
		default:
			return ""
		}
		previous = r
		hasPrevious = true
	}
	name := strings.ReplaceAll(builder.String(), "-", "_")
	switch name {
	case "ask_user_question":
		return "ask_user"
	case "agent":
		return "task"
	}
	return name
}

// CheckWithTemporaryToolGrants applies a turn-scoped skill pre-approval.
// Parent/config deny and parent session deny remain authoritative because the
// scoped manager is bounded by WithPolicyOverrides. Elevated Bash calls still
// pass through Check's action-semantics confirmation floor.
func (m *Manager) CheckWithTemporaryToolGrants(
	ctx context.Context,
	toolName string,
	args map[string]any,
	grants []string,
) (*Response, error) {
	return m.CheckWithTemporaryToolRules(ctx, toolName, args, grants, nil)
}

// CheckWithTemporaryToolRules applies one turn's skill permission rules.
// Denies are restrictions and therefore remain authoritative even when normal
// permission prompts are disabled. Grants only suppress prompts and retain all
// parent/config/run/session safety floors.
func (m *Manager) CheckWithTemporaryToolRules(
	ctx context.Context,
	toolName string,
	args map[string]any,
	grants []string,
	denies []string,
) (*Response, error) {
	workDir := WorkDirFromContext(ctx)
	if temporaryToolDenyMatchesAny(denies, toolName, args, workDir) {
		return &Response{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   "Tool is denied by the active skill",
		}, nil
	}
	for _, grant := range grants {
		if temporaryToolGrantMatches(grant, toolName, args, workDir) {
			scoped := m.WithPolicyOverrides(map[string]Level{toolName: LevelAllow})
			return scoped.Check(ctx, toolName, args)
		}
	}
	return m.Check(ctx, toolName, args)
}

func temporaryToolGrantMatches(grant, toolName string, args map[string]any, workDir string) bool {
	name := grant
	pattern := ""
	if open := strings.IndexByte(grant, '('); open >= 0 && strings.HasSuffix(grant, ")") {
		name = grant[:open]
		pattern = grant[open+1 : len(grant)-1]
	}
	if !toolRuleNameMatches(name, toolName) {
		return false
	}
	if pattern == "" {
		return true
	}
	return scopedToolRuleMatches(name, pattern, toolName, args, workDir, false)
}

func temporaryToolGrantMatchesAny(grants []string, toolName string, args map[string]any, workDir string) bool {
	for _, grant := range grants {
		if temporaryToolGrantMatches(grant, toolName, args, workDir) {
			return true
		}
	}
	return false
}

func temporaryToolDenyMatchesAny(denies []string, toolName string, args map[string]any, workDir string) bool {
	for _, deny := range denies {
		name := deny
		pattern := ""
		if open := strings.IndexByte(deny, '('); open >= 0 && strings.HasSuffix(deny, ")") {
			name = deny[:open]
			pattern = deny[open+1 : len(deny)-1]
		}
		if !toolRuleNameMatches(name, toolName) {
			continue
		}
		if pattern == "" {
			return true
		}
		if scopedToolRuleMatches(name, pattern, toolName, args, workDir, true) {
			return true
		}
	}
	return false
}

// ToolDenyRuleMatchesName reports whether a bare (non-argument-scoped) deny
// rule matches a canonical/runtime tool name. CLI schema filtering uses the
// same matcher as runtime enforcement.
func ToolDenyRuleMatchesName(rule, toolName string) bool {
	if strings.ContainsRune(rule, '(') {
		return false
	}
	return toolRuleNameMatches(rule, toolName)
}

// wildcardMatch implements the '*' subset used by Bash permission patterns in
// linear time and treats every other byte literally.
func wildcardMatch(pattern, value string) bool {
	patternIndex, valueIndex := 0, 0
	starIndex, retryValueIndex := -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			patternIndex++
			retryValueIndex = valueIndex
			continue
		}
		if starIndex >= 0 {
			patternIndex = starIndex + 1
			retryValueIndex++
			valueIndex = retryValueIndex
			continue
		}
		return false
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}
