package permission

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

var readRuleTools = map[string]bool{
	"read": true, "grep": true, "glob": true, "list_dir": true, "tree": true,
}

var editRuleTools = map[string]bool{
	"edit": true, "write": true, "batch": true,
	"refactor": true, "copy": true, "move": true, "delete": true, "mkdir": true,
}

func validateScopedToolRule(name, pattern string) error {
	switch {
	case name == "bash":
		return nil
	case name == "web_fetch":
		domain := strings.TrimSpace(strings.TrimPrefix(pattern, "domain:"))
		if !strings.HasPrefix(pattern, "domain:") || domain == "" ||
			strings.ContainsAny(domain, "/?#@* \t") {
			return fmt.Errorf("WebFetch specifier must be domain:<hostname>")
		}
		parsed, err := url.Parse("https://" + domain)
		if err != nil || parsed.Hostname() == "" || parsed.Port() != "" {
			return fmt.Errorf("WebFetch specifier must contain one hostname without a port")
		}
		return nil
	case name == "task":
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("Agent specifier must name an agent type")
		}
		return nil
	case isPathRuleName(name):
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("path specifier must not be empty")
		}
		if !doublestar.ValidatePattern(filepath.ToSlash(pattern)) {
			return fmt.Errorf("path specifier is not a valid glob")
		}
		return nil
	default:
		return fmt.Errorf("argument-scoped rules are unsupported for tool %q", name)
	}
}

func isPathRuleName(name string) bool {
	return readRuleTools[name] || editRuleTools[name]
}

// claudeMCPRulePrefix is how Claude Code names MCP tools. Gokin registers them
// under its own `<server>_<tool>` names, so a rule written in the Claude form
// used to match nothing at all and was accepted as a silent no-op — the worst
// possible outcome for a rule whose entire purpose is to take authority away.
const claudeMCPRulePrefix = "mcp__"

// claudeMCPRuleMatches resolves a Claude-style MCP rule against a Gokin runtime
// tool name. The second return distinguishes "this is an MCP rule and it did
// not match" from "this is not an MCP rule" so the caller can fall through to
// ordinary name matching.
//
// Accepted forms:
//
//	mcp__*                     every tool registered by any MCP server
//	mcp__<server>              every tool of that server
//	mcp__<server>__*           every tool of that server
//	mcp__<server>__<tool>      that exact tool
//
// A server/tool segment is matched against Gokin's `<server>_<tool>` naming,
// and membership is confirmed against the MCP registration table so a rule can
// never reach a built-in tool that merely shares a prefix.
func claudeMCPRuleMatches(ruleName, runtimeName string) (matched, isMCPRule bool) {
	if !strings.HasPrefix(ruleName, claudeMCPRulePrefix) {
		return false, false
	}
	// A runtime name that is already in the Claude form matches structurally —
	// that is plain wildcard matching and needs no registration evidence.
	if strings.HasPrefix(runtimeName, claudeMCPRulePrefix) {
		return wildcardMatch(ruleName, runtimeName), true
	}
	// Otherwise the rule can only be about Gokin's own `<server>_<tool>` names,
	// and only a tool the MCP registration table knows may be reached — a
	// built-in that merely shares a server-like prefix must stay out of range.
	if !IsMCPToolName(runtimeName) {
		return false, true
	}
	specifier := strings.TrimPrefix(ruleName, claudeMCPRulePrefix)
	if specifier == "" || specifier == "*" {
		return true, true
	}

	server, tool, hasTool := strings.Cut(specifier, "__")
	server = canonicalGrantToolNameWithWildcards(server, true)
	if server == "" {
		return false, true
	}
	if !hasTool || tool == "" || tool == "*" {
		// Every tool of that server: Gokin prefixes them with `<server>_`.
		return wildcardMatch(server+"_*", runtimeName), true
	}
	tool = canonicalGrantToolNameWithWildcards(tool, true)
	if tool == "" {
		return false, true
	}
	return wildcardMatch(server+"_"+tool, runtimeName), true
}

func toolRuleNameMatches(ruleName, runtimeName string) bool {
	runtimeName = canonicalGrantToolName(runtimeName)
	if matched, isMCPRule := claudeMCPRuleMatches(ruleName, runtimeName); isMCPRule {
		return matched
	}
	if strings.ContainsRune(ruleName, '*') {
		return wildcardMatch(ruleName, runtimeName)
	}
	switch ruleName {
	case "read":
		return readRuleTools[runtimeName]
	case "edit":
		return editRuleTools[runtimeName]
	default:
		return ruleName == runtimeName
	}
}

