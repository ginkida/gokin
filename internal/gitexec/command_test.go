package gitexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommandDisablesFSMonitorAndHooks(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"commit", "-m", "fixture"}, {"-C", t.TempDir(), "status"}} {
		cmd := CommandContext(t.Context(), args...)
		if len(cmd.Args) < 5 || cmd.Args[1] != "-c" || cmd.Args[2] != "core.fsmonitor=false" || cmd.Args[4] != "core.hooksPath="+os.DevNull {
			t.Fatalf("unsafe Git arguments: %q", cmd.Args)
		}
	}
}

func TestCommitHooksAreCapturedBeforeRepositoryWrites(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if output, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, output)
	}
	if CaptureHooks(root) != nil {
		t.Fatal("sample hooks must not require a trusted-hook directory")
	}
	path := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf original"), 0700); err != nil {
		t.Fatal(err)
	}
	hooks := CaptureHooks(root)
	if hooks == nil {
		t.Fatal("did not capture existing user hooks")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf injected"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cleanup, err := hooks.Activate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := CommandContext(ctx, "commit")
	var dir string
	for _, arg := range cmd.Args {
		if len(arg) > 15 && arg[:15] == "core.hooksPath=" {
			dir = arg[15:]
		}
	}
	if dir == "" || dir == os.DevNull {
		t.Fatalf("trusted hook path missing: %q", cmd.Args)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pre-commit"))
	if err != nil || string(data) != "#!/bin/sh\nprintf original" {
		t.Fatalf("hook snapshot changed: %s %v", data, err)
	}
}
