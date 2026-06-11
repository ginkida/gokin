package app

import (
	"strings"
	"testing"

	appcontext "gokin/internal/context"
	"gokin/internal/testkit"
)

func TestAppUpdateWorkingMemoryFromTurn_UsesResponseMetadata(t *testing.T) {
	dir := t.TempDir()
	wm := appcontext.NewWorkingMemoryManager(dir)
	pb := appcontext.NewPromptBuilder(dir, &appcontext.ProjectInfo{
		Type: appcontext.ProjectTypeGo,
		Name: "gokin",
	})
	pb.SetWorkingMemory(wm)

	// Prime the cache: the prompt must stay byte-identical across the
	// working-memory update (roadmap #7 — WM lives OUTSIDE the cached prefix).
	promptBefore := pb.Build()

	mock := testkit.NewMockClient()
	app := &App{
		workDir:              dir,
		workingMemory:        wm,
		promptBuilder:        pb,
		client:               mock,
		responseToolsUsed:    []string{"edit", "bash"},
		responseTouchedPaths: []string{"internal/app/done_gate.go"},
		responseCommands:     []string{"go test ./internal/app -count=1"},
	}

	app.updateWorkingMemoryFromTurn(
		"tighten verification reliability",
		"Updated the done gate verification flow for touched-path aware checks.",
	)

	content := wm.GetContent()
	for _, needle := range []string{
		"Updated the done gate verification flow for touched-path aware checks.",
		"internal/app/done_gate.go",
		"go test ./internal/app -count=1",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("working memory missing %q:\n%s", needle, content)
		}
	}

	// Roadmap #7: working memory is delivered as per-turn context on the
	// client, NOT in the cached system prefix.
	if tc := mock.TurnContext(); !strings.Contains(tc, "go test ./internal/app -count=1") {
		t.Fatalf("client turn context missing working memory content:\n%s", tc)
	}
	if prompt := pb.Build(); prompt != promptBefore {
		t.Fatal("system prompt changed after a working-memory update — cached prefix busted (roadmap #7 regression)")
	}
}
