package context

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

const contextSkillCarryPrefix = "[gokin:active-skills:v1]\n"

func recordContextSkill(t *testing.T, session *chat.Session, name, rendered string) (renderHash string) {
	t.Helper()
	invocation, changed, err := session.InvocationLedger().Record(
		name,
		rendered,
		"project",
		"/workspace/.gokin/skills/"+name+"/SKILL.md",
	)
	if err != nil {
		t.Fatalf("record skill %q: %v", name, err)
	}
	if !changed {
		t.Fatalf("first record of skill %q reported changed=false", name)
	}
	return invocation.RenderHash
}

func newContextSkillCarryManager(
	t *testing.T,
	session *chat.Session,
	mock *testkit.MockClient,
	maxInputTokens int,
	strategy SummaryStrategy,
) *ContextManager {
	t.Helper()
	manager := NewContextManager(context.Background(), session, mock, &config.ContextConfig{
		EnableAutoSummary: true,
		MaxInputTokens:    maxInputTokens,
	})
	manager.SetSummaryStrategy(strategy)
	t.Cleanup(manager.Close)
	return manager
}

func contextSkillCarryIndexes(history []*genai.Content) []int {
	var indexes []int
	for index, content := range history {
		if content == nil || content.Role != string(genai.RoleUser) || len(content.Parts) != 1 || content.Parts[0] == nil {
			continue
		}
		if strings.HasPrefix(content.Parts[0].Text, contextSkillCarryPrefix) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func countContextSkillSentinel(history []*genai.Content, sentinel string) int {
	count := 0
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			count += strings.Count(part.Text, sentinel)
			if response := part.FunctionResponse; response != nil {
				if rendered, ok := response.Response["content"].(string); ok {
					count += strings.Count(rendered, sentinel)
				}
			}
		}
	}
	return count
}

func historyWithoutContextSkillCarry(history []*genai.Content) []*genai.Content {
	filtered := make([]*genai.Content, 0, len(history))
	for _, content := range history {
		if content != nil && content.Role == string(genai.RoleUser) && len(content.Parts) == 1 && content.Parts[0] != nil &&
			strings.HasPrefix(content.Parts[0].Text, contextSkillCarryPrefix) {
			continue
		}
		filtered = append(filtered, content)
	}
	return filtered
}

func assertContextManagerCountsCommittedHistory(t *testing.T, manager *ContextManager, history []*genai.Content) int {
	t.Helper()
	expected, err := manager.tokenCounter.CountContents(context.Background(), history)
	if err != nil {
		t.Fatalf("count committed history: %v", err)
	}
	if got := manager.GetCurrentTokens(); got != expected {
		t.Fatalf("manager currentTokens = %d, want committed-history count %d", got, expected)
	}
	usage := manager.GetTokenUsage()
	if usage.InputTokens != expected {
		t.Fatalf("manager usage input tokens = %d, want committed-history count %d", usage.InputTokens, expected)
	}
	return expected
}

func assertBalancedContextToolIDs(t *testing.T, history []*genai.Content) {
	t.Helper()
	calls := make(map[string]int)
	responses := make(map[string]int)
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				calls[part.FunctionCall.ID]++
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responses[part.FunctionResponse.ID]++
			}
		}
	}
	if !reflect.DeepEqual(calls, responses) {
		t.Fatalf("orphaned tool IDs after context compaction: calls=%v responses=%v", calls, responses)
	}
}

