package context

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/tools"
	"gokin/internal/toolschema"
	"google.golang.org/genai"
)

type discoverySchemaTool struct{ description string }

func (t *discoverySchemaTool) Name() string        { return "large_mcp_schema" }
func (t *discoverySchemaTool) Description() string { return t.description }
func (t *discoverySchemaTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.description, Parameters: &genai.Schema{Type: genai.TypeObject}}
}
func (t *discoverySchemaTool) Validate(map[string]any) error { return nil }
func (t *discoverySchemaTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult("unused"), nil
}

func TestDeferredDiscoverySurvivesHookAndNotificationEnrichment(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&discoverySchemaTool{description: strings.Repeat("x", 27500)})
	ctx := tools.ContextWithDeferredCatalog(t.Context(), registry, registry.GeminiTools())
	search, _ := tools.DeferredMetaTool(ctx, registry, "tool_search")
	result, err := search.Execute(ctx, map[string]any{"query": "large_mcp_schema"})
	if err != nil || !result.Success || len(result.Content) > toolschema.MaxDiscoveryResultBytes {
		t.Fatal(result, err)
	}
	original := result.Content
	result = tools.AppendHookContext(result, []hooks.Result{{AdditionalContext: strings.Repeat("hook context ", 100)}})
	result = NewResultCompactor(10000).CompactForType("tool_search", result)
	response := result.ToMap()
	response["content"] = "[System: " + strings.Repeat("notification ", 200) + "]\n" + response["content"].(string)
	compressed := NewResponseCompressor(10000).CompressFunctionResponse(&genai.FunctionResponse{Name: "tool_search", Response: response})
	content := compressed.Response["content"].(string)
	if !strings.Contains(content, original) {
		t.Fatalf("complete schema was truncated after enrichment: %d -> %d", len(original), len(content))
	}
	line := strings.Split(strings.TrimSpace(original), "\n")[1]
	var declaration map[string]any
	if err := json.Unmarshal([]byte(line), &declaration); err != nil {
		t.Fatal(err)
	}
}

func TestUnmarkedDiscoveryResultsHaveNoTruncationExemption(t *testing.T) {
	compactor := NewResultCompactor(256)
	compressor := NewResponseCompressor(256)
	for _, size := range []int{toolschema.MaxDiscoveryResultBytes, toolschema.MaxDiscoveryResultBytes + 1} {
		payload := strings.Repeat("schema ", size/7) + strings.Repeat("x", size%7)
		result := tools.NewSuccessResult(payload)
		compacted := compactor.CompactForType("tool_search", result)
		response := compressor.CompressFunctionResponse(&genai.FunctionResponse{Name: "tool_search", Response: result.ToMap()})
		wantIntact := false
		if (compacted.Content == payload) != wantIntact || (response.Response["content"] == payload) != wantIntact {
			t.Fatalf("discovery preservation size=%d compacted=%d compressed=%v", size, len(compacted.Content), response.Response)
		}
	}
}

func dispatchedHistory(name string, args map[string]any, content string) []*genai.Content {
	return []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "use_tool", Args: map[string]any{"name": name, "arguments": args}}}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "use_tool", Response: map[string]any{"success": true, "content": content}}}}},
	}
}

func TestDeferredHistoryScannersUseEffectiveNames(t *testing.T) {
	wire := dispatchedHistory("bash", map[string]any{"command": "go test ./..."}, "all tests passed")
	before, _ := json.Marshal(wire)
	direct := toolschema.EffectiveHistory(wire)
	if got, want := extractDurableSessionLearnings(wire), extractDurableSessionLearnings(direct); len(got) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatched command learnings=%v want=%v", got, want)
	}
	scorer := NewRelevanceScorer()
	if got, want := scorer.ScoreMessages(wire, nil), scorer.ScoreMessages(direct, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatched relevance=%v want=%v", got, want)
	}
	summarizer := NewSummarizer(nil)
	if got, want := summarizer.formatMessages(wire), summarizer.formatMessages(direct); got != want {
		t.Fatalf("summary lost effective identity: %q want=%q", got, want)
	}
	after, _ := json.Marshal(wire)
	if string(before) != string(after) {
		t.Fatal("scanner rewrote wire history")
	}
}

func TestDeferredSkillHistoryCompressionPreservesInstructionsAndWireName(t *testing.T) {
	payload := strings.Repeat("workflow instructions ", 800)
	history := dispatchedHistory("skill", map[string]any{"name": "check"}, payload)
	compressed := NewResponseCompressor(256).CompressContents(history)
	response := compressed[1].Parts[0].FunctionResponse
	if response.Name != "use_tool" || response.Response["content"] != payload {
		t.Fatal("dispatched skill history was truncated or renamed")
	}
}
