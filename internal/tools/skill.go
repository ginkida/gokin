package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"gokin/internal/security"
	"gokin/internal/skills"

	"google.golang.org/genai"
)

const (
	maxSkillCatalogEntries = 40
	maxSkillCatalogChars   = 4000
	maxSkillListEntries    = 200
	maxSkillListChars      = 20000
	maxSkillArgumentsBytes = 16 * 1024
	maxSkillArgumentCount  = 256
)

// Skill payloads are also reachable through /skill, which intentionally
// bypasses Executor's generic post-tool pipeline. Redact at the shared render
// point so the immutable invocation snapshot and both delivery paths contain
// exactly the same safe text. SecretRedactor is immutable after construction
// and safe for concurrent calls.
var skillPayloadRedactor = security.NewSecretRedactor()

// SkillTool loads reusable workflows only when the model needs one. Skill
// bodies stay out of the permanent prompt and therefore do not churn GLM's
// prefix cache or consume every turn's context budget.
type SkillTool struct {
	catalog *skills.Catalog
	workDir string

	// ledger is deliberately separate from the discovery catalog: the catalog
	// may reload edited files at any time, while an invocation must retain the
	// exact rendered instructions the model actually received. Foreground tools
	// are rebound to the session-owned ledger; cloned sub-agent tools keep their
	// own ledger so concurrent agents cannot overwrite each other's context.
	ledgerMu sync.RWMutex
	ledger   *skills.InvocationLedger
}

// NewSkillTool discovers native Gokin skills and Claude-compatible SKILL.md
// directories at project and user scope.
func NewSkillTool(workDir string) *SkillTool {
	workDir = normalizeSkillWorkDir(workDir)
	return &SkillTool{
		catalog: skills.NewCatalog(skills.DefaultRoots(workDir)),
		workDir: workDir,
		ledger:  skills.NewInvocationLedger(),
	}
}

func normalizeSkillWorkDir(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		return ""
	}
	if absolute, err := filepath.Abs(workDir); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(workDir)
}

// NewSkillToolWithCatalog is the deterministic construction seam used by
// tests and embedders with explicit roots.
func NewSkillToolWithCatalog(catalog *skills.Catalog) *SkillTool {
	return NewSkillToolWithCatalogAndWorkDir(catalog, "")
}

// NewSkillToolWithCatalogAndWorkDir binds deterministic project-variable
// expansion for embedders that provide their own discovery catalog.
func NewSkillToolWithCatalogAndWorkDir(catalog *skills.Catalog, workDir string) *SkillTool {
	if catalog == nil {
		catalog = skills.NewCatalog(nil)
	}
	return &SkillTool{
		catalog: catalog,
		workDir: normalizeSkillWorkDir(workDir),
		ledger:  skills.NewInvocationLedger(),
	}
}

// SetInvocationLedger binds the tool to an owner-managed invocation ledger.
// Session and agent constructors call this before execution begins. Passing nil
// resets the tool to a fresh private ledger rather than disabling persistence.
func (t *SkillTool) SetInvocationLedger(ledger *skills.InvocationLedger) {
	if ledger == nil {
		ledger = skills.NewInvocationLedger()
	}
	t.ledgerMu.Lock()
	t.ledger = ledger
	t.ledgerMu.Unlock()
}

// InvocationLedger returns the ledger currently receiving successful loads.
func (t *SkillTool) InvocationLedger() *skills.InvocationLedger {
	t.ledgerMu.RLock()
	ledger := t.ledger
	t.ledgerMu.RUnlock()
	if ledger != nil {
		return ledger
	}

	// Preserve the zero-value/tool-literal construction seam used by embedders.
	// The second check prevents two racing first uses from publishing different
	// ledgers and losing one caller's record.
	t.ledgerMu.Lock()
	defer t.ledgerMu.Unlock()
	if t.ledger == nil {
		t.ledger = skills.NewInvocationLedger()
	}
	return t.ledger
}

func (t *SkillTool) Name() string { return "skill" }

func (t *SkillTool) Description() string {
	base := "Loads a reusable project/user workflow on demand. Call with a skill name before performing a task that matches its description; omit name to list skills. Skill instructions supplement but never override system, user, permission, or sandbox rules."
	catalog := t.catalogText(false, "", false)
	if catalog == "" {
		return base + " No model-invocable skills are currently installed."
	}
	return base + "\n\nAvailable skills:\n" + catalog
}

