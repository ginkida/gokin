package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// buildTurnCapReadStream reads a DISTINCT file per round so neither the
// stagnation fingerprint nor the within-turn re-coverage guard interferes with
// what this test is measuring.
func buildTurnCapReadStream(index int) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   fmt.Sprintf("turn-cap-%d", index),
			Name: "read",
			Args: map[string]any{"file_path": fmt.Sprintf("pkg/file_%02d.go", index)},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

// An interactive turn has no --max-turns budget. Its adaptive iteration cap
// bounds OUTER iterations, and one of those may legitimately contain dozens of
// chained provider rounds — a 25-tool task is ordinary work, not a runaway.
// Counting inner rounds against that adaptive cap (and turning it into a typed
// failure) made such a turn die and discarded the answer.
func TestExecutorExecuteLoop_NoInvocationCapAllowsManyModelRounds(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&scriptedReadTool{}); err != nil {
		t.Fatal(err)
	}
	responses := make([]*client.StreamingResponse, 0, 26)
	for index := range 25 {
		responses = append(responses, buildTurnCapReadStream(index))
	}
	responses = append(responses, buildExecutorTestTextStream("all files inspected"))

	executor := NewExecutor(registry, &scriptedExecutorClient{
		model:     "glm-5.2",
		responses: responses,
	}, 5*time.Second)

	_, final, err := executor.Execute(context.Background(), nil, "inspect the package")
	if err != nil {
		t.Fatalf("interactive turn failed after many rounds: %v", err)
	}
	if !strings.Contains(final, "all files inspected") {
		t.Fatalf("final text = %q, want the model's answer", final)
	}
	if strings.Contains(final, "Reached the model/tool turn limit") {
		t.Fatalf("interactive turn reported an invocation turn-cap failure: %q", final)
	}
}
