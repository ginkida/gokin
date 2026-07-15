package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

const agentActiveSkillSentinel = "<<AGENT_ACTIVE_SKILL_SENTINEL>>"

func skillLifecycleRegistry(t *testing.T) (*tools.Registry, *tools.SkillTool) {
	t.Helper()
	registry := tools.NewRegistry()
	skillTool := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	if err := registry.Register(skillTool); err != nil {
		t.Fatalf("register skill tool: %v", err)
	}
	return registry, skillTool
}

func requireAgentSkillTool(t *testing.T, agent *Agent) *tools.SkillTool {
	t.Helper()
	tool, ok := agent.registry.Get("skill")
	if !ok {
		t.Fatal("agent registry has no skill tool")
	}
	skillTool, ok := tool.(*tools.SkillTool)
	if !ok {
		t.Fatalf("agent skill tool has type %T, want *tools.SkillTool", tool)
	}
	return skillTool
}

func recordAgentActiveSkill(t *testing.T, ledger *skills.InvocationLedger, rendered string) skills.Invocation {
	t.Helper()
	invocation, changed, err := ledger.Record("reliable-workflow", rendered, "project", ".gokin/skills/reliable-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("record active skill: %v", err)
	}
	if !changed {
		t.Fatal("first active skill record unexpectedly deduplicated")
	}
	return invocation
}

func countSkillSentinel(history []*genai.Content, sentinel string) int {
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
			if part.FunctionResponse != nil && part.FunctionResponse.Response != nil {
				if rendered, ok := part.FunctionResponse.Response["content"].(string); ok {
					count += strings.Count(rendered, sentinel)
				}
			}
		}
	}
	return count
}

func countAgentSkillCarryMessages(history []*genai.Content) int {
	count := 0
	for _, content := range history {
		if content == nil || content.Role != genai.RoleUser {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && strings.HasPrefix(part.Text, "[gokin:active-skills:v1]\n") {
				count++
			}
		}
	}
	return count
}

func assertAgentActiveSkillOnce(t *testing.T, history []*genai.Content, wantCarry int) {
	t.Helper()
	if got := countSkillSentinel(history, agentActiveSkillSentinel); got != 1 {
		t.Fatalf("active skill sentinel occurrences = %d, want 1", got)
	}
	if got := countAgentSkillCarryMessages(history); got != wantCarry {
		t.Fatalf("synthetic active-skill carry messages = %d, want %d", got, wantCarry)
	}
}

func TestAgentOwnsAndBindsDistinctSkillInvocationLedger(t *testing.T) {
	registry, foregroundSkill := skillLifecycleRegistry(t)
	first := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
	second := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
	dynamic := NewAgentWithDynamicType(&DynamicAgentType{
		Name:         "custom-skill-agent",
		AllowedTools: []string{"skill"},
	}, nil, registry, t.TempDir(), 3, "", nil, nil)

	if first.invokedSkills == nil || second.invokedSkills == nil || dynamic.invokedSkills == nil {
		t.Fatal("constructor left an agent invocation ledger nil")
	}
	if first.invokedSkills == second.invokedSkills || first.invokedSkills == dynamic.invokedSkills ||
		second.invokedSkills == dynamic.invokedSkills {
		t.Fatal("sub-agents share an invocation ledger")
	}
	foregroundLedger := foregroundSkill.InvocationLedger()
	if first.invokedSkills == foregroundLedger || second.invokedSkills == foregroundLedger ||
		dynamic.invokedSkills == foregroundLedger {
		t.Fatal("sub-agent inherited the foreground invocation ledger")
	}

	for name, agent := range map[string]*Agent{"first": first, "second": second, "dynamic": dynamic} {
		if got := requireAgentSkillTool(t, agent).InvocationLedger(); got != agent.invokedSkills {
			t.Errorf("%s agent SkillTool ledger = %p, want owned %p", name, got, agent.invokedSkills)
		}
	}
}

