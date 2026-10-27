package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gokin/internal/testkit"
)

func TestHostGitStatusIgnoresFSMonitor(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	run("init")
	marker := filepath.Join(root, "must-not-run")
	monitor := filepath.Join(root, "monitor")
	if err := os.WriteFile(monitor, []byte("#!/bin/sh\nprintf escaped > '"+marker+"'\nprintf '\\0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	run("config", "core.fsmonitor", monitor)
	// Negative control proves this repository's ordinary host Git invokes it.
	run("status", "--porcelain")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fsmonitor fixture did not execute: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	tool := NewGitStatusTool(root)
	if result, err := tool.Execute(t.Context(), nil); err != nil || !result.Success {
		t.Fatalf("git status: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("host Git invoked repository fsmonitor")
	}
}

func TestHostGitCommitUsesOnlyOriginalHooks(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "Fixture")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "fixture@example.test")
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	run("init")
	marker := filepath.Join(root, "observed")
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf original > '"+marker+"'"), 0700); err != nil {
		t.Fatal(err)
	}
	tool := NewGitCommitTool(root)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf injected > '"+marker+"'"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "file")
	if result, err := tool.Execute(t.Context(), map[string]any{"message": "fixture"}); err != nil || !result.Success {
		t.Fatalf("commit: %+v %v", result, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "original" {
		t.Fatalf("host ran changed hook: %s %v", data, err)
	}
}
