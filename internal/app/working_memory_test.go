package app

import (
	"strings"
	"testing"

	appcontext "gokin/internal/context"
)

func TestAppUpdateWorkingMemoryFromTurn_UsesResponseMetadata(t *testing.T) {
	dir := t.TempDir()
	wm := appcontext.NewWorkingMemoryManager(dir)
	pb := appcontext.NewPromptBuilder(dir, &appcontext.ProjectInfo{
		Type: appcontext.ProjectTypeGo,
		Name: "gokin",
	})
	pb.SetWorkingMemory(wm)

	// Prime the cache so the test verifies prompt invalidation too.
	_ = pb.Build()

	app := &App{
		workDir:              dir,
		workingMemory:        wm,
		promptBuilder:        pb,
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

	prompt := pb.Build()
	if !strings.Contains(prompt, "# Working Memory") {
		t.Fatalf("Build() missing working memory section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "go test ./internal/app -count=1") {
		t.Fatalf("Build() missing updated working memory command:\n%s", prompt)
	}
}
