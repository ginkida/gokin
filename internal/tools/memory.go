package tools

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gokin/internal/memory"

	"google.golang.org/genai"
)

// MemoryTool provides persistent memory storage between sessions.
type MemoryTool struct {
	store *memory.Store
	// learning is the project-learning store written by the `memorize` tool.
	// It is a SEPARATE store from `store`; `list` surfaces it so a user who
	// saved knowledge via memorize still sees it here (they used to be blind
	// to each other — the "memory list is empty after memorize" field report).
	learning *memory.ProjectLearning
	// allowGlobal is shared by foreground/sub-agent clones so a live config
	// revocation takes effect for already-running agents too. Global memory can
	// cross repository boundaries, therefore its safe zero/default is denied.
	allowGlobal *atomic.Bool
}

// NewMemoryTool creates a new memory tool.
func NewMemoryTool() *MemoryTool {
	return &MemoryTool{allowGlobal: &atomic.Bool{}}
}

// SetStore sets the memory store.
func (t *MemoryTool) SetStore(store *memory.Store) {
	t.store = store
}

// SetLearning wires the project-learning store (written by `memorize`) so that
// `memory list` can surface memorized facts/preferences/conventions too.
func (t *MemoryTool) SetLearning(learning *memory.ProjectLearning) {
	t.learning = learning
}

// SetAllowGlobal controls access to the user-wide memory namespace. Callers
// must opt in explicitly through memory.allow_global; denied global requests
// fail loudly instead of being silently rewritten into project scope.
func (t *MemoryTool) SetAllowGlobal(allow bool) {
	if t.allowGlobal == nil {
		t.allowGlobal = &atomic.Bool{}
	}
	t.allowGlobal.Store(allow)
}

func (t *MemoryTool) globalMemoryAllowed() bool {
	return t != nil && t.allowGlobal != nil && t.allowGlobal.Load()
}

const globalMemoryDisabledMessage = "global memory access is disabled to prevent cross-project leakage; set `memory.allow_global: true` in config to opt in"

func (t *MemoryTool) Name() string {
	return "memory"
}

func (t *MemoryTool) Description() string {
	return "Keyed memory store for remembering/recalling/forgetting/listing notes across sessions (supports keys, tags, TTL, and session/project scope; global scope requires memory.allow_global opt-in). action=list also surfaces durable project knowledge saved via the `memorize` tool (a separate project-learning store), so this is the one place to see everything remembered."
}

func (t *MemoryTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type:        genai.TypeString,
					Description: "Action to perform: 'remember' (save), 'recall' (retrieve), 'forget' (delete), 'list' (show all), 'feedback' (mark retrieval success/failure)",
					Enum:        []string{"remember", "recall", "forget", "list", "feedback"},
				},
				"content": {
					Type:        genai.TypeString,
					Description: "Content to remember (required for 'remember' action)",
				},
				"key": {
					Type:        genai.TypeString,
					Description: "Optional key to identify the memory. Makes it easier to recall or update later",
				},
				"query": {
					Type:        genai.TypeString,
					Description: "Search query for 'recall' action (searches in content and key)",
				},
				"tags": {
					Type:        genai.TypeArray,
					Items:       &genai.Schema{Type: genai.TypeString},
					Description: "Tags for organizing memories. Use for filtering in 'recall'",
				},
				"scope": {
					Type:        genai.TypeString,
					Description: "For 'remember': scope of the new memory: 'session' (current session only), 'project' (this repository only), 'global' (all projects; disabled unless memory.allow_global is true). Default: 'project'. For recall/list use project_only",
					Enum:        []string{"session", "project", "global"},
				},
				"id": {
					Type:        genai.TypeString,
					Description: "Memory ID for 'forget' action",
				},
				"project_only": {
					Type:        genai.TypeBoolean,
					Description: "If true, only show/search session and current-project memories (default: true). Setting false requires memory.allow_global=true",
				},
				"ttl_minutes": {
					Type:        genai.TypeInteger,
					Description: "Optional TTL in minutes for remembered memory. After expiration, memory is archived.",
				},
				"include_archived": {
					Type:        genai.TypeBoolean,
					Description: "Include archived memories in search/list results (default: false)",
				},
				"success": {
					Type:        genai.TypeBoolean,
					Description: "For 'feedback' action: true if retrieved memory was useful, false otherwise.",
				},
			},
			Required: []string{"action"},
		},
	}
}

