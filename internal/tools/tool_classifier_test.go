package tools

import (
	"testing"

	"google.golang.org/genai"
)

// helper to build a FunctionCall with a name and a single dummy arg.
func fc(name string) *genai.FunctionCall {
	return &genai.FunctionCall{Name: name, Args: map[string]any{"file_path": "/foo.go"}}
}

// names extracts the tool names from a group for readable assertion messages.
func names(g toolGroup) []string {
	out := make([]string, len(g.Calls))
	for i, c := range g.Calls {
		out[i] = c.Name
	}
	return out
}

func assertGroupCount(t *testing.T, groups []toolGroup, want int) {
	t.Helper()
	if len(groups) != want {
		t.Fatalf("group count = %d, want %d", len(groups), want)
	}
}

func assertGroup(t *testing.T, g toolGroup, wantNames []string, wantParallel bool) {
	t.Helper()
	got := names(g)
	if len(got) != len(wantNames) {
		t.Fatalf("group call count = %d (%v), want %d (%v)", len(got), got, len(wantNames), wantNames)
	}
	for i := range got {
		if got[i] != wantNames[i] {
			t.Fatalf("group call[%d] = %q, want %q", i, got[i], wantNames[i])
		}
	}
	if g.Parallel != wantParallel {
		t.Fatalf("group Parallel = %v, want %v (names=%v)", g.Parallel, wantParallel, got)
	}
}

// 1. Empty input returns a single group with nil calls and Parallel=false.
func TestClassifyDependencies_Empty(t *testing.T) {
	groups := defaultClassifier.classifyDependencies(nil)

	assertGroupCount(t, groups, 1)
	assertGroup(t, groups[0], nil, false)
}

// 2. Single tool returns a single non-parallel group.
func TestClassifyDependencies_SingleTool(t *testing.T) {
	groups := defaultClassifier.classifyDependencies([]*genai.FunctionCall{fc("read")})

	assertGroupCount(t, groups, 1)
	assertGroup(t, groups[0], []string{"read"}, false)
}

// 3. All read tools (read, grep, glob, tree) are batched into one parallel group.
func TestClassifyDependencies_AllReadTools(t *testing.T) {
	calls := []*genai.FunctionCall{fc("read"), fc("grep"), fc("glob"), fc("tree")}
	groups := defaultClassifier.classifyDependencies(calls)

	assertGroupCount(t, groups, 1)
	assertGroup(t, groups[0], []string{"read", "grep", "glob", "tree"}, true)
}

// 4. All write tools produce separate sequential (non-parallel) groups.
func TestClassifyDependencies_AllWriteTools(t *testing.T) {
	calls := []*genai.FunctionCall{fc("write"), fc("edit"), fc("bash")}
	groups := defaultClassifier.classifyDependencies(calls)

	assertGroupCount(t, groups, 3)
	assertGroup(t, groups[0], []string{"write"}, false)
	assertGroup(t, groups[1], []string{"edit"}, false)
	assertGroup(t, groups[2], []string{"bash"}, false)
}

// 5. Mixed: [grep, grep, write, read, read]
//
//	→ parallel(grep,grep), sequential(write), parallel(read,read)
func TestClassifyDependencies_MixedReadWriteRead(t *testing.T) {
	calls := []*genai.FunctionCall{fc("grep"), fc("grep"), fc("write"), fc("read"), fc("read")}
	groups := defaultClassifier.classifyDependencies(calls)

	assertGroupCount(t, groups, 3)
	assertGroup(t, groups[0], []string{"grep", "grep"}, true)
	assertGroup(t, groups[1], []string{"write"}, false)
	assertGroup(t, groups[2], []string{"read", "read"}, true)
}

// 6. Mixed: [write, read, write]
//
//	The mid-loop flush of a read group always sets Parallel=true (even for a
//	single read), so this produces:
//	  sequential(write), parallel(read), sequential(write)
func TestClassifyDependencies_WriteReadWrite(t *testing.T) {
	calls := []*genai.FunctionCall{fc("write"), fc("read"), fc("write")}
	groups := defaultClassifier.classifyDependencies(calls)

	assertGroupCount(t, groups, 3)
	assertGroup(t, groups[0], []string{"write"}, false)
	// mid-loop flush: Parallel is unconditionally true (line 45 in source).
	assertGroup(t, groups[1], []string{"read"}, true)
	assertGroup(t, groups[2], []string{"write"}, false)
}

// 7. Write at start: [bash, read, read]
//
//	→ sequential(bash), parallel(read,read)
func TestClassifyDependencies_WriteAtStart(t *testing.T) {
	calls := []*genai.FunctionCall{fc("bash"), fc("read"), fc("read")}
	groups := defaultClassifier.classifyDependencies(calls)

	assertGroupCount(t, groups, 2)
	assertGroup(t, groups[0], []string{"bash"}, false)
	assertGroup(t, groups[1], []string{"read", "read"}, true)
}

// 8. Write at end: [read, read, edit]
//
//	→ parallel(read,read), sequential(edit)
func TestClassifyDependencies_WriteAtEnd(t *testing.T) {
	calls := []*genai.FunctionCall{fc("read"), fc("read"), fc("edit")}
	groups := defaultClassifier.classifyDependencies(calls)

	assertGroupCount(t, groups, 2)
	assertGroup(t, groups[0], []string{"read", "read"}, true)
	assertGroup(t, groups[1], []string{"edit"}, false)
}
