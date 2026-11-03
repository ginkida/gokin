package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/repl"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestCredentialHeadlessReceipt(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "explicit-allow"}[allowed], func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			if err := os.WriteFile(filepath.Join(work, ".env"), []byte("credential_fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			mock := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": ".env"}).EnqueueText("Final answer.")
			a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewReadTool(work))
			a.workDir = work
			executor.SetWorkDir(work)
			manager := permission.NewManager(nil, false) // bypass still enforces the floor
			manager.SetPromptHandler(a.promptPermission)
			if allowed {
				rules, err := permission.ParseTemporaryToolGrantList("Read(./.env)")
				if err != nil {
					t.Fatal(err)
				}
				manager.SetRunToolRules(rules, nil)
			}
			executor.SetPermissions(manager)
			var stdout bytes.Buffer
			result, err := a.RunHeadlessWithOptions(t.Context(), "Inspect and answer", HeadlessOptions{OutputFormat: HeadlessOutputJSON, NoSelfReview: true, Stdout: &stdout, Stderr: io.Discard})
			if err != nil || result.Status != "success" || result.Incomplete == allowed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !allowed && (len(result.PermissionDenials) != 1 || !strings.Contains(fmt.Sprint(mock.Calls()[1].Responses[0].Response), "--allowed-tools 'Read(./.env)'")) {
				t.Fatalf("denials=%+v", result.PermissionDenials)
			}
			if allowed && len(result.PermissionDenials) != 0 {
				t.Fatalf("explicit allow denied: %+v", result)
			}
		})
	}
}

func TestSecretPathAppPropagation(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	target := filepath.Join(home, ".ssh", "fixture")
	if err := os.Mkdir(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Tools.AllowSecretPaths = []string{target}
	registry := tools.BareRegistry(home)
	runtime := &secretAwareHybridRuntime{}
	lazyRegistry := tools.NewRegistry()
	lazyRegistry.MustRegister(tools.NewReplExecTool(nil))
	deferred := &deferredHybridInit{
		registry: lazyRegistry,
		opts:     repl.Options{WorkDir: home},
		opener: func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
			return runtime, repl.Availability{Available: true}
		},
	}
	t.Cleanup(func() { _ = deferred.close() })
	a := &App{workDir: home, config: cfg, registry: registry, deferredHybrid: deferred}
	a.configureSecretPaths()
	if _, err := deferred.activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.paths) != 1 || runtime.paths[0] != target {
		t.Fatalf("lazy runtime lost config: %v", runtime.paths)
	}
	if !a.isDirAccessAllowed(target) {
		t.Fatal("gate missed config exception")
	}
	read, _ := registry.Get("read")
	r, err := read.Execute(t.Context(), map[string]any{"file_path": target})
	if err != nil || !r.Success {
		t.Fatalf("validator missed exception: %+v %v", r, err)
	}
	// Ordinary grant refreshes preserve the config-only exception.
	a.applyGrantedDirsToTools()
	r, err = read.Execute(t.Context(), map[string]any{"file_path": target})
	if err != nil || !r.Success {
		t.Fatalf("grant refresh lost exception: %+v %v", r, err)
	}
	cfg.Tools.AllowSecretPaths = nil
	a.configureSecretPaths()
	if len(runtime.paths) != 0 {
		t.Fatalf("live runtime retained revoked config: %v", runtime.paths)
	}
	if a.isDirAccessAllowed(target) {
		t.Fatal("revocation missed gate")
	}
	r, err = read.Execute(t.Context(), map[string]any{"file_path": target})
	if err != nil || r.Success {
		t.Fatalf("revocation missed validator: %+v %v", r, err)
	}
	for _, dir := range []string{home, filepath.Dir(home)} {
		if _, err := a.GrantAllowedDir(dir, false); err == nil {
			t.Errorf("/add-dir admitted %s", dir)
		}
		a.session = chat.NewSession()
		a.hooksManager = hooks.NewManager(false, home)
		if _, err := a.registerSDKRepository(t.Context(), dir, false, false, false); err == nil || !strings.Contains(err.Error(), "home directory") {
			t.Errorf("SDK home refusal %s: %v", dir, err)
		}
	}
}

type secretAwareHybridRuntime struct {
	fakeHybridRuntime
	paths []string
}

func (r *secretAwareHybridRuntime) SetAllowSecretPaths(paths []string) {
	r.paths = append([]string(nil), paths...)
}
