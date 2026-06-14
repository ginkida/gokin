package tools

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func pruneTestFR(name, content string) *genai.Part {
	return &genai.Part{FunctionResponse: &genai.FunctionResponse{
		Name:     name,
		Response: map[string]any{"content": content},
	}}
}

func pruneTestText(s string) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: s}}}
}

func pruneTestUser(parts ...*genai.Part) *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: parts}
}

func pruneTestContent(p *genai.Part) string {
	if p == nil || p.FunctionResponse == nil || p.FunctionResponse.Response == nil {
		return ""
	}
	c, _ := p.FunctionResponse.Response["content"].(string)
	return c
}

// TestPruneOldToolOutputs pins the v0.98.x #6 fix: in-loop pruning shrinks large
// OLD tool outputs (outside the protected windows) in place — never removing a
// part, so tool_use/tool_result pairing survives.
func TestPruneOldToolOutputs(t *testing.T) {
	big := strings.Repeat("x", 2000)
	const small = "ok"
	history := []*genai.Content{
		pruneTestText("system"),                   // 0 protected (start)
		pruneTestText("greeting"),                 // 1 protected (start)
		pruneTestText("task"),                     // 2 protected (start)
		pruneTestUser(pruneTestFR("read", big)),   // 3 candidate → prune
		pruneTestUser(pruneTestFR("grep", big)),   // 4 candidate → prune
		pruneTestUser(pruneTestFR("read", small)), // 5 candidate but too small → keep
		pruneTestUser(pruneTestFR("read", big)),   // 6 protected (last 2)
		pruneTestUser(pruneTestFR("read", big)),   // 7 protected (last 2)
	}

	freed := pruneOldToolOutputs(history, 2, 1000)
	if freed <= 0 {
		t.Fatalf("expected chars freed, got %d", freed)
	}
	if !strings.Contains(pruneTestContent(history[3].Parts[0]), "pruned") {
		t.Errorf("index 3 should be pruned; got %q", pruneTestContent(history[3].Parts[0]))
	}
	if !strings.Contains(pruneTestContent(history[4].Parts[0]), "pruned") {
		t.Error("index 4 should be pruned")
	}
	if pruneTestContent(history[5].Parts[0]) != small {
		t.Errorf("small output should not be pruned; got %q", pruneTestContent(history[5].Parts[0]))
	}
	if pruneTestContent(history[6].Parts[0]) != big || pruneTestContent(history[7].Parts[0]) != big {
		t.Error("recent (protected) outputs must not be pruned")
	}
	// Pairing preserved: no message lost its parts.
	for i, m := range history {
		if len(m.Parts) == 0 {
			t.Errorf("message %d lost its parts — tool pairing broken", i)
		}
	}
}

func TestPruneOldToolOutputs_ShortHistoryNoop(t *testing.T) {
	history := []*genai.Content{
		pruneTestText("a"), pruneTestText("b"), pruneTestText("c"),
		pruneTestUser(pruneTestFR("read", strings.Repeat("x", 5000))),
	}
	if freed := pruneOldToolOutputs(history, 6, 1000); freed != 0 {
		t.Errorf("short history should be a no-op, freed %d", freed)
	}
}