func (t *MemoryTool) Validate(args map[string]any) error {
	action, ok := GetString(args, "action")
	if !ok {
		return NewValidationError("action", "action is required")
	}

	switch action {
	case "remember":
		if _, ok := GetString(args, "content"); !ok {
			return NewValidationError("content", "content is required for 'remember' action")
		}
		if scope, specified := GetString(args, "scope"); specified && scope != "session" && scope != "project" && scope != "global" {
			return NewValidationError("scope", "scope must be one of: session, project, global")
		}
	case "forget":
		// Need either id or key
		_, hasID := GetString(args, "id")
		_, hasKey := GetString(args, "key")
		if !hasID && !hasKey {
			return NewValidationError("id", "either 'id' or 'key' is required for 'forget' action")
		}
	case "feedback":
		_, hasID := GetString(args, "id")
		_, hasKey := GetString(args, "key")
		if !hasID && !hasKey {
			return NewValidationError("id", "either 'id' or 'key' is required for 'feedback' action")
		}
		if _, ok := GetBool(args, "success"); !ok {
			return NewValidationError("success", "success is required for 'feedback' action")
		}
	case "recall", "list":
		// No required parameters. Authorization belongs to Execute so denied
		// global access carries a typed PolicyBlock (headless must fail closed).
	default:
		return NewValidationError("action", "invalid action: "+action)
	}

	return nil
}

func (t *MemoryTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	action, _ := GetString(args, "action")

	// Scope authorization belongs at execution time (not structural Validate)
	// so the refusal carries PolicyBlock metadata and headless runs fail closed.
	// Check it before store availability: an explicit forbidden global request
	// must not be disguised as an ordinary configuration/storage failure.
	if err := t.validateGlobalRequest(args); err != nil {
		return NewPolicyBlockedResult(PolicyBlockPermission, err.Error()), nil
	}
	if action != "remember" {
		if scope, specified := GetString(args, "scope"); specified && scope != "" {
			return NewErrorResult("scope is only valid for action=remember; for recall/list set project_only=false (requires memory.allow_global)"), nil
		}
	}
	if t.store == nil {
		// `list` can still surface project-learning (written by `memorize`) even
		// when the keyed kv-store is disabled — the two are independent.
		if action == "list" && t.learning != nil && t.learning.HasContent() {
			return t.list(args)
		}
		return NewErrorResult("memory is unavailable — it's likely disabled in config. Set `memory.enabled: true` (and a non-zero `memory.max_entries`) in your config (~/.config/gokin/config.yaml) or via /config, then restart."), nil
	}

	switch action {
	case "remember":
		result, err := t.remember(args)
		if err == nil && result.Success {
			EmitMemoryNotify(ctx, "saved", truncate(GetStringDefault(args, "content", ""), 60))
		}
		return result, err
	case "recall":
		result, err := t.recall(args)
		if err == nil && result.Success {
			EmitMemoryNotify(ctx, "recalled", truncate(GetStringDefault(args, "query", ""), 40))
		}
		return result, err
	case "forget":
		result, err := t.forget(args)
		if err == nil && result.Success {
			EmitMemoryNotify(ctx, "forgotten", "")
		}
		return result, err
	case "list":
		return t.list(args)
	case "feedback":
		return t.feedback(args)
	default:
		return NewErrorResult("invalid action: " + action), nil
	}
}

func (t *MemoryTool) validateGlobalRequest(args map[string]any) error {
	if t.globalMemoryAllowed() {
		return nil
	}
	action, _ := GetString(args, "action")
	if scope, _ := GetString(args, "scope"); scope == "global" {
		return fmt.Errorf("%s", globalMemoryDisabledMessage)
	}
	if action == "recall" || action == "list" {
		if projectOnly, explicit := GetBool(args, "project_only"); explicit && !projectOnly {
			return fmt.Errorf("%s", globalMemoryDisabledMessage)
		}
	}
	return nil
}