func TestAgentSkillInvocationStateRoundTripAndFailClosedRestore(t *testing.T) {
	registry, _ := skillLifecycleRegistry(t)
	source := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
	source.history = []*genai.Content{genai.NewContentFromText("resume me", genai.RoleUser)}
	want := recordAgentActiveSkill(t, source.invokedSkills, "Loaded durable workflow. "+agentActiveSkillSentinel)

	encoded, err := json.Marshal(source.GetState())
	if err != nil {
		t.Fatalf("marshal AgentState: %v", err)
	}
	var persisted AgentState
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("unmarshal AgentState: %v", err)
	}

	target := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
	stableLedger := target.invokedSkills
	if _, _, err := stableLedger.Record("stale", "stale instructions", "project", "stale/SKILL.md"); err != nil {
		t.Fatalf("preseed stale skill: %v", err)
	}
	if err := target.RestoreHistory(&persisted); err != nil {
		t.Fatalf("RestoreHistory(round trip): %v", err)
	}
	if target.invokedSkills != stableLedger {
		t.Fatal("RestoreHistory replaced the stable agent ledger pointer")
	}
	if requireAgentSkillTool(t, target).InvocationLedger() != stableLedger {
		t.Fatal("restored agent SkillTool no longer points at its stable ledger")
	}
	got := stableLedger.SnapshotNewestFirst()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("restored invocations = %+v, want %+v", got, want)
	}

	// A legacy state has no invoked_skills field. Restoring it must clear the
	// prior session's active skills rather than leak them into the resumed run.
	legacy := persisted
	legacy.InvokedSkills = nil
	if err := target.RestoreHistory(&legacy); err != nil {
		t.Fatalf("RestoreHistory(legacy): %v", err)
	}
	if got := stableLedger.SnapshotNewestFirst(); len(got) != 0 {
		t.Fatalf("legacy restore retained stale invocations: %+v", got)
	}

	invalidRendered := want
	invalidRendered.Rendered = strings.Repeat("x", skills.MaxRenderedSkillBytes+1)
	invalidStates := []struct {
		name    string
		entries []skills.Invocation
	}{
		{name: "oversized render", entries: []skills.Invocation{invalidRendered}},
		{name: "entry cap", entries: make([]skills.Invocation, skills.MaxInvocationRestoreEntries+1)},
	}
	for _, test := range invalidStates {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := stableLedger.Record("stale", "must be cleared", "project", "stale/SKILL.md"); err != nil {
				t.Fatalf("preseed stale skill: %v", err)
			}
			state := persisted
			state.InvokedSkills = test.entries
			if err := target.RestoreHistory(&state); err != nil {
				t.Fatalf("optional invalid invoked skills aborted valid history restore: %v", err)
			}
			if target.invokedSkills != stableLedger {
				t.Fatal("invalid restore replaced the stable ledger pointer")
			}
			if got := stableLedger.SnapshotNewestFirst(); len(got) != 0 {
				t.Fatalf("invalid restore retained stale invocations: %+v", got)
			}
		})
	}
}

func TestAgentCompactionPathsReattachActiveSkill(t *testing.T) {
	rendered := "Loaded reliable workflow. " + agentActiveSkillSentinel

	t.Run("preemptive summary", func(t *testing.T) {
		history := buildHistory(10, strings.Repeat("x", 4000))
		agent, mock := compactionDecisionTestAgent(t, history, 1000)
		agent.invokedSkills = skills.NewInvocationLedger()
		recordAgentActiveSkill(t, agent.invokedSkills, rendered)
		mock.EnqueueText("Summary of the conversation that is long enough to be accepted.")

		if err := agent.checkAndSummarize(context.Background()); err != nil {
			t.Fatalf("checkAndSummarize: %v", err)
		}
		assertAgentActiveSkillOnce(t, agent.history, 1)
	})

	t.Run("forced summary", func(t *testing.T) {
		history := buildHistory(14, "middle history for forced summarization")
		agent, mock := compactionDecisionTestAgent(t, history, 128000)
		agent.invokedSkills = skills.NewInvocationLedger()
		recordAgentActiveSkill(t, agent.invokedSkills, rendered)
		mock.EnqueueText("Summary of the forced compaction that is long enough to be accepted.")

		if err := agent.forceCompactViaSummary(context.Background()); err != nil {
			t.Fatalf("forceCompactViaSummary: %v", err)
		}
		assertAgentActiveSkillOnce(t, agent.history, 1)
	})

	t.Run("truncation fallback", func(t *testing.T) {
		agent := &Agent{
			ID:            "skill-truncation",
			history:       buildHistory(20, "middle history for truncation"),
			invokedSkills: skills.NewInvocationLedger(),
		}
		recordAgentActiveSkill(t, agent.invokedSkills, rendered)

		if err := agent.forceCompactViaTruncation(); err != nil {
			t.Fatalf("forceCompactViaTruncation: %v", err)
		}
		assertAgentActiveSkillOnce(t, agent.history, 1)
	})
}

