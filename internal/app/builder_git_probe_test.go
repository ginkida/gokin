//go:build unix

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/tools"
)

func TestBuilderGitProbeMemoDoesNotFreezeRuntimeOrOtherBuilders(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'probe\\n' >> calls\n[ -f repository ] || exit 128\necho .git\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cfg := &config.Config{}
	cfg.API.Backend = "ollama"
	first := NewBuilder(cfg, root)
	t.Cleanup(first.cancel)
	if first.isInGitRepo() || first.isInGitRepo() {
		t.Fatal("fixture unexpectedly starts in a repository")
	}
	if err := os.WriteFile(filepath.Join(root, "repository"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewGitStatusTool(root))
	app := &App{config: cfg, workDir: root, registry: registry}
	decls := app.getActiveToolDeclarations()
	if len(decls) != 1 || decls[0].Name != "git_status" {
		t.Errorf("runtime declarations did not discover the new repository: %v", decls)
	}
	second := NewBuilder(cfg, root)
	t.Cleanup(second.cancel)
	if !second.isInGitRepo() {
		t.Error("new builder reused another builder's stale snapshot")
	}
	if first.isInGitRepo() {
		t.Error("first builder did not retain its own startup snapshot")
	}
	data, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(data), "probe\n"); calls != 3 {
		t.Errorf("git ran %d times, want one per builder plus one fresh runtime probe", calls)
	}
}
