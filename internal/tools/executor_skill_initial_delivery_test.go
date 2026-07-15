package tools

import (
	"strings"
	"testing"

	"gokin/internal/skills"

	"google.golang.org/genai"
)

const (
	executorSkillMiddleSentinel = "<<EXECUTOR_SKILL_MIDDLE_SENTINEL>>"
	executorSkillTailSentinel   = "<<EXECUTOR_SKILL_TAIL_SENTINEL>>"
)

func executorSkillTestPayload(t *testing.T) string {
	t.Helper()

	const payloadBytes = 20 * 1024
	prefix := strings.Repeat("a", 12*1024)
	remaining := payloadBytes - len(prefix) - len(executorSkillMiddleSentinel) - len(executorSkillTailSentinel)
	if remaining < 0 {
		t.Fatal("test sentinels exceed payload size")
	}
	payload := prefix + executorSkillMiddleSentinel + strings.Repeat("b", remaining) + executorSkillTailSentinel
	if len(payload) != payloadBytes {
		t.Fatalf("payload length = %d, want %d", len(payload), payloadBytes)
	}
	if len(payload) > skills.MaxSkillBytes {
		t.Fatalf("payload length = %d, exceeds MaxSkillBytes = %d", len(payload), skills.MaxSkillBytes)
	}
	return payload
}

func TestForegroundPressurePruneSkipsSuccessfulSkill(t *testing.T) {
	payload := executorSkillTestPayload(t)
	oversized := strings.Repeat("x", skills.MaxRenderedSkillBytes+1)
	skillPart := genai.NewPartFromFunctionResponse("skill", map[string]any{"success": true, "content": payload})
	oversizedSkillPart := genai.NewPartFromFunctionResponse("skill", map[string]any{"success": true, "content": oversized})
	readPart := genai.NewPartFromFunctionResponse("read", map[string]any{"success": true, "content": payload})
	history := []*genai.Content{
		genai.NewContentFromText("system", genai.RoleUser),
		genai.NewContentFromText("ready", genai.RoleModel),
		genai.NewContentFromText("task", genai.RoleUser),
		{Role: genai.RoleUser, Parts: []*genai.Part{skillPart}},
		{Role: genai.RoleUser, Parts: []*genai.Part{oversizedSkillPart}},
		{Role: genai.RoleUser, Parts: []*genai.Part{readPart}},
		genai.NewContentFromText("recent", genai.RoleModel),
	}

	if freed := pruneOldToolOutputs(history, 1, 1_000); freed <= 0 {
		t.Fatalf("pruneOldToolOutputs() freed %d chars, want ordinary read output pruned", freed)
	}
	gotSkill, _ := skillPart.FunctionResponse.Response["content"].(string)
	if gotSkill != payload {
		t.Fatalf("pressure prune changed successful skill payload: middle=%t tail=%t",
			strings.Contains(gotSkill, executorSkillMiddleSentinel),
			strings.Contains(gotSkill, executorSkillTailSentinel))
	}
	gotOversized, _ := oversizedSkillPart.FunctionResponse.Response["content"].(string)
	if gotOversized == oversized || !strings.Contains(gotOversized, "older skill output pruned") {
		t.Fatalf("oversized fake skill was not pressure-pruned: %q", gotOversized)
	}
	gotRead, _ := readPart.FunctionResponse.Response["content"].(string)
	if gotRead == payload || !strings.Contains(gotRead, "older read output pruned") {
		t.Fatalf("ordinary read output was not pressure-pruned: %q", gotRead)
	}
}