func scopedToolRuleMatches(
	ruleName string,
	pattern string,
	runtimeName string,
	args map[string]any,
	workDir string,
	deny bool,
) bool {
	runtimeName = canonicalGrantToolName(runtimeName)
	switch ruleName {
	case "bash":
		command, ok := args["command"].(string)
		return ok && bashPermissionRuleMatches(pattern, command, deny)
	case "web_fetch":
		if runtimeName != "web_fetch" {
			return false
		}
		return webFetchDomainMatches(pattern, args)
	case "task":
		if runtimeName != "task" {
			return false
		}
		return agentTypeMatches(pattern, args, deny)
	default:
		if !isPathRuleName(ruleName) {
			return false
		}
		return pathRuleMatches(pattern, runtimeName, args, workDir, deny)
	}
}

// bashShellOperatorBytes are the characters through which one shell command
// line reaches beyond the program the rule named — by chaining another program
// (`|`, `&`, `;`, newline) or by redirecting into a file (`<`, `>`). A
// pre-approval's `*` must never expand across them: `Bash(git status *)` grants
// inspection, not `git status && curl … | sh` and not `git status > ~/.bashrc`.
//
// `(` is deliberately absent: without quote parsing it appears far more often
// inside ordinary arguments (`grep 'func (e '`) than as a subshell, and a
// subshell still cannot match a pattern whose literal prefix names a program.
const bashShellOperatorBytes = "|&;\n\r<>"

// bashPermissionRuleMatches evaluates one argument-scoped Bash rule.
//
// The two directions are deliberately asymmetric, because an over-match and an
// under-match have opposite consequences:
//
//   - A pre-approval (deny=false) must be conservative. It matches only the
//     command AS A WHOLE, with wildcards that cannot expand across a shell
//     operator or a command substitution, so a granted prefix can never carry
//     an unrelated program in behind it.
//   - A deny (deny=true) must be greedy. It matches the whole command and every
//     individual segment of it, so `cd . && git push` and a leading space can
//     no longer walk past a `Bash(git push *)` rule.
func bashPermissionRuleMatches(pattern, command string, deny bool) bool {
	if deny {
		if bashPermissionPatternMatches(pattern, command, false) {
			return true
		}
		for _, segment := range splitBashSegments(command) {
			if bashPermissionPatternMatches(pattern, segment, false) {
				return true
			}
		}
		return false
	}
	// Drop the redirections that cannot write a user file before the operator
	// check, exactly as the read-only classifier does — models append
	// `2>/dev/null` and `2>&1` to inspection commands constantly, and refusing
	// to pre-approve those would only trade safety for noise.
	command = harmlessBashRedirectRE.ReplaceAllString(command, " ")
	return bashPermissionPatternMatches(strings.TrimSpace(pattern), strings.TrimSpace(command), true)
}

// bashPermissionPatternMatches matches one pattern against one command string.
// When restrictStar is set, `*` may not expand across a shell operator or a
// command substitution.
func bashPermissionPatternMatches(pattern, command string, restrictStar bool) bool {
	if pattern == "*" {
		return !restrictStar || !bashCommandChainsPrograms(command)
	}
	if strings.HasSuffix(pattern, ":*") {
		prefix := strings.TrimSuffix(pattern, ":*")
		if command != prefix && !strings.HasPrefix(command, prefix+" ") {
			return false
		}
		return !restrictStar || !bashCommandChainsPrograms(strings.TrimPrefix(command, prefix))
	}
	if strings.HasSuffix(pattern, " *") {
		prefix := strings.TrimSuffix(pattern, " *")
		if command == prefix {
			return true
		}
	}
	if !restrictStar {
		return wildcardMatch(pattern, command)
	}
	return wildcardMatchRestricted(pattern, command, bashShellOperatorBytes)
}

// bashCommandChainsPrograms reports whether the text can launch a program other
// than the one the pattern named.
func bashCommandChainsPrograms(text string) bool {
	return strings.ContainsAny(text, bashShellOperatorBytes) ||
		strings.Contains(text, "`") ||
		strings.Contains(text, "$(")
}