func TestOptimizeContextCarriesActiveSkillImmediatelyAfterSummary(t *testing.T) {
	const sentinel = "<<OPTIMIZE-ACTIVE-SKILL>>"
	session := chat.NewSession()
	original := make([]*genai.Content, 0, 10)
	for index := 0; index < 10; index++ {
		role := genai.Role(genai.RoleUser)
		if index%2 == 1 {
			role = genai.RoleModel
		}
		original = append(original, genai.NewContentFromText(
			fmt.Sprintf("original-%02d with stable material for summary planning", index), role,
		))
	}
	session.SetHistory(original)
	// Record after installing raw history so this test exercises the
	// ContextManager commit itself, rather than Session.SetHistory's eager gate.
	recordContextSkill(t, session, "verify", "Run focused verification. "+sentinel)

	mock := testkit.NewMockClient().EnqueueText(
		"The middle conversation established a stable implementation and verification plan.",
	)
	strategy := SummaryStrategy{
		InitialMessageCount:   2,
		RecentMessageCount:    2,
		MinMessagesForSummary: 3,
		MaxHistorySize:        50,
		TargetRatio:           0.5,
	}
	manager := newContextSkillCarryManager(t, session, mock, 100_000, strategy)

	var callbackNewTokens, callbackRemoved int
	manager.OnCompact = func(_, newTokens, removed int, _ string) {
		callbackNewTokens = newTokens
		callbackRemoved = removed
	}
	if err := manager.OptimizeContext(context.Background()); err != nil {
		t.Fatalf("OptimizeContext: %v", err)
	}

	committed := session.GetHistory()
	if len(committed) != 6 {
		t.Fatalf("committed history len = %d, want keep-start(2)+summary+carry+keep-end(2)", len(committed))
	}
	if committed[0] != original[0] || committed[1] != original[1] {
		t.Fatal("OptimizeContext did not preserve KeepStart before summary/carry")
	}
	if !strings.Contains(committed[2].Parts[0].Text, "[Previous conversation summary]") {
		t.Fatalf("summary not found at KeepStart boundary: %#v", committed[2])
	}
	if indexes := contextSkillCarryIndexes(committed); !reflect.DeepEqual(indexes, []int{3}) {
		t.Fatalf("skill carry indexes = %v, want exactly [3] immediately after summary", indexes)
	}
	if got := countContextSkillSentinel(committed, sentinel); got != 1 {
		t.Fatalf("active skill sentinel occurrences = %d, want exactly 1", got)
	}

	expectedTokens := assertContextManagerCountsCommittedHistory(t, manager, committed)
	withoutCarry, err := manager.tokenCounter.CountContents(context.Background(), historyWithoutContextSkillCarry(committed))
	if err != nil {
		t.Fatalf("count history without carry: %v", err)
	}
	if expectedTokens <= withoutCarry {
		t.Fatalf("committed token count %d did not include carry (without carry = %d)", expectedTokens, withoutCarry)
	}
	if callbackNewTokens != expectedTokens || callbackRemoved != len(original)-len(committed) {
		t.Fatalf("OnCompact metrics = tokens:%d removed:%d, want committed tokens:%d removed:%d",
			callbackNewTokens, callbackRemoved, expectedTokens, len(original)-len(committed))
	}
	metrics := manager.GetMetrics().GetSummary()
	wantSaved := int64(EstimateContentsTokens(original) - expectedTokens)
	if metrics.TokensSaved != wantSaved {
		t.Fatalf("optimization metrics saved tokens = %d, want %d from committed history", metrics.TokensSaved, wantSaved)
	}
}

