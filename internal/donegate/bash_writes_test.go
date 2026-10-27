package donegate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/gitexec"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestPartialWorkspaceProfileDoesNotNarrowToGitDirtyPaths(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	for name, content := range map[string]string{"go.mod": "module example\n", "good.go": "package example\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := gitexec.CommandContext(t.Context(), "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if touched := DiscoverTouchedPaths(root); len(touched) == 0 {
		t.Fatal("fixture has no dirty paths")
	}
	profile := DetectFullProfile(root)
	if len(profile.TouchedPaths) != 0 {
		t.Fatalf("partial observation narrowed to Git paths: %v", profile.TouchedPaths)
	}
	registry := tools.NewRegistry()
	bash := &doneGateStubTool{name: "bash"}
	registry.MustRegister(bash)
	checks := BuildChecks(registry, root, "Do it", []string{"bash"}, profile, Policy{Enabled: true, Mode: "strict"})
	found := false
	for _, check := range checks {
		if _, err := check.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		if command, _ := bash.lastArgs["command"].(string); strings.Contains(command, "go vet ./...") {
			found = true
		}
	}
	if !found {
		t.Fatal("full workspace vet was not scheduled")
	}
}