// wildcardMatchRestricted is wildcardMatch with a bounded `*`: the star may
// consume any run of characters that contains none of forbidden, and never
// crosses a command substitution.
func wildcardMatchRestricted(pattern, value, forbidden string) bool {
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
			// Expanding the star must not swallow an operator or a substitution.
			if strings.IndexByte(forbidden, value[retryValueIndex]) >= 0 {
				return false
			}
			if value[retryValueIndex] == '`' {
				return false
			}
			if value[retryValueIndex] == '$' && retryValueIndex+1 < len(value) &&
				value[retryValueIndex+1] == '(' {
				return false
			}
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

func webFetchDomainMatches(pattern string, args map[string]any) bool {
	domain := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(pattern, "domain:")))
	domain = strings.Trim(domain, "[]")
	rawURL, _ := args["url"].(string)
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	domain = strings.TrimSuffix(domain, ".")
	return host == domain
}

func agentTypeMatches(pattern string, args map[string]any, deny bool) bool {
	agentType, _ := args["subagent_type"].(string)
	agentType = strings.ToLower(strings.TrimSpace(agentType))
	if agentType == "" {
		// A resume call does not expose the saved agent's type at this boundary.
		// Never grant scoped authority to it; conservatively honor a deny.
		resume, _ := args["resume"].(string)
		return deny && strings.TrimSpace(resume) != ""
	}
	return wildcardMatch(strings.ToLower(pattern), agentType)
}

func pathRuleMatches(
	pattern string,
	runtimeName string,
	args map[string]any,
	workDir string,
	deny bool,
) bool {
	targets, complete := permissionPathTargets(runtimeName, args, workDir)
	if !complete || len(targets) == 0 {
		// Refactor-like tools that cannot declare their affected files before
		// execution never receive a scoped allow. A scoped deny remains a
		// conservative boundary for those tools.
		return deny && editRuleTools[runtimeName]
	}
	for _, target := range targets {
		if deny && permissionOperationMayReachPattern(
			pattern, target, workDir, runtimeName,
		) {
			return true
		}
		matched := permissionPathPatternMatches(pattern, target, workDir, deny)
		if deny && matched {
			return true
		}
		if !deny && !matched {
			return false
		}
	}
	return !deny
}

func permissionOperationMayReachPattern(pattern, target, workDir, runtimeName string) bool {
	if runtimeName == "read" {
		return false
	}
	if !readRuleTools[runtimeName] && !editRuleTools[runtimeName] {
		return false
	}
	absolutePattern, ok := absolutePermissionPattern(pattern, workDir)
	if !ok {
		return false
	}
	patternPrefix := permissionGlobStaticPrefix(absolutePattern)
	absoluteTarget, ok := absolutePermissionTarget(target, workDir)
	if !ok {
		return false
	}
	prefixCandidates := []string{patternPrefix}
	if resolved, err := resolvePermissionPath(patternPrefix); err == nil &&
		resolved != patternPrefix {
		prefixCandidates = append(prefixCandidates, resolved)
	}
	targetCandidates := []string{absoluteTarget}
	if resolved, err := resolvePermissionPath(absoluteTarget); err == nil &&
		resolved != absoluteTarget {
		targetCandidates = append(targetCandidates, resolved)
	}
	for _, targetCandidate := range targetCandidates {
		for _, prefixCandidate := range prefixCandidates {
			if permissionPathWithin(prefixCandidate, targetCandidate) {
				return true
			}
		}
	}
	return false
}

