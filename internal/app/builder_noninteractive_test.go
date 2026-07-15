package app

import (
	"io"
	"os"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestBuilderNonInteractiveSkipsAllowedDirsPrompt(t *testing.T) {
	b := NewBuilderWithOptions(&config.Config{}, t.TempDir(), BuildOptions{NonInteractive: true})
	originalStdin := os.Stdin
	os.Stdin = nil
	t.Cleanup(func() { os.Stdin = originalStdin })

	stdout := captureBuilderStdout(t, func() {
		if err := b.checkAllowedDirs(); err != nil {
			t.Fatalf("checkAllowedDirs() error = %v", err)
		}
	})
	if stdout != "" {
		t.Fatalf("non-interactive allowed-dir check wrote stdout: %q", stdout)
	}
	if got := b.cfg.Tools.AllowedDirs; len(got) != 0 {
		t.Fatalf("non-interactive allowed-dir check mutated config: %v", got)
	}
}

func TestBuilderNonInteractiveMissingOllamaModelDoesNotPromptOrPull(t *testing.T) {
	b := NewBuilderWithOptions(&config.Config{}, t.TempDir(), BuildOptions{NonInteractive: true})

	// Any stdin read or PullModel call would panic: the non-interactive branch
	// must return before touching either dependency.
	originalStdin := os.Stdin
	os.Stdin = nil
	t.Cleanup(func() { os.Stdin = originalStdin })

	var gotErr error
	stdout := captureBuilderStdout(t, func() {
		gotErr = b.promptModelPull(nil, "glm-5.2")
	})
	if stdout != "" {
		t.Fatalf("non-interactive missing-model check wrote stdout: %q", stdout)
	}
	if gotErr == nil {
		t.Fatal("non-interactive missing-model check unexpectedly succeeded")
	}
	if got := gotErr.Error(); !strings.Contains(got, "ollama pull glm-5.2") {
		t.Fatalf("error is not actionable: %q", got)
	}
}

func captureBuilderStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = r.Close()
		_ = w.Close()
	})

	fn()
	os.Stdout = originalStdout
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout capture: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	return string(b)
}
