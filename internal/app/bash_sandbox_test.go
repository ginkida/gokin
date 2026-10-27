package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gokin/internal/security"
	"gokin/internal/testkit"
)

func TestHeadlessReportsUnavailableBashSandbox(t *testing.T) {
	// Missing launcher is deterministic on every host, including machines whose
	// OS backend works. There is no provider traffic in this fixture.
	t.Setenv("PATH", t.TempDir())
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional", true: "required"}[required], func(t *testing.T) {
			model := testkit.NewMockClient().EnqueueText("Ready.")
			a, _ := newHeadlessPolicyTestApp(t, model, &appHeadlessScriptedTool{name: "unused"})
			config := security.DefaultSandboxConfig()
			config.RequireSandbox = required
			a.bashSandbox = security.NewSandboxPolicy(a.workDir, config)
			var stdout, stderr bytes.Buffer
			result, err := a.RunHeadlessWithOptions(t.Context(), "hello", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: &stderr, NoSelfReview: true})
			if required && (err == nil || result.Error == nil || result.Error.Kind != "sandbox" || len(model.Calls()) != 0) {
				t.Fatalf("required backend did not stop before model: %+v %v", result, err)
			}
			if !required && err != nil {
				t.Fatal(err)
			}
			var envelope HeadlessResult
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Sandbox.Status != "unavailable" || envelope.Sandbox.Reason == "" || envelope.Sandbox.Network != "host" {
				t.Fatalf("dishonest sandbox status: %+v", envelope.Sandbox)
			}
			if !required && !strings.Contains(stderr.String(), "UNSANDBOXED") {
				t.Fatalf("missing fallback warning: %q", stderr.String())
			}
			frame := SDKResult(result, 0, 0)
			if frame["sandbox"] != result.Sandbox {
				t.Fatal("SDK result dropped effective sandbox state")
			}
			if !required {
				a.bashSandbox.Warn(t.Context(), io.Discard)
			}
		})
	}
}

func TestHeadlessRefusesUnsafeOptionalSandboxBeforeModel(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("OS sandbox platform")
	}
	model := testkit.NewMockClient().EnqueueText("must not run")
	a, _ := newHeadlessPolicyTestApp(t, model, &appHeadlessScriptedTool{name: "unused"})
	t.Setenv("HOME", t.TempDir())
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(root))
	path := filepath.Join(base, "gokin", "bash", fmt.Sprintf("%x", hash[:12]))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "bwrap"
	if runtime.GOOS == "darwin" {
		name = "sandbox-exec"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf gokin-bash-probe"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	a.bashSandbox = security.NewSandboxPolicy(root, security.DefaultSandboxConfig())
	var stdout, stderr bytes.Buffer
	result, err := a.RunHeadlessWithOptions(t.Context(), "hello", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: &stderr})
	if err == nil || result.Error == nil || result.Error.Kind != "sandbox" || len(model.Calls()) != 0 {
		t.Fatalf("unsafe optional policy reached model: %+v %v", result, err)
	}
	if strings.Contains(stderr.String(), "Commands run UNSANDBOXED") {
		t.Fatal("unsafe scope advertised fallback")
	}
}