func TestAgentCompactionDoesNotDuplicateRetainedCurrentSkillResponse(t *testing.T) {
	rendered := "Loaded current workflow. " + agentActiveSkillSentinel
	ledger := skills.NewInvocationLedger()
	recordAgentActiveSkill(t, ledger, rendered)

	history := buildHistory(10, "middle history")
	history = append(history,
		genai.NewContentFromText("tail-0", genai.RoleModel),
		genai.NewContentFromText("tail-1", genai.RoleUser),
		&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "skill-call", Name: "skill", Args: map[string]any{"name": "reliable-workflow"},
		}}}},
		&genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "skill-call", Name: "skill", Response: map[string]any{
				"success": true,
				"content": rendered,
			},
		}}}},
		genai.NewContentFromText("tail-4", genai.RoleModel),
		genai.NewContentFromText("tail-5", genai.RoleUser),
	)
	agent := &Agent{ID: "retained-skill", history: history, invokedSkills: ledger}

	if err := agent.forceCompactViaTruncation(); err != nil {
		t.Fatalf("forceCompactViaTruncation: %v", err)
	}
	assertAgentActiveSkillOnce(t, agent.history, 0)
	ids := tpcCollect(agent.history)
	if !ids.calls["skill-call"] || !ids.responses["skill-call"] {
		t.Fatalf("retained skill tool pair became inconsistent: calls=%v responses=%v", ids.calls, ids.responses)
	}
}

func TestAgentModelBoundaryRefreshesCarryWithoutSplittingPendingToolPair(t *testing.T) {
	rendered := "Loaded boundary workflow. " + agentActiveSkillSentinel
	ledger := skills.NewInvocationLedger()
	recordAgentActiveSkill(t, ledger, rendered)

	t.Run("ordinary text send", func(t *testing.T) {
		mock := testkit.NewMockClient().EnqueueText("ok").EnqueueText("ok again")
		agent := &Agent{
			client:        mock,
			invokedSkills: ledger,
			history: []*genai.Content{
				genai.NewContentFromText("ready", genai.RoleModel),
				genai.NewContentFromText("continue the task", genai.RoleUser),
			},
		}
		for i := 0; i < 2; i++ {
			if _, err := agent.getModelResponse(context.Background()); err != nil {
				t.Fatalf("getModelResponse round %d: %v", i+1, err)
			}
			assertAgentActiveSkillOnce(t, agent.history, 1)
		}
		calls := mock.Calls()
		if len(calls) != 2 {
			t.Fatalf("model calls = %d, want 2", len(calls))
		}
		for i, call := range calls {
			if call.Method != "SendMessageWithHistory" || call.Message != "continue the task" {
				t.Errorf("call[%d] = %s message %q, want ordinary text send", i, call.Method, call.Message)
			}
			if got := countSkillSentinel(call.History, agentActiveSkillSentinel); got != 1 {
				t.Errorf("call[%d] active skill occurrences = %d, want 1", i, got)
			}
		}
	})

	t.Run("pending function response", func(t *testing.T) {
		mock := testkit.NewMockClient().EnqueueText("processed")
		agent := &Agent{
			client:        mock,
			invokedSkills: ledger,
			history: []*genai.Content{
				{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-call", Name: "read"}}}},
				{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
					ID: "read-call", Name: "read", Response: map[string]any{"success": true, "content": "data"},
				}}}},
			},
		}
		if _, err := agent.getModelResponse(context.Background()); err != nil {
			t.Fatalf("getModelResponse: %v", err)
		}
		if got := countAgentSkillCarryMessages(agent.history); got != 0 {
			t.Fatalf("inserted %d carry messages into a pending FunctionResponse boundary", got)
		}
		calls := mock.Calls()
		if len(calls) != 1 || calls[0].Method != "SendFunctionResponse" {
			t.Fatalf("pending tool round calls = %+v", calls)
		}
		if len(calls[0].History) != 1 || calls[0].History[0].Parts[0].FunctionCall.ID != "read-call" {
			t.Fatalf("pending tool call history changed: %+v", calls[0].History)
		}
	})
}