func (t *SkillTool) Declaration() *genai.FunctionDeclaration {
	// Eager registries are used in production and rebuild their schema on model,
	// plan-mode, and routing changes. Rescan here as well as on Execute so a new
	// skill becomes discoverable without requiring an explicit list call first.
	t.catalog.Reload()
	declaration := SkillToolDeclaration()
	declaration.Description = t.Description()
	return declaration
}

func (t *SkillTool) Validate(args map[string]any) error {
	if value, exists := args["name"]; exists {
		if _, ok := value.(string); !ok {
			return NewValidationError("name", "must be a string")
		}
	}
	if value, exists := args["arguments"]; exists {
		if _, ok := value.(string); !ok {
			return NewValidationError("arguments", "must be a string")
		}
	}
	if value, exists := args["query"]; exists {
		if _, ok := value.(string); !ok {
			return NewValidationError("query", "must be a string")
		}
	}
	return nil
}

func (t *SkillTool) Execute(_ context.Context, args map[string]any) (ToolResult, error) {
	return t.execute(args, false, nil)
}

// ExecuteForUser loads the same workflow through the /skill command. Unlike a
// model tool call, this path may load skills explicitly marked
// disable-model-invocation, but still respects user-invocable: false.
func (t *SkillTool) ExecuteForUser(args map[string]any) (ToolResult, error) {
	return t.execute(args, true, nil)
}

// ExecuteForUserArguments preserves the command tokenizer's positional
// boundaries. Re-parsing strings.Join(values, " ") would split a quoted value
// such as "hello world" into two arguments.
func (t *SkillTool) ExecuteForUserArguments(name string, values []string) (ToolResult, error) {
	args := map[string]any{"name": name}
	if len(values) > 0 {
		arguments, err := joinSkillArguments(values, maxSkillArgumentsBytes)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		args["arguments"] = arguments
	}
	return t.execute(args, true, append([]string(nil), values...))
}

// ExecuteForUserRawArguments keeps the exact command text for $ARGUMENTS while
// deriving positional values with the skill renderer's shell-style tokenizer.
// The generic slash-command tokenizer intentionally normalizes quotes and is
// therefore not a lossless source for this one command.
func (t *SkillTool) ExecuteForUserRawArguments(name, arguments string) (ToolResult, error) {
	if len(arguments) > maxSkillArgumentsBytes {
		return NewErrorResult(fmt.Sprintf("skill arguments exceed the %d-byte limit", maxSkillArgumentsBytes)), nil
	}
	values, err := splitSkillArguments(arguments)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("parse skill arguments: %v", err)), nil
	}
	return t.execute(map[string]any{"name": name, "arguments": arguments}, true, values)
}

func (t *SkillTool) execute(args map[string]any, forUser bool, positionalOverride []string) (ToolResult, error) {
	// Pick up skill edits without restarting the session. The catalog publishes
	// the replacement snapshot atomically for concurrent sub-agent calls.
	warnings := t.catalog.Reload()
	name, _ := GetString(args, "name")
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		query, _ := GetString(args, "query")
		catalog := t.catalogText(true, query, forUser)
		if catalog == "" {
			audience := "model-invocable"
			if forUser {
				audience = "user-invocable"
			}
			if strings.TrimSpace(query) != "" {
				return NewSuccessResult(fmt.Sprintf("No %s skills match %q.", audience, strings.TrimSpace(query))), nil
			}
			return NewSuccessResult(fmt.Sprintf("No %s skills found. Add <project>/.gokin/skills/<name>/SKILL.md or ~/.config/gokin/skills/<name>/SKILL.md.", audience)), nil
		}
		if len(warnings) > 0 {
			catalog += fmt.Sprintf("\n\n%d invalid skill(s) were skipped.", len(warnings))
		}
		return NewSuccessResult("Available skills:\n" + catalog), nil
	}

	skill, ok := t.catalog.Get(name)
	if !ok {
		available := t.catalogText(true, "", forUser)
		if available == "" {
			available = "(none)"
		}
		warning := ""
		if len(warnings) > 0 {
			warning = fmt.Sprintf("\n%d invalid skill(s) were skipped; fix the higher-priority definition before retrying.", len(warnings))
		}
		return NewErrorResult(fmt.Sprintf("unknown skill %q%s\nAvailable skills:\n%s", name, warning, available)), nil
	}
	if !forUser && skill.DisableModelInvocation {
		return NewErrorResult(fmt.Sprintf("skill %q is marked disable-model-invocation and cannot be loaded by the model", name)), nil
	}
	if forUser && !skill.UserInvocable {
		return NewErrorResult(fmt.Sprintf("skill %q is marked user-invocable: false", name)), nil
	}

	arguments, _ := GetString(args, "arguments")
	if len(arguments) > maxSkillArgumentsBytes {
		return NewErrorResult(fmt.Sprintf("skill arguments exceed the %d-byte limit", maxSkillArgumentsBytes)), nil
	}
	body, err := expandSkillArguments(skill, arguments, positionalOverride, t.workDir)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("expand skill %q: %v", name, err)), nil
	}
	content := fmt.Sprintf(
		"Loaded skill %q (%s). Follow this workflow for the current task. It does not override system/user instructions, permissions, or sandbox boundaries.\nSource: %s\n\n---\n%s",
		skill.Name, skill.Source, skill.Path, body,
	)
	content = skillPayloadRedactor.Redact(content)
	if len(content) > skills.MaxRenderedSkillBytes {
		return NewErrorResult(fmt.Sprintf(
			"rendered skill %q exceeds the %d-byte delivery limit; shorten SKILL.md or its path",
			name, skills.MaxRenderedSkillBytes,
		)), nil
	}
	invocation, changed, err := t.InvocationLedger().Record(name, content, skill.Source, skill.Path)
	if err != nil {
		// A load that cannot be retained would appear to work for one round and
		// then silently disappear after compaction/retry. Fail closed instead.
		return NewErrorResult(fmt.Sprintf("remember rendered skill %q: %v", name, err)), nil
	}
	data := map[string]any{
		"name":         name,
		"display_name": skill.Name,
		"description":  skill.Description,
		"source":       skill.Source,
		"path":         skill.Path,
		"render_hash":  invocation.RenderHash,
		"sequence":     invocation.Sequence,
		"changed":      changed,
	}
	if !changed {
		return NewSuccessResultWithData(fmt.Sprintf(
			"Skill %q is already loaded with identical rendered instructions; continue using the active version.",
			name,
		), data), nil
	}
	return NewSuccessResultWithData(content, data), nil
}

