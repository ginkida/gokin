package agent

import (
	"context"
	"strings"
	"testing"

	ctxmgr "gokin/internal/context"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

const (
	agentSkillMiddleSentinel = "<<AGENT_SKILL_MIDDLE_SENTINEL>>"
	agentSkillTailSentinel   = "<<AGENT_SKILL_TAIL_SENTINEL>>"
)

type agentInitialStaticTool struct {
	name    string
	content string
}

func (t *agentInitialStaticTool) Name() string        { return t.name }
func (t *agentInitialStaticTool) Description() string { return "test initial-delivery tool" }
func (t *agentInitialStaticTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: "test initial-delivery tool"}
}
func (t *agentInitialStaticTool) Validate(map[string]any) error { return nil }
func (t *agentInitialStaticTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult(t.content), nil
}

func agentSkillTestPayload(t *testing.T) string {
	t.Helper()

	const payloadBytes = 20 * 1024
	prefix := strings.Repeat("a", 12*1024)
	remaining := payloadBytes - len(prefix) - len(agentSkillMiddleSentinel) - len(agentSkillTailSentinel)
	if remaining < 0 {
		t.Fatal("test sentinels exceed payload size")
	}
	payload := prefix + agentSkillMiddleSentinel + strings.Repeat("b", remaining) + agentSkillTailSentinel
	if len(payload) != payloadBytes {
		t.Fatalf("payload length = %d, want %d", len(payload), payloadBytes)
	}
	if len(payload) > skills.MaxSkillBytes {
		t.Fatalf("payload length = %d, exceeds MaxSkillBytes = %d", len(payload), skills.MaxSkillBytes)
	}
	return payload
}

func recordedAgentFunctionResponse(t *testing.T, calls []testkit.RecordedCall) *genai.FunctionResponse {
	t.Helper()
	for _, call := range calls {
		if call.Method == "SendFunctionResponse" && len(call.Responses) > 0 {
			return call.Responses[0]
		}
	}
	t.Fatalf("model calls contain no SendFunctionResponse: %+v", calls)
	return nil
}

func TestAgentInitialSkillDeliveryPreservesFullPayload(t *testing.T) {
	payload := agentSkillTestPayload(t)
	oversized := strings.Repeat("x", skills.MaxRenderedSkillBytes+1)

	for _, tc := range []struct {
		name              string
		toolName          string
		content           string
		wantIntact        bool
		wantSentinelsGone bool
	}{
		{name: "skill", toolName: "skill", content: payload, wantIntact: true},
		{name: "oversized fake skill", toolName: "skill", content: oversized},
		{name: "ordinary large result", toolName: "read", content: payload, wantSentinelsGone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			if err := registry.Register(&agentInitialStaticTool{name: tc.toolName, content: tc.content}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			mock := testkit.NewMockClient().
				EnqueueToolCall(tc.toolName, map[string]any{}).
				EnqueueText("done")
			agent := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
			agent.client = mock
			agent.compactor = ctxmgr.NewResultCompactor(10_000)

			result, err := agent.Run(context.Background(), "load instructions")
			if err != nil {
				t.Fatalf("Run() error = %v (result=%+v)", err, result)
			}
			response := recordedAgentFunctionResponse(t, mock.Calls())
			delivered, _ := response.Response["content"].(string)
			if tc.wantIntact {
				if delivered != payload {
					t.Fatalf("sub-agent changed skill payload before model delivery: middle=%t tail=%t len=%d want=%d",
						strings.Contains(delivered, agentSkillMiddleSentinel),
						strings.Contains(delivered, agentSkillTailSentinel),
						len(delivered), len(payload))
				}
				return
			}

			if delivered == tc.content {
				t.Fatal("ordinary large sub-agent tool result unexpectedly bypassed compaction")
			}
			if tc.wantSentinelsGone && (strings.Contains(delivered, agentSkillMiddleSentinel) || strings.Contains(delivered, agentSkillTailSentinel)) {
				t.Fatalf("ordinary sub-agent delivery retained sentinels after expected compaction: %q", delivered)
			}
		})
	}
}

func TestAgentPressurePruneSkipsSuccessfulSkill(t *testing.T) {
	payload := agentSkillTestPayload(t)
	oversized := strings.Repeat("x", skills.MaxRenderedSkillBytes+1)
	skillPart := genai.NewPartFromFunctionResponse("skill", map[string]any{"success": true, "content": payload})
	oversizedSkillPart := genai.NewPartFromFunctionResponse("skill", map[string]any{"success": true, "content": oversized})
	readPart := genai.NewPartFromFunctionResponse("read", map[string]any{"success": true, "content": payload})
	agent := &Agent{
		history: []*genai.Content{
			genai.NewContentFromText("system", genai.RoleUser),
			genai.NewContentFromText("ready", genai.RoleModel),
			{Role: genai.RoleUser, Parts: []*genai.Part{skillPart}},
			{Role: genai.RoleUser, Parts: []*genai.Part{oversizedSkillPart}},
			{Role: genai.RoleUser, Parts: []*genai.Part{readPart}},
			genai.NewContentFromText("working", genai.RoleUser),
			genai.NewContentFromText("recent", genai.RoleModel),
		},
		summarizeMinMsgs:   4,
		summarizeProtect:   1,
		pruneMinOutputSize: 1_000,
		compactor:          ctxmgr.NewResultCompactor(10_000),
	}

	if freed := agent.pruneToolOutputs(0); freed <= 0 {
		t.Fatalf("pruneToolOutputs() freed %d chars, want ordinary read output pruned", freed)
	}
	gotSkill, _ := skillPart.FunctionResponse.Response["content"].(string)
	if gotSkill != payload {
		t.Fatalf("agent pressure prune changed successful skill payload: middle=%t tail=%t",
			strings.Contains(gotSkill, agentSkillMiddleSentinel),
			strings.Contains(gotSkill, agentSkillTailSentinel))
	}
	gotOversized, _ := agent.history[3].Parts[0].FunctionResponse.Response["content"].(string)
	if gotOversized == oversized || !strings.Contains(gotOversized, "[skill:") {
		t.Fatalf("oversized fake skill was not pressure-pruned: %q", gotOversized)
	}
	gotRead, _ := agent.history[4].Parts[0].FunctionResponse.Response["content"].(string)
	if gotRead == payload || !strings.Contains(gotRead, "[read:") {
		t.Fatalf("ordinary read output was not pressure-pruned: %q", gotRead)
	}
}
