package tools

import (
	"testing"

	"google.golang.org/genai"
)

func TestParallelGroupConcurrency(t *testing.T) {
	calls := func(names ...string) []*genai.FunctionCall {
		out := make([]*genai.FunctionCall, len(names))
		for i, name := range names {
			out[i] = &genai.FunctionCall{Name: name}
		}
		return out
	}
	cases := []struct {
		name  string
		calls []*genai.FunctionCall
		want  int
	}{
		{"local reads", calls("read", "grep", "glob", "git_diff"), MaxConcurrentLocalReads},
		{"network fetch pins group", calls("read", "web_fetch"), MaxConcurrentToolExecutions},
		{"search normalized name", calls(" Web_Search "), MaxConcurrentToolExecutions},
		{"nil entry ignored", []*genai.FunctionCall{nil, {Name: "read"}}, MaxConcurrentLocalReads},
	}
	for _, tc := range cases {
		if got := ParallelGroupConcurrency(tc.calls); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
	if MaxConcurrentLocalReads <= MaxConcurrentToolExecutions {
		t.Fatalf("local-read cap %d must exceed the network cap %d", MaxConcurrentLocalReads, MaxConcurrentToolExecutions)
	}
	for name := range networkParallelTools {
		if !IsParallelSafeTool(name) {
			t.Errorf("%s is listed as a network parallel tool but is not parallel-safe", name)
		}
	}
}