func TestIncrementalCompactCarriesActiveSkillImmediatelyAfterSummary(t *testing.T) {
	const sentinel = "<<INCREMENTAL-ACTIVE-SKILL>>"
	session := chat.NewSession()
	original := make([]*genai.Content, 0, 60)
	for index := 0; index < 60; index++ {
		role := genai.Role(genai.RoleUser)
		if index%2 == 1 {
			role = genai.RoleModel
		}
		original = append(original, genai.NewContentFromText(fmt.Sprintf("incremental-%02d stable history", index), role))
	}
	session.SetHistory(original)
	recordContextSkill(t, session, "review", "Keep review invariants active. "+sentinel)

	mock := testkit.NewMockClient().EnqueueText(
		"The oldest turns established the implementation constraints and review invariants.",
	)
	strategy := DefaultSummaryStrategy()
	strategy.MinMessagesForSummary = 5
	manager := newContextSkillCarryManager(t, session, mock, 100_000, strategy)

	var callbackNewTokens, callbackRemoved int
	manager.OnCompact = func(_, newTokens, removed int, _ string) {
		callbackNewTokens = newTokens
		callbackRemoved = removed
	}
	if err := manager.IncrementalCompact(context.Background()); err != nil {
		t.Fatalf("IncrementalCompact: %v", err)
	}

	committed := session.GetHistory()
	if len(committed) != 52 {
		t.Fatalf("committed history len = %d, want summary+carry+50 recent", len(committed))
	}
	if !strings.Contains(committed[0].Parts[0].Text, "[Previous conversation summary]") {
		t.Fatalf("incremental summary missing at index 0: %#v", committed[0])
	}
	if indexes := contextSkillCarryIndexes(committed); !reflect.DeepEqual(indexes, []int{1}) {
		t.Fatalf("skill carry indexes = %v, want exactly [1] immediately after summary", indexes)
	}
	if got := countContextSkillSentinel(committed, sentinel); got != 1 {
		t.Fatalf("active skill sentinel occurrences = %d, want exactly 1", got)
	}
	if committed[2] != original[10] || committed[len(committed)-1] != original[len(original)-1] {
		t.Fatal("incremental compaction did not preserve the expected recent suffix around carry")
	}

	expectedTokens := assertContextManagerCountsCommittedHistory(t, manager, committed)
	if callbackNewTokens != expectedTokens || callbackRemoved != len(original)-len(committed) {
		t.Fatalf("OnCompact metrics = tokens:%d removed:%d, want committed tokens:%d removed:%d",
			callbackNewTokens, callbackRemoved, expectedTokens, len(original)-len(committed))
	}
}

func TestEmergencyTruncateCarriesSkillAfterRegroundingAndPrunesOrphans(t *testing.T) {
	const (
		maxInput = 1_000
		sentinel = "<<EMERGENCY-ACTIVE-SKILL>>"
	)
	session := chat.NewSession()
	original := []*genai.Content{
		genai.NewContentFromText("you are gokin, a reliable coding assistant", genai.RoleModel),
		genai.NewContentFromText("Implement durable session recovery and verify every invariant", genai.RoleUser),
		{Role: string(genai.RoleModel), Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "orphan-old-call", Name: "bash", Args: map[string]any{"command": "go test ./..."},
		}}}},
		{Role: string(genai.RoleUser), Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "orphan-old-response", Name: "edit", Response: map[string]any{"success": false, "content": "error: stale edit failed"},
		}}}},
	}
	for index := 0; index < 14; index++ {
		original = append(original, genai.NewContentFromText(
			fmt.Sprintf("routine-%02d %s", index, strings.Repeat("continue reliable implementation work ", 20)),
			genai.RoleModel,
		))
	}
	original = append(original,
		&genai.Content{Role: string(genai.RoleModel), Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "recent-complete", Name: "read", Args: map[string]any{"path": "internal/chat/session.go"},
		}}}},
		&genai.Content{Role: string(genai.RoleUser), Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "recent-complete", Name: "read", Response: map[string]any{"success": true, "content": "latest session state"},
		}}}},
	)
	session.SetHistory(original)
	recordContextSkill(t, session, "recovery", "Preserve recovery discipline. "+sentinel)

	mock := testkit.NewMockClient()
	manager := newContextSkillCarryManager(t, session, mock, maxInput, DefaultSummaryStrategy())
	manager.lastEstimatedTokens = EstimateContentsTokens(original)

	var callbackNewTokens, callbackRemoved int
	manager.OnCompact = func(_, newTokens, removed int, _ string) {
		callbackNewTokens = newTokens
		callbackRemoved = removed
	}
	removed := manager.EmergencyTruncate()
	if removed <= 0 {
		t.Fatalf("EmergencyTruncate removed = %d, want a destructive truncation", removed)
	}

	committed := session.GetHistory()
	if len(committed) < 4 {
		t.Fatalf("committed history too short for preserved head, regrounding, and carry: %d", len(committed))
	}
	if !strings.Contains(committed[2].Parts[0].Text, "[Context was truncated") {
		t.Fatalf("regrounding note missing after preserved head: %#v", committed[2])
	}
	if indexes := contextSkillCarryIndexes(committed); !reflect.DeepEqual(indexes, []int{3}) {
		t.Fatalf("skill carry indexes = %v, want exactly [3] after preserved head and regrounding", indexes)
	}
	if got := countContextSkillSentinel(committed, sentinel); got != 1 {
		t.Fatalf("active skill sentinel occurrences = %d, want exactly 1", got)
	}
	assertBalancedContextToolIDs(t, committed)

	expectedTokens := EstimateContentsTokens(committed)
	if manager.GetCurrentTokens() != expectedTokens || manager.GetTokenUsage().InputTokens != expectedTokens {
		t.Fatalf("manager token state = current:%d usage:%d, want committed estimate %d",
			manager.GetCurrentTokens(), manager.GetTokenUsage().InputTokens, expectedTokens)
	}
	manager.mu.RLock()
	lastEstimatedTokens := manager.lastEstimatedTokens
	lastHistoryLen := manager.lastHistoryLen
	manager.mu.RUnlock()
	if lastEstimatedTokens != expectedTokens || lastHistoryLen != len(committed) {
		t.Fatalf("cached history metrics = tokens:%d len:%d, want committed tokens:%d len:%d",
			lastEstimatedTokens, lastHistoryLen, expectedTokens, len(committed))
	}
	if callbackNewTokens != expectedTokens || callbackRemoved != removed || removed != len(original)-len(committed) {
		t.Fatalf("EmergencyTruncate metrics = callback tokens:%d removed:%d return:%d, want tokens:%d removed:%d",
			callbackNewTokens, callbackRemoved, removed, expectedTokens, len(original)-len(committed))
	}
}

