package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestImplicitCachePrefixStableAcrossToolRounds(t *testing.T) {
	for _, scenario := range []struct {
		provider string
		evolving bool
	}{{"deepseek", false}, {"glm", false}, {"deepseek", true}, {"glm", true}} {
		t.Run(fmt.Sprintf("%s/evolving=%t", scenario.provider, scenario.evolving), func(t *testing.T) {
			provider := scenario.provider
			requests := make(chan []byte, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			t.Cleanup(server.Close)
			c, err := NewAnthropicClient(AnthropicConfig{APIKey: "test", BaseURL: server.URL, Provider: provider, Model: provider + "-test", EnableThinking: true})
			if err != nil {
				t.Fatal(err)
			}
			c.SetSystemInstruction("Stable instructions. " + strings.Repeat("prefix ", 300))
			c.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read", Description: "Read a file"}}}})
			c.SetTurnContext("Initial task state")
			prompt := "Add the comment in m.go"
			stream, err := c.SendMessageWithHistory(t.Context(), nil, prompt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.CollectContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			history := []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}
			for round := 1; round <= 2; round++ {
				if scenario.evolving {
					c.SetTurnContext(fmt.Sprintf("New task state %d", round))
				}
				id := fmt.Sprintf("read-%d", round)
				thought := &genai.Part{Thought: true, Text: "Keep this reasoning unchanged"}
				if provider == "deepseek" {
					thought.ThoughtSignature = []byte("stable-signature")
				}
				history = append(history, &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{thought, {FunctionCall: &genai.FunctionCall{ID: id, Name: "read", Args: map[string]any{"file_path": "m.go"}}}}})
				result := &genai.FunctionResponse{ID: id, Name: "read", Response: map[string]any{"content": "unchanged file body"}}
				stream, err := c.SendFunctionResponse(t.Context(), history, []*genai.FunctionResponse{result})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stream.CollectContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				history = append(history, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: result}}})
			}
			var previous map[string]json.RawMessage
			for round := range 3 {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(<-requests, &body); err != nil {
					t.Fatal(err)
				}
				if round > 0 {
					for _, key := range []string{"system", "tools"} {
						if !bytes.Equal(previous[key], body[key]) {
							t.Errorf("round %d changed %s", round+1, key)
						}
					}
					prefix := bytes.TrimSuffix(previous["messages"], []byte("]"))
					if !bytes.HasPrefix(body["messages"], prefix) {
						at := 0
						for at < len(prefix) && at < len(body["messages"]) && prefix[at] == body["messages"][at] {
							at++
						}
						lo := max(0, at-35)
						t.Errorf("round %d first changed message byte %d of %d; old=%s new=%s", round+1, at, len(prefix), prefix[lo:min(len(prefix), at+100)], body["messages"][lo:min(len(body["messages"]), at+100)])
					}
					t.Logf("round %d: retained %d message-prefix bytes; appended %d bytes", round+1, len(prefix), len(body["messages"])-len(previous["messages"]))
				}
				previous = body
			}
			if strings.Contains(marshalString(t, history), "turn-context") {
				t.Fatal("wire snapshots leaked into conversation history")
			}
			// /clear or a new invocation may repeat exactly the same prompt.
			// Empty history must not inherit the prior invocation's snapshots.
			c.SetTurnContext("")
			stream, err = c.SendMessageWithHistory(t.Context(), nil, prompt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.CollectContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(<-requests), "turn-context") {
				t.Fatal("identical prompt after clear inherited private snapshots")
			}
		})
	}
}
