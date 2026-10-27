package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxTempDirectoryExistsBeforeCommand(t *testing.T) {
	requireSandboxExec(t)
	work := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	command, err := NewSandboxedCommand(t.Context(), work, `test -d "$TMPDIR" && test -w "$TMPDIR" && mktemp "$TMPDIR/gokin-temp-XXXXXX"`, DefaultSandboxConfig())
	if err != nil {
		t.Fatal(err)
	}
	result := command.Run(10 * time.Second)
	if result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("sandbox temp directory is unusable: %+v stderr=%s", result, result.Stderr)
	}
	path := strings.TrimSpace(string(result.Stdout))
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("private sandbox scratch survived command teardown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "tmp")); !os.IsNotExist(err) {
		t.Fatal("sandbox created an unnecessary project tmp directory")
	}
}

func TestSandboxStdinIsLiteralAndPreservesExitStatus(t *testing.T) {
	requireSandboxExec(t)
	command, err := NewSandboxedCommand(t.Context(), t.TempDir(), "cat; exit 7", DefaultSandboxConfig())
	if err != nil {
		t.Fatal(err)
	}
	input := "literal $(touch NEVER)\nsecond line\n"
	command.SetStdin(input)
	result := command.Run(10 * time.Second)
	if result.Error != nil || result.ExitCode != 7 || string(result.Stdout) != input {
		t.Fatalf("sandbox stdin/status = %+v", result)
	}
}