// maxMemoryTTLMinutes caps a memory TTL at ~1 year. Without a bound, a large
// ttl_minutes (e.g. 999999999) overflows time.Duration(n)*time.Minute past
// int64, wrapping to a NEGATIVE duration — the entry would expire instantly
// instead of "effectively never".
const maxMemoryTTLMinutes = 525600 // 365 days

// clampTTLMinutes bounds a model-supplied TTL to [0, maxMemoryTTLMinutes].
func clampTTLMinutes(m int) int {
	if m < 0 {
		return 0
	}
	if m > maxMemoryTTLMinutes {
		return maxMemoryTTLMinutes
	}
	return m
}

func (t *MemoryTool) remember(args map[string]any) (ToolResult, error) {
	content, _ := GetString(args, "content")
	key, _ := GetString(args, "key")
	tagsRaw := args["tags"]
	ttlMinutes := GetIntDefault(args, "ttl_minutes", 0)

	// Parse scope. Global reaches this point only after the explicit opt-in gate
	// in Execute; never silently downgrade it to project because that lies about
	// both persistence and visibility to the caller.
	scopeName, _ := GetString(args, "scope")
	memType := memory.MemoryProject
	switch scopeName {
	case "", "project":
		// Project is the safe default.
	case "session":
		memType = memory.MemorySession
	case "global":
		memType = memory.MemoryGlobal
	default:
		return NewErrorResult(fmt.Sprintf("invalid memory scope %q; expected session, project, or global", scopeName)), nil
	}

	// Create entry
	entry := memory.NewEntry(content, memType)
	if key != "" {
		entry.WithKey(key)
	}
	if ttlMinutes > 0 {
		entry.WithTTL(time.Duration(clampTTLMinutes(ttlMinutes)) * time.Minute)
	}

	// Parse tags
	if tagsRaw != nil {
		if tagsSlice, ok := tagsRaw.([]any); ok {
			tags := make([]string, 0, len(tagsSlice))
			for _, t := range tagsSlice {
				if s, ok := t.(string); ok {
					tags = append(tags, s)
				}
			}
			entry.WithTags(tags)
		}
	}

	// Save entry and use the canonical result. Semantic dedup may reinforce an
	// older memory instead of storing this newly generated ID; returning the
	// incoming ID would make follow-up feedback/forget calls target nothing.
	storedEntry, err := t.store.AddResolved(entry)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to save memory: %s", err)), nil
	}
	entry = storedEntry

	var msg string
	if key != "" {
		msg = fmt.Sprintf("Remembered: %q with key %q", truncate(content, 100), key)
	} else {
		msg = fmt.Sprintf("Remembered: %q (id: %s)", truncate(content, 100), entry.ID)
	}

	return NewSuccessResultWithData(msg, map[string]any{
		"id":      entry.ID,
		"key":     entry.Key,
		"content": entry.Content,
		"type":    entry.Type,
		"tags":    entry.Tags,
		"expires": entry.ExpiresAt,
	}), nil
}