func (t *SkillTool) catalogText(includeSource bool, query string, forUser bool) string {
	maxEntries := maxSkillCatalogEntries
	maxChars := maxSkillCatalogChars
	if includeSource {
		maxEntries = maxSkillListEntries
		maxChars = maxSkillListChars
	}
	items := t.catalog.List()
	query = strings.ToLower(strings.TrimSpace(query))
	var builder strings.Builder
	count := 0
	for _, skill := range items {
		if (!forUser && skill.DisableModelInvocation) || (forUser && !skill.UserInvocable) {
			continue
		}
		searchText := skill.Name + " " + skill.DisplayName + " " + skill.Description + " " + skill.WhenToUse
		if query != "" && !strings.Contains(strings.ToLower(searchText), query) {
			continue
		}
		line := "- " + skill.Name
		if skill.DisplayName != "" && !strings.EqualFold(skill.DisplayName, skill.Name) {
			line += " (" + strings.Join(strings.Fields(skill.DisplayName), " ") + ")"
		}
		line += ": " + strings.Join(strings.Fields(skill.Description), " ")
		if skill.WhenToUse != "" {
			line += " When to use: " + strings.Join(strings.Fields(skill.WhenToUse), " ")
		}
		if includeSource && skill.ArgumentHint != "" {
			line += " " + strings.Join(strings.Fields(skill.ArgumentHint), " ")
		}
		if includeSource {
			line += " [" + skill.Source + "]"
		}
		if builder.Len()+len(line)+1 > maxChars {
			builder.WriteString("\n- … more skills available; call skill with query to filter the catalog")
			break
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)
		count++
		if count >= maxEntries {
			if len(items) > count {
				builder.WriteString("\n- … additional skills omitted from this listing")
			}
			break
		}
	}
	return builder.String()
}