func TestIncrementalCompactRetainedCurrentSkillResponseSuppressesSyntheticDuplicate(t *testing.T) {
	const sentinel = "<<RAW-CURRENT-SKILL-RESPONSE>>"
	session := chat.NewSession()
	rendered := "Loaded immutable skill instructions. " + sentinel
	renderHash := recordContextSkill(t, session, "current", rendered)

	original := make([]*genai.Content, 0, 60)
	for index := 0; index < 58; index++ {
		role := genai.Role(genai.RoleUser)
		if index%2 == 1 {
			role = genai.RoleModel
		}
		original = append(original, genai.NewContentFromText(fmt.Sprintf("raw-current-%02d", index), role))
	}
	original = append(original,
		&genai.Content{Role: string(genai.RoleModel), Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "skill-current", Name: "skill", Args: map[string]any{"name": "current"},
		}}}},
		&genai.Content{Role: string(genai.RoleUser), Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "skill-current", Name: "skill", Response: map[string]any{
				"success": true,
				"content": rendered,
				"data":    map[string]any{"render_hash": renderHash, "changed": true},
			},
		}}}},
	)
	// The full current response is authoritative, so even the initial history
	// gate must not add a synthetic duplicate.
	session.SetHistory(original)
	if indexes := contextSkillCarryIndexes(session.GetHistory()); len(indexes) != 0 {
		t.Fatalf("initial retained full response produced synthetic carry at %v", indexes)
	}

	mock := testkit.NewMockClient().EnqueueText(
		"The old messages established the current skill invocation and implementation constraints.",
	)
	strategy := DefaultSummaryStrategy()
	strategy.MinMessagesForSummary = 5
	manager := newContextSkillCarryManager(t, session, mock, 100_000, strategy)
	if err := manager.IncrementalCompact(context.Background()); err != nil {
		t.Fatalf("IncrementalCompact: %v", err)
	}

	committed := session.GetHistory()
	if indexes := contextSkillCarryIndexes(committed); len(indexes) != 0 {
		t.Fatalf("retained current full response produced synthetic carry at %v", indexes)
	}
	if got := countContextSkillSentinel(committed, sentinel); got != 1 {
		t.Fatalf("current rendered skill occurrences = %d, want exactly one raw response", got)
	}
	if committed[len(committed)-2] != original[len(original)-2] || committed[len(committed)-1] != original[len(original)-1] {
		t.Fatal("the retained current skill call/response pair was not preserved")
	}
	assertBalancedContextToolIDs(t, committed)
	assertContextManagerCountsCommittedHistory(t, manager, committed)
}