func (t *MemoryTool) recall(args map[string]any) (ToolResult, error) {
	key, _ := GetString(args, "key")
	query, _ := GetString(args, "query")
	tagsRaw := args["tags"]
	projectOnly := GetBoolDefault(args, "project_only", true) // Default: only current directory
	includeArchived := GetBoolDefault(args, "include_archived", false)

	// If key is specified, try exact match first
	if key != "" {
		if entry, ok := t.getByKey(key, projectOnly); ok {
			t.store.RecordAccess(entry.ID)
			return NewSuccessResultWithData(
				fmt.Sprintf("Found memory with key %q: %s", key, entry.Content),
				map[string]any{
					"id":      entry.ID,
					"key":     entry.Key,
					"content": entry.Content,
					"type":    entry.Type,
					"tags":    entry.Tags,
					"found":   true,
				},
			), nil
		}
	}

	// Build search query
	searchQuery := memory.SearchQuery{
		Key:             key,
		Query:           query,
		ProjectOnly:     projectOnly,
		Limit:           20, // Reasonable default
		IncludeArchived: includeArchived,
	}

	// Parse tags
	if tagsRaw != nil {
		if tagsSlice, ok := tagsRaw.([]any); ok {
			for _, t := range tagsSlice {
				if s, ok := t.(string); ok {
					searchQuery.Tags = append(searchQuery.Tags, s)
				}
			}
		}
	}

	results := t.store.Search(searchQuery)
	for i, entry := range results {
		if i >= 3 {
			break
		}
		t.store.RecordAccess(entry.ID)
	}

	if len(results) == 0 {
		return NewSuccessResultWithData("No memories found", map[string]any{
			"found": false,
			"count": 0,
		}), nil
	}

	// Build results string by type
	byType := make(map[memory.MemoryType][]*memory.Entry)
	for _, entry := range results {
		byType[entry.Type] = append(byType[entry.Type], entry)
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "Found %d memories:\n\n", len(results))

	types := []struct {
		t     memory.MemoryType
		label string
	}{
		{memory.MemorySession, "Session"},
		{memory.MemoryProject, "Project"},
		{memory.MemoryGlobal, "Global"},
	}

	resultData := make([]map[string]any, 0, len(results))
	for _, tc := range types {
		if items, ok := byType[tc.t]; ok && len(items) > 0 {
			fmt.Fprintf(&builder, "[%s Memories]\n", tc.label)
			for _, entry := range items {
				if entry.Key != "" {
					fmt.Fprintf(&builder, "- [%s] %s\n", entry.Key, entry.Content)
				} else {
					fmt.Fprintf(&builder, "- %s\n", entry.Content)
				}
				resultData = append(resultData, map[string]any{
					"id":           entry.ID,
					"key":          entry.Key,
					"content":      entry.Content,
					"type":         entry.Type,
					"tags":         entry.Tags,
					"archived":     entry.Archived,
					"success_rate": entry.SuccessRate(),
				})
			}
			builder.WriteString("\n")
		}
	}

	return NewSuccessResultWithData(builder.String(), map[string]any{
		"found":   true,
		"count":   len(results),
		"results": resultData,
	}), nil
}

// getByKey resolves a keyed memory within the caller-authorized scope. Store.Get
// intentionally applies session→project→global shadowing, so it cannot be
// used when global access is disabled: a global-only key would cross the
// repository boundary even though project_only defaults to true.
func (t *MemoryTool) getByKey(key string, projectOnly bool) (*memory.Entry, bool) {
	if !projectOnly {
		return t.store.Get(key)
	}
	entries := t.store.Search(memory.SearchQuery{
		Key:         key,
		ProjectOnly: true,
	})
	for _, memType := range []memory.MemoryType{memory.MemorySession, memory.MemoryProject} {
		for _, entry := range entries {
			if entry != nil && entry.Type == memType {
				return entry, true
			}
		}
	}
	return nil, false
}

func (t *MemoryTool) forget(args map[string]any) (ToolResult, error) {
	id, _ := GetString(args, "id")
	key, _ := GetString(args, "key")

	targetID := id
	displayTarget := id
	if targetID != "" {
		entry, ok := t.store.GetByID(targetID)
		if !ok {
			// The id field is strict. Passing an unknown ID through Store.Remove
			// would reinterpret it as a key and could bypass the global-scope gate.
			return NewErrorResult(fmt.Sprintf("Memory not found by id: %s", targetID)), nil
		}
		if entry.Type == memory.MemoryGlobal && !t.globalMemoryAllowed() {
			return NewPolicyBlockedResult(PolicyBlockPermission, globalMemoryDisabledMessage), nil
		}
		targetID = entry.ID
	} else {
		displayTarget = key
		entry, ok := t.getByKey(key, !t.globalMemoryAllowed())
		if !ok {
			return NewErrorResult(fmt.Sprintf("Memory not found in allowed scope: %s", key)), nil
		}
		targetID = entry.ID
	}

	if t.store.Remove(targetID) {
		return NewSuccessResult(fmt.Sprintf("Forgot memory: %s", displayTarget)), nil
	}

	return NewErrorResult(fmt.Sprintf("Memory not found: %s", displayTarget)), nil
}

