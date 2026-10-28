package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/gitexec"
)

// GOKIN_BENCH_WORKSPACE measures the same no-op observation on a real checkout.
// The generated fixture and the selected checkout are never mutated in the timed loop.
func BenchmarkBashObservationWarm(b *testing.B) {
	root := os.Getenv("GOKIN_BENCH_WORKSPACE")
	if root == "" {
		root = b.TempDir()
		for i := range 2000 {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file_%04d.go", i)), []byte("package example\n"), 0600); err != nil {
				b.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
			cmd := gitexec.CommandContext(b.Context(), args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				b.Fatalf("git: %s %v", out, err)
			}
		}
	}
	for _, mode := range []string{"Cold", "Warm", "ReadOnly", "GoTestCold", "GoTestWarm", "GitOutputCold", "GitOutputWarm"} {
		b.Run(mode, func(b *testing.B) {
			var tracker WorkspaceWriteTracker
			args := map[string]any{"command": ":"}
			if mode == "ReadOnly" {
				args["command"] = "git status --short"
			}
			if strings.HasPrefix(mode, "GoTest") {
				args["command"] = "go test ./..."
			}
			if strings.HasPrefix(mode, "GitOutput") {
				args["command"] = "git diff --output=state.go"
			}
			run := func(context.Context) (ToolResult, error) { return NewSuccessResult(""), nil }
			_, _ = tracker.Observe(b.Context(), root, "bash", args, run, nil)
			b.ResetTimer()
			for range b.N {
				if strings.HasSuffix(mode, "Cold") {
					tracker = WorkspaceWriteTracker{}
				}
				r, err := tracker.Observe(b.Context(), root, "bash", args, run, nil)
				if err != nil || FilesChangedPartial(r) {
					b.Fatalf("incomplete observation: %+v %v", r, err)
				}
			}
		})
	}
}
