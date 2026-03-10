package context

import (
	"testing"

	"google.golang.org/genai"
)

func TestCompressContentsHandlesNilEntries(t *testing.T) {
	rc := NewResponseCompressor(16)
	contents := []*genai.Content{
		nil,
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{
					FunctionResponse: &genai.FunctionResponse{
						Name:     "read",
						Response: map[string]any{"content": "this is a long enough payload to compress"},
					},
				},
			},
		},
	}

	compressed := rc.CompressContents(contents)

	if len(compressed) != len(contents) {
		t.Fatalf("len(compressed) = %d, want %d", len(compressed), len(contents))
	}
	if compressed[0] != nil {
		t.Fatal("nil content entry should remain nil")
	}
	if compressed[1] == nil || len(compressed[1].Parts) != 1 || compressed[1].Parts[0] == nil {
		t.Fatal("non-nil content should still be compressed")
	}
}