func (t *MemoryTool) list(args map[string]any) (ToolResult, error) {
	projectOnly := GetBoolDefault(args, "project_only", true) // Default: only current directory
	includeArchived := GetBoolDefault(args, "include_archived", false)

	var entries []*memory.Entry
	if t.store != nil {
		if includeArchived {
			entries = t.store.Search(memory.SearchQuery{
				ProjectOnly:     projectOnly,
				IncludeArchived: true,
				Limit:           200,
			})
		} else {
			entries = t.store.List(projectOnly)
		}
	}

	// Project-learning (written by `memorize`) is a separate store. Surface it
	// here so a user who saved knowledge via memorize is never told "nothing
	// stored" — the two stores used to be blind to each other.
	learnedSection := ""
	if t.learning != nil && t.learning.HasContent() {
		learnedSection = t.learning.FormatForPrompt()
	}

	if len(entries) == 0 {
		scope := "global"
		if projectOnly {
			scope = "project"
		}
		if learnedSection != "" {
			return NewSuccessResultWithData(
				fmt.Sprintf("No keyed memories stored (%s scope). Project knowledge saved via `memorize`:\n\n%s", scope, learnedSection),
				map[string]any{"count": 0, "has_learning": true},
			), nil
		}
		return NewSuccessResultWithData(
			fmt.Sprintf("No memories stored (%s scope)", scope),
			map[string]any{"count": 0},
		), nil
	}

	// Build results string by type
	byType := make(map[memory.MemoryType][]*memory.Entry)
	for _, entry := range entries {
		byType[entry.Type] = append(byType[entry.Type], entry)
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "Stored memories (%d total):\n\n", len(entries))

	types := []struct {
		t     memory.MemoryType
		label string
	}{
		{memory.MemorySession, "Session"},
		{memory.MemoryProject, "Project"},
		{memory.MemoryGlobal, "Global"},
	}

	resultData := make([]map[string]any, 0, len(entries))
	for _, tc := range types {
		if items, ok := byType[tc.t]; ok && len(items) > 0 {
			fmt.Fprintf(&builder, "[%s Memories]\n", tc.label)
			for _, entry := range items {
				if entry.Key != "" {
					fmt.Fprintf(&builder, "- [%s] %s (id: %s)\n", entry.Key, truncate(entry.Content, 60), entry.ID)
				} else {
					fmt.Fprintf(&builder, "- %s (id: %s)\n", truncate(entry.Content, 60), entry.ID)
				}
				resultData = append(resultData, map[string]any{
					"id":           entry.ID,
					"key":          entry.Key,
					"content":      entry.Content,
					"type":         entry.Type,
					"tags":         entry.Tags,
					"archived":     entry.Archived,
					"success_rate": entry.SuccessRate(),
				})
			}
			builder.WriteString("\n")
		}
	}

	if learnedSection != "" {
		builder.WriteString("[Project Learning — saved via memorize]\n")
		builder.WriteString(learnedSection)
		builder.WriteString("\n")
	}

	return NewSuccessResultWithData(builder.String(), map[string]any{
		"count":        len(entries),
		"entries":      resultData,
		"has_learning": learnedSection != "",
	}), nil
}

func (t *MemoryTool) feedback(args map[string]any) (ToolResult, error) {
	id, _ := GetString(args, "id")
	key, _ := GetString(args, "key")
	success, _ := GetBool(args, "success")

	targetID := id
	if targetID != "" {
		if entry, ok := t.store.GetByID(targetID); ok && entry.Type == memory.MemoryGlobal && !t.globalMemoryAllowed() {
			return NewPolicyBlockedResult(PolicyBlockPermission, globalMemoryDisabledMessage), nil
		}
	} else if key != "" {
		entry, ok := t.getByKey(key, !t.globalMemoryAllowed())
		if !ok {
			return NewErrorResult(fmt.Sprintf("Memory not found by key in allowed scope: %s", key)), nil
		}
		targetID = entry.ID
	}

	if targetID == "" {
		return NewErrorResult("missing memory id/key for feedback"), nil
	}

	if ok := t.store.RecordFeedback(targetID, success); !ok {
		return NewErrorResult(fmt.Sprintf("Memory not found: %s", targetID)), nil
	}

	outcome := "failure"
	if success {
		outcome = "success"
	}
	return NewSuccessResult(fmt.Sprintf("Recorded memory feedback (%s): %s", outcome, targetID)), nil
}

// truncate truncates a string to the specified length (rune-safe).
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 || len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen]) // no room for ellipsis; avoids maxLen-3 underflow panic
	}
	return string(runes[:maxLen-3]) + "..."
}