func expandSkillArguments(skill skills.Skill, arguments string, positionalOverride []string, projectDir string) (string, error) {
	if len(arguments) > maxSkillArgumentsBytes {
		return "", fmt.Errorf("arguments exceed the %d-byte limit", maxSkillArgumentsBytes)
	}
	if !utf8.ValidString(arguments) {
		return "", fmt.Errorf("arguments must be valid UTF-8")
	}
	for _, variable := range []string{"${CLAUDE_SESSION_ID}", "${CLAUDE_EFFORT}"} {
		if containsUnescapedSkillToken(skill.Body, variable) {
			return "", fmt.Errorf("runtime variable %s is recognized but unsupported", variable)
		}
	}
	if (projectDir == "" || projectDir == ".") && containsUnescapedSkillToken(skill.Body, "${CLAUDE_PROJECT_DIR}") {
		return "", fmt.Errorf("runtime variable ${CLAUDE_PROJECT_DIR} requires a workDir-bound skill tool")
	}
	values := positionalOverride
	if values == nil {
		var err error
		values, err = splitSkillArguments(arguments)
		if err != nil {
			return "", err
		}
	}
	if len(values) > maxSkillArgumentCount {
		return "", fmt.Errorf("arguments exceed the %d-value limit", maxSkillArgumentCount)
	}
	named := make(map[string]string, len(skill.Arguments))
	for i, name := range skill.Arguments {
		if i < len(values) {
			named[name] = values[i]
		} else {
			named[name] = ""
		}
	}

	var builder strings.Builder
	if len(skill.Body) > skills.MaxSkillBytes {
		return "", fmt.Errorf("expanded workflow exceeds the %d-byte limit", skills.MaxSkillBytes)
	}
	builder.Grow(len(skill.Body))
	usedArguments := false
	for i := 0; i < len(skill.Body); {
		if skill.Body[i] == '\\' {
			start := i
			for i < len(skill.Body) && skill.Body[i] == '\\' {
				i++
			}
			if i < len(skill.Body) && skill.Body[i] == '$' {
				_, end, _, recognized := resolveSkillPlaceholder(skill.Body, i, arguments, values, named, skill, projectDir)
				if recognized && i-start == 1 {
					if err := appendSkillExpansion(&builder, skill.Body[i:end]); err != nil {
						return "", err
					}
					i = end
					continue
				}
			}
			if err := appendSkillExpansion(&builder, skill.Body[start:i]); err != nil {
				return "", err
			}
			continue
		}
		if skill.Body[i] == '$' {
			replacement, end, argumentPlaceholder, recognized := resolveSkillPlaceholder(skill.Body, i, arguments, values, named, skill, projectDir)
			if recognized {
				if err := appendSkillExpansion(&builder, replacement); err != nil {
					return "", err
				}
				usedArguments = usedArguments || argumentPlaceholder
				i = end
				continue
			}
		}
		if err := appendSkillExpansion(&builder, skill.Body[i:i+1]); err != nil {
			return "", err
		}
		i++
	}
	if strings.TrimSpace(arguments) != "" && !usedArguments {
		if err := appendSkillExpansion(&builder, "\n\nARGUMENTS: "+arguments); err != nil {
			return "", err
		}
	}
	return builder.String(), nil
}

