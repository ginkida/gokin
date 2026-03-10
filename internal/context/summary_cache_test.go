package context

import (
	"testing"

	"google.golang.org/genai"
)

func TestHashMessagesIgnoresNilMessagesAndParts(t *testing.T) {
	messages := []*genai.Content{
		nil,
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				nil,
				genai.NewPartFromText("hello"),
			},
		},
	}

	hashA := HashMessages(messages)
	hashB := HashMessages(messages)

	if hashA == "" {
		t.Fatal("hash should not be empty")
	}
	if hashA != hashB {
		t.Fatalf("hash should be stable, got %q and %q", hashA, hashB)
	}
}
