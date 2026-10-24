package tools_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	ctxmgr "gokin/internal/context"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestCompressedResponseMatchesInitialDeliveryAndReplay(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&executorDeliveryStaticTool{name: "inspect", content: strings.Repeat("abcdefgh", 256)})
	mock := testkit.NewMockClient().EnqueueToolCall("inspect", map[string]any{"query": "first"}).EnqueueToolCall("inspect", map[string]any{"query": "next"}).EnqueueText("Done.")
	executor := tools.NewExecutor(registry, mock, time.Second)
	executor.EnablePreFlightChecks(false)
	executor.SetResponseCompressor(ctxmgr.NewResponseCompressor(100))
	if _, _, err := executor.Execute(t.Context(), nil, "Inspect both queries"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 3 || len(calls[1].Responses) != 1 {
		t.Fatalf("unexpected trace: %+v", calls)
	}
	sent, err := json.Marshal(calls[1].Responses[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range calls[2].History {
		for _, part := range message.Parts {
			if part.FunctionResponse == nil {
				continue
			}
			replayed, err := json.Marshal(part.FunctionResponse)
			if err != nil {
				t.Fatal(err)
			}
			if string(sent) != string(replayed) {
				t.Fatalf("earlier response changed on replay: initial=%d bytes replay=%d bytes", len(sent), len(replayed))
			}
			return
		}
	}
	t.Fatal("initial result missing from replayed history")
}