func containsUnescapedSkillToken(body, token string) bool {
	for searchFrom := 0; searchFrom < len(body); {
		offset := strings.Index(body[searchFrom:], token)
		if offset < 0 {
			return false
		}
		index := searchFrom + offset
		backslashes := 0
		for i := index - 1; i >= 0 && body[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes != 1 {
			return true
		}
		searchFrom = index + len(token)
	}
	return false
}

func appendSkillExpansion(builder *strings.Builder, value string) error {
	if len(value) > skills.MaxSkillBytes-builder.Len() {
		return fmt.Errorf("expanded workflow exceeds the %d-byte limit", skills.MaxSkillBytes)
	}
	builder.WriteString(value)
	return nil
}

func resolveSkillPlaceholder(
	body string,
	start int,
	arguments string,
	values []string,
	named map[string]string,
	skill skills.Skill,
	projectDir string,
) (replacement string, end int, argumentPlaceholder bool, recognized bool) {
	if strings.HasPrefix(body[start:], "${CLAUDE_SKILL_DIR}") {
		return filepath.Dir(skill.Path), start + len("${CLAUDE_SKILL_DIR}"), false, true
	}
	if strings.HasPrefix(body[start:], "${CLAUDE_PROJECT_DIR}") {
		if projectDir != "" && projectDir != "." {
			return projectDir, start + len("${CLAUDE_PROJECT_DIR}"), false, true
		}
		return "${CLAUDE_PROJECT_DIR}", start + len("${CLAUDE_PROJECT_DIR}"), false, true
	}
	for _, literal := range []string{"${CLAUDE_SESSION_ID}", "${CLAUDE_EFFORT}"} {
		if strings.HasPrefix(body[start:], literal) {
			// Unescaped occurrences fail before rendering. Recognizing the token
			// here lets a single leading backslash use the standard literal escape
			// path and removes that escape marker from the delivered workflow.
			return literal, start + len(literal), false, true
		}
	}
	if strings.HasPrefix(body[start:], "$ARGUMENTS") {
		end = start + len("$ARGUMENTS")
		if end < len(body) && body[end] == '[' {
			closeOffset := strings.IndexByte(body[end+1:], ']')
			if closeOffset >= 0 {
				closeIndex := end + 1 + closeOffset
				indexText := body[end+1 : closeIndex]
				if index, err := strconv.Atoi(indexText); err == nil && index >= 0 {
					return skillArgumentAt(values, index), closeIndex + 1, true, true
				}
			}
			return "", start, false, false
		}
		if end == len(body) || !isSkillArgumentNameByte(body[end]) {
			return arguments, end, true, true
		}
	}
	if start+1 < len(body) && body[start+1] >= '0' && body[start+1] <= '9' {
		end = start + 1
		for end < len(body) && body[end] >= '0' && body[end] <= '9' {
			end++
		}
		index, err := strconv.Atoi(body[start+1 : end])
		if err != nil || index < 1 {
			// $0 is not a positional argument. The model-facing contract is
			// $1..$9, ONE-based — matching file commands ($1 = first arg) and
			// Claude-compatible skills. Zero-based resolution here silently
			// dropped a single argument passed to a body using $1 (v0.100.90).
			return "", start, false, false
		}
		return skillArgumentAt(values, index-1), end, true, true
	}
	if start+1 < len(body) && isSkillArgumentNameStart(body[start+1]) {
		end = start + 2
		for end < len(body) && isSkillArgumentNameByte(body[end]) {
			end++
		}
		if value, ok := named[body[start+1:end]]; ok {
			return value, end, true, true
		}
	}
	return "", start, false, false
}

func skillArgumentAt(values []string, index int) string {
	if index >= 0 && index < len(values) {
		return values[index]
	}
	return ""
}

func isSkillArgumentNameStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isSkillArgumentNameByte(value byte) bool {
	return isSkillArgumentNameStart(value) || value >= '0' && value <= '9' || value == '-'
}

func splitSkillArguments(arguments string) ([]string, error) {
	var result []string
	var builder strings.Builder
	var quote rune
	escaped := false
	tokenStarted := false
	for _, current := range arguments {
		if escaped {
			if quote == '"' {
				switch current {
				case '"', '\\', '$', '`':
					builder.WriteRune(current)
				case '\n':
					// Shell-style line continuation removes both characters.
				default:
					builder.WriteRune('\\')
					builder.WriteRune(current)
				}
			} else {
				if current != '\n' {
					builder.WriteRune(current)
				}
			}
			tokenStarted = true
			escaped = false
			continue
		}
		if quote != 0 {
			switch current {
			case '\\':
				if quote == '"' {
					escaped = true
				} else {
					builder.WriteRune(current)
				}
			case quote:
				quote = 0
				tokenStarted = true
			default:
				builder.WriteRune(current)
				tokenStarted = true
			}
			continue
		}
		switch {
		case current == '\\':
			escaped = true
			tokenStarted = true
		case current == '"' || current == '\'':
			quote = current
			tokenStarted = true
		case unicode.IsSpace(current):
			if tokenStarted {
				result = append(result, builder.String())
				builder.Reset()
				tokenStarted = false
			}
		default:
			builder.WriteRune(current)
			tokenStarted = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("arguments contain an unterminated quote")
	}
	if escaped {
		builder.WriteByte('\\')
	}
	if tokenStarted {
		result = append(result, builder.String())
	}
	return result, nil
}

func joinSkillArguments(values []string, maxBytes int) (string, error) {
	if len(values) > maxSkillArgumentCount {
		return "", fmt.Errorf("skill arguments exceed the %d-value limit", maxSkillArgumentCount)
	}
	rawBytes := 0
	for i, value := range values {
		if !utf8.ValidString(value) {
			return "", fmt.Errorf("skill arguments must be valid UTF-8")
		}
		separatorBytes := 0
		if i > 0 {
			separatorBytes = 1
		}
		if len(value)+separatorBytes > maxBytes-rawBytes {
			return "", fmt.Errorf("skill arguments exceed the %d-byte limit", maxBytes)
		}
		rawBytes += len(value) + separatorBytes
	}

	encoded := make([]string, len(values))
	encodedBytes := 0
	for i, value := range values {
		if value == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.ContainsAny(value, "\\\"'") {
			encoded[i] = strconv.Quote(value)
		} else {
			encoded[i] = value
		}
		separatorBytes := 0
		if i > 0 {
			separatorBytes = 1
		}
		if len(encoded[i])+separatorBytes > maxBytes-encodedBytes {
			return "", fmt.Errorf("skill arguments exceed the %d-byte limit after quoting", maxBytes)
		}
		encodedBytes += len(encoded[i]) + separatorBytes
	}
	return strings.Join(encoded, " "), nil
}
