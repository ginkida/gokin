package agent

import (
	"testing"

	"gokin/internal/client"

	"google.golang.org/genai"
)

func TestBuildResponseParts_PreservesSignedThinkingAndFinalText(t *testing.T) {
	thought := &genai.Part{Thought: true, Text: "reasoning", ThoughtSignature: []byte("signature")}
	parts := (&Agent{}).buildResponseParts(&client.Response{
		Text:  "final answer",
		Parts: []*genai.Part{thought},
	})

	if len(parts) != 2 || parts[0] != thought || parts[1].Text != "final answer" {
		t.Fatalf("mixed response history = %#v, want signed thought then final text", parts)
	}
}
