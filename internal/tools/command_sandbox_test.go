package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gokin/internal/security"
	"gokin/internal/tasks"
	"gokin/internal/testkit"
)

func TestRequiredSandboxCoversCommandRunners(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "must-not-execute")
	for _, name := range []string{"bash", "go", "gofmt", "git"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf escaped > '"+marker+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// No launcher in PATH. Runner binaries exist and would write the marker
	// if any caller silently fell back despite the required policy.
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/blocked\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policyConfig := security.DefaultSandboxConfig()
	policyConfig.RequireSandbox = true
	policy := security.NewSandboxPolicy(root, policyConfig)
	registry := NewRegistry()
	for _, tool := range []Tool{NewBashTool(root), NewRunTestsTool(root), NewVerifyCodeTool(root)} {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	SetSandboxPolicyOnRegistry(registry, policy)
	bash, _ := registry.Get("bash")
	bash.(*BashTool).SetTaskManager(tasks.NewManager(root))
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"bash", map[string]any{"command": "touch must-not-execute"}},
		{"bash", map[string]any{"command": "touch must-not-execute", "run_in_background": true}},
		{"run_tests", map[string]any{"framework": "go"}},
		{"verify_code", nil},
	} {
		tool, _ := registry.Get(test.name)
		result, err := tool.Execute(t.Context(), test.args)
		if err != nil || result.Success || !strings.Contains(result.Error, "required bash sandbox unavailable") {
			t.Fatalf("%s escaped required policy: %+v %v", test.name, result, err)
		}
	}
	formatter := NewFormatter(map[string]string{".go": "gofmt -w"}, time.Second)
	if err := formatter.formatInWorkspace(t.Context(), filepath.Join(root, "file.go"), registry, root, policy); err == nil {
		t.Fatal("formatter ignored required policy")
	}
	// This is the direct-compiler launch seam used by delta checks, including
	// registries where the bash capability has been removed.
	cmd := exec.CommandContext(t.Context(), "go", "test", "./...")
	if _, err := PrepareVerificationCommand(t.Context(), cmd, NewRegistry(), root, policy); err == nil {
		t.Fatal("direct verifier ignored required policy")
	}
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetSandboxPolicy(policy)
	executor.deltaGitStatusPaths(t.Context(), root)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a blocked runner executed")
	}
}

func TestBashSandboxPolicyAndGrantsInvalidatePermissionLease(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	bash := NewBashTool(root)
	args := map[string]any{"command": "printf allowed"}
	for _, change := range []func(){
		func() { bash.SetAllowedDirs([]string{t.TempDir()}) },
		func() {
			config := bash.sandboxPolicy.Config()
			config.AllowNetwork = true
			bash.sandboxPolicy.Configure(root, config)
		},
	} {
		approved := PermissionArgsForTool(bash, args)
		change()
		result, err := ExecuteWithPermissionScope(t.Context(), bash, args, approved)
		if err != nil || result.Success || !strings.Contains(result.Error, "Permission scope changed") {
			t.Fatalf("stale approval executed: %+v %v", result, err)
		}
	}
	clone := CloneToolForWorkDir(bash, t.TempDir()).(*BashTool)
	if clone.sandboxPolicy != bash.sandboxPolicy || len(clone.sandboxDirs) != 1 {
		t.Fatal("clone lost run policy/grants")
	}
}

func TestBashSandboxEnableAndGrantRevocationResetOutsideCwd(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	outside := testkit.ResolvedTempDir(t)
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	bash.session.SetWorkDir(outside)
	bash.SetSandboxEnabled(true)
	if got := bash.session.WorkDir(); got != root {
		t.Fatalf("enabling sandbox retained unusable cwd: %s", got)
	}
	bash.SetAllowedDirs([]string{outside})
	bash.session.SetWorkDir(outside)
	bash.SetAllowedDirs(nil)
	if got := bash.session.WorkDir(); got != root {
		t.Fatalf("revoked directory remained cwd: %s", got)
	}
}

func TestBashIsolationToolRunners(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	config := security.DefaultSandboxConfig()
	config.RequireSandbox = true
	policy := security.NewSandboxPolicy(root, config)
	if status := policy.Status(t.Context()); status.Status != "enforced" {
		t.Skipf("bash backend capability: %s: %s", status.Backend, status.Reason)
	}
	toolDir := testkit.ResolvedTempDir(t)
	executable := filepath.Join(toolDir, "custom-check")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf tool-ok"), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable)
	command.Dir = root
	cleanup, err := policy.Prepare(t.Context(), command, security.CommandScope{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.CombinedOutput()
	cleanup()
	if err != nil || string(output) != "tool-ok" {
		t.Fatalf("external tool executable unavailable: %v %s", err, output)
	}
	outside := testkit.ResolvedTempDir(t)
	marker := filepath.Join(outside, "must-not-write")
	registry := DefaultRegistry(root)
	SetSandboxPolicyOnRegistry(registry, policy)
	bashTool, _ := registry.Get("bash")
	bash := bashTool.(*BashTool)
	manager := tasks.NewManager(root)
	defer manager.CancelAll()
	bash.SetTaskManager(manager)
	result, err := bash.Execute(t.Context(), map[string]any{"command": "mkdir child; cd child; pwd"})
	if err != nil || !result.Success || bash.WorkingDirectory() != filepath.Join(root, "child") {
		t.Fatalf("sandbox cwd channel failed: %+v %v cwd=%s", result, err, bash.WorkingDirectory())
	}
	result, err = bash.Execute(t.Context(), map[string]any{"command": "printf escaped > '" + marker + "'", "run_in_background": true})
	if err != nil || !result.Success {
		t.Fatalf("background launch failed: %+v %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("background result data: %#v", result.Data)
	}
	id, _ := data["task_id"].(string)
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("background task missing from shared manager")
	}
	select {
	case <-task.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("background task did not finish")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("background task escaped filesystem policy")
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/runners\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := "package fixture\nimport (\"os\"; \"testing\")\nfunc TestBoundary(t *testing.T) { if err:=os.WriteFile(" + strconv.Quote(marker) + ", []byte(\"escaped\"),0600); err==nil { t.Fatal(\"outside write allowed\") } }\n"
	if err := os.WriteFile(filepath.Join(root, "fixture_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run_tests", "verify_code"} {
		tool, _ := registry.Get(name)
		result, err := tool.Execute(t.Context(), nil)
		if err != nil || !result.Success {
			t.Fatalf("%s sandbox fixture failed: %+v %v", name, result, err)
		}
	}
}
