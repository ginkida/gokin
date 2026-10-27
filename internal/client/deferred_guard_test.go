package client

import (
	"strings"
	"testing"
)

func TestOutputGuardDeferredFileArguments(t *testing.T) {
	content := strings.Repeat("alice,active,42\n", 3000)
	args := map[string]any{"name": "write", "arguments": map[string]any{"content": content}}
	if err := checkToolArgumentRepetition("test", "use_tool", args); err != nil {
		t.Fatalf("deferred write must accept the same file fixture as a native write: %v", err)
	}
	args["arguments"] = map[string]any{"content": strings.Repeat("let me continue. ", 10000)}
	if err := checkToolArgumentRepetition("test", "use_tool", args); err == nil {
		t.Fatal("deferred arguments bypassed the repetition guard")
	}
}