func permissionGlobStaticPrefix(pattern string) string {
	globIndex := strings.IndexAny(pattern, "*?[{")
	if globIndex < 0 {
		return filepath.Clean(pattern)
	}
	prefix := pattern[:globIndex]
	separator := strings.LastIndexAny(prefix, `/\`)
	if separator < 0 {
		return filepath.Clean(prefix)
	}
	if separator == 0 {
		return string(filepath.Separator)
	}
	return filepath.Clean(prefix[:separator])
}

func permissionPathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func permissionPathTargets(runtimeName string, args map[string]any, workDir string) ([]string, bool) {
	stringArg := func(key string) (string, bool) {
		value, ok := args[key].(string)
		value = strings.TrimSpace(value)
		return value, ok && value != ""
	}
	switch runtimeName {
	case "read", "write", "edit":
		value, ok := stringArg("file_path")
		return []string{value}, ok
	case "grep", "glob", "list_dir", "tree":
		if value, ok := stringArg("path"); ok {
			return []string{value}, true
		}
		if runtimeName == "glob" {
			if value, ok := stringArg("pattern"); ok {
				prefix := permissionGlobStaticPrefix(filepath.FromSlash(value))
				if prefix != "." && prefix != string(filepath.Separator) {
					return []string{filepath.Join(workDir, prefix)}, true
				}
			}
		}
		if strings.TrimSpace(workDir) != "" {
			return []string{workDir}, true
		}
		return nil, false
	case "delete", "mkdir":
		value, ok := stringArg("path")
		return []string{value}, ok
	case "copy", "move":
		source, sourceOK := stringArg("source")
		destination, destinationOK := stringArg("destination")
		return []string{source, destination}, sourceOK && destinationOK
	case "batch":
		rawOperations, ok := args["operations"].([]any)
		if !ok || len(rawOperations) == 0 {
			return nil, false
		}
		targets := make([]string, 0, len(rawOperations))
		for _, raw := range rawOperations {
			operation, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			value, ok := operation["file_path"].(string)
			value = strings.TrimSpace(value)
			if !ok || value == "" {
				return nil, false
			}
			targets = append(targets, value)
		}
		return targets, true
	case "refactor":
		return nil, false
	default:
		return nil, false
	}
}

func permissionPathPatternMatches(pattern, target, workDir string, deny bool) bool {
	absolutePattern, ok := absolutePermissionPattern(pattern, workDir)
	if !ok {
		return false
	}
	absoluteTarget, ok := absolutePermissionTarget(target, workDir)
	if !ok {
		return false
	}
	patterns := []string{absolutePattern}
	if resolved, err := resolvePermissionGlobPattern(absolutePattern); err == nil &&
		resolved != absolutePattern {
		patterns = append(patterns, resolved)
	}
	candidates := []string{absoluteTarget}
	if resolved, err := resolvePermissionPath(absoluteTarget); err == nil &&
		resolved != absoluteTarget {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		candidateMatched := false
		for _, candidatePattern := range patterns {
			matched, err := doublestar.Match(
				permissionSlashPath(candidatePattern),
				permissionSlashPath(candidate),
			)
			if err != nil {
				return false
			}
			if matched {
				candidateMatched = true
				break
			}
		}
		if deny && candidateMatched {
			return true
		}
		if !deny && !candidateMatched {
			return false
		}
	}
	return !deny
}

func permissionSlashPath(value string) string {
	value = filepath.ToSlash(value)
	value = strings.ReplaceAll(value, `\`, "/")
	if len(value) >= 3 && value[1] == ':' && value[2] == '/' {
		drive := strings.ToLower(value[:1])
		return "/" + drive + value[2:]
	}
	return value
}

func resolvePermissionGlobPattern(pattern string) (string, error) {
	globIndex := strings.IndexAny(pattern, "*?[{")
	if globIndex < 0 {
		return resolvePermissionPath(pattern)
	}
	prefix := pattern[:globIndex]
	separator := strings.LastIndexAny(prefix, `/\`)
	if separator < 0 {
		return "", fmt.Errorf("glob pattern has no resolvable path prefix")
	}
	base := prefix[:separator]
	if base == "" {
		base = string(filepath.Separator)
	}
	resolvedBase, err := resolvePermissionPath(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedBase, filepath.FromSlash(pattern[separator+1:])), nil
}

func absolutePermissionPattern(pattern, workDir string) (string, bool) {
	pattern = strings.TrimSpace(pattern)
	switch {
	case strings.HasPrefix(pattern, "//"):
		return filepath.Clean(filepath.FromSlash(pattern[1:])), true
	case strings.HasPrefix(pattern, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", false
		}
		return filepath.Clean(filepath.Join(home, filepath.FromSlash(pattern[2:]))), true
	case strings.HasPrefix(pattern, "/"):
		if strings.TrimSpace(workDir) == "" {
			return "", false
		}
		return filepath.Clean(filepath.Join(workDir, filepath.FromSlash(pattern[1:]))), true
	default:
		if strings.TrimSpace(workDir) == "" {
			return "", false
		}
		pattern = strings.TrimPrefix(pattern, "./")
		return filepath.Clean(filepath.Join(workDir, filepath.FromSlash(pattern))), true
	}
}

func absolutePermissionTarget(target, workDir string) (string, bool) {
	if filepath.IsAbs(target) {
		return filepath.Clean(target), true
	}
	if strings.TrimSpace(workDir) == "" {
		return "", false
	}
	return filepath.Clean(filepath.Join(workDir, target)), true
}

// resolvePermissionPath resolves symlinks through the deepest existing parent
// so rules remain sound for both existing reads and not-yet-created writes.
func resolvePermissionPath(target string) (string, error) {
	current := filepath.Clean(target)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
