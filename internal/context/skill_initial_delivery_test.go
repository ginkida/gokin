package context

import (
	"strings"
	"testing"

	"gokin/internal/skills"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

const (
	initialSkillMiddleSentinel = "<<SKILL_INITIAL_MIDDLE_SENTINEL>>"
	initialSkillTailSentinel   = "<<SKILL_INITIAL_TAIL_SENTINEL>>"
)

func initialSkillTestPayload(t *testing.T) string {
	t.Helper()

	const payloadBytes = 20 * 1024
	prefix := strings.Repeat("a", 12*1024)
	remaining := payloadBytes - len(prefix) - len(initialSkillMiddleSentinel) - len(initialSkillTailSentinel)
	if remaining < 0 {
		t.Fatal("test sentinels exceed payload size")
	}
	payload := prefix + initialSkillMiddleSentinel + strings.Repeat("b", remaining) + initialSkillTailSentinel
	if len(payload) != payloadBytes {
		t.Fatalf("payload length = %d, want %d", len(payload), payloadBytes)
	}
	if len(payload) > skills.MaxSkillBytes {
		t.Fatalf("payload length = %d, exceeds MaxSkillBytes = %d", len(payload), skills.MaxSkillBytes)
	}
	return payload
}

func oversizedSkillTestPayload() string {
	return strings.Repeat("x", skills.MaxRenderedSkillBytes+1)
}

func TestSkillInitialPayloadBypassesResultCompactor(t *testing.T) {
	payload := initialSkillTestPayload(t)
	compactor := NewResultCompactor(10_000)

	got := compactor.CompactForType("skill", tools.NewSuccessResult(payload))
	if got.Content != payload {
		t.Fatalf("successful skill payload changed before delivery: middle=%t tail=%t len=%d want=%d",
			strings.Contains(got.Content, initialSkillMiddleSentinel),
			strings.Contains(got.Content, initialSkillTailSentinel),
			len(got.Content), len(payload))
	}

	ordinary := compactor.CompactForType("ordinary_tool", tools.NewSuccessResult(payload))
	if ordinary.Content == payload {
		t.Fatal("ordinary large tool payload unexpectedly bypassed ResultCompactor")
	}
	if strings.Contains(ordinary.Content, initialSkillMiddleSentinel) || strings.Contains(ordinary.Content, initialSkillTailSentinel) {
		t.Fatalf("ordinary payload retained sentinels after expected compaction: %q", ordinary.Content)
	}

	oversized := oversizedSkillTestPayload()
	oversizedSkill := compactor.CompactForType("skill", tools.NewSuccessResult(oversized))
	if oversizedSkill.Content == oversized || len(oversizedSkill.Content) >= len(oversized) {
		t.Fatalf("oversized fake skill bypassed ResultCompactor: got=%d original=%d",
			len(oversizedSkill.Content), len(oversized))
	}
}

func TestSkillInitialPayloadBypassesResponseCompressor(t *testing.T) {
	payload := initialSkillTestPayload(t)
	compressor := NewResponseCompressor(10_000)

	skillPart := genai.NewPartFromFunctionResponse("skill", map[string]any{
		"success": true,
		"content": payload,
		"data": map[string]any{
			"blob": strings.Repeat("d", 20*1024),
		},
	})
	gotSkill := compressor.CompressContent(skillPart)
	gotSkillContent, _ := gotSkill.FunctionResponse.Response["content"].(string)
	if gotSkillContent != payload {
		t.Fatalf("successful skill response changed before delivery: middle=%t tail=%t len=%d want=%d",
			strings.Contains(gotSkillContent, initialSkillMiddleSentinel),
			strings.Contains(gotSkillContent, initialSkillTailSentinel),
			len(gotSkillContent), len(payload))
	}
	gotData, _ := gotSkill.FunctionResponse.Response["data"].(map[string]any)
	gotBlob, _ := gotData["blob"].(string)
	if len(gotBlob) >= 20*1024 || !strings.Contains(gotBlob, "[truncated]") {
		t.Fatalf("bounded skill bypass preserved oversized sibling data: blob len=%d", len(gotBlob))
	}

	ordinaryPart := genai.NewPartFromFunctionResponse("ordinary_tool", map[string]any{
		"success": true,
		"content": payload,
	})
	gotOrdinary := compressor.CompressContent(ordinaryPart)
	gotOrdinaryContent, _ := gotOrdinary.FunctionResponse.Response["content"].(string)
	if gotOrdinaryContent == payload {
		t.Fatal("ordinary large tool response unexpectedly bypassed ResponseCompressor")
	}
	if strings.Contains(gotOrdinaryContent, initialSkillMiddleSentinel) || strings.Contains(gotOrdinaryContent, initialSkillTailSentinel) {
		t.Fatalf("ordinary response retained sentinels after expected compression: %q", gotOrdinaryContent)
	}

	oversized := oversizedSkillTestPayload()
	oversizedPart := genai.NewPartFromFunctionResponse("skill", map[string]any{
		"success": true,
		"content": oversized,
	})
	gotOversized := compressor.CompressContent(oversizedPart)
	gotOversizedContent, _ := gotOversized.FunctionResponse.Response["content"].(string)
	if gotOversizedContent == oversized || len(gotOversizedContent) >= len(oversized) {
		t.Fatalf("oversized fake skill bypassed ResponseCompressor: got=%d original=%d",
			len(gotOversizedContent), len(oversized))
	}
}
