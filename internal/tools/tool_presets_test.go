package tools

import (
	"reflect"
	"slices"
	"testing"
)

func TestToolPresets(t *testing.T) {
	read := []string{"read", "grep", "glob", "list_dir", "tree", "diff", "git_status", "git_diff", "git_log", "git_blame", "review_changes", "go_to_definition", "find_references", "go_search", "todo"}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"@read", read},
		{"@test", append(slices.Clone(read), "bash", "run_tests", "verify_code")},
		{"@edit", append(slices.Clone(read), "bash", "run_tests", "verify_code", "edit", "write", "mkdir")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandToolPresets([]string{tc.name, "read"})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("expansion = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if _, err := ExpandToolPresets([]string{"@unknown"}); err == nil {
		t.Fatal("unknown preset accepted")
	}
	for _, input := range [][]string{nil, {}, {"read", "bash"}} {
		got, err := ExpandToolPresets(input)
		if err != nil || !reflect.DeepEqual(got, input) {
			t.Fatalf("literal/nil/empty expansion changed: %#v -> %#v (%v)", input, got, err)
		}
	}
}
