package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"gokin/internal/app"
	"gokin/internal/config"
)

func TestBashIsolationCLIOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	if err := applyBashIsolationOverrides(cfg, true, true, false); err != nil {
		t.Fatal(err)
	}
	if !cfg.Tools.Bash.Sandbox || !cfg.Tools.Bash.AllowNetwork || !cfg.Tools.Bash.RequireSandbox {
		t.Fatalf("runtime policy lost: %+v", cfg.Tools.Bash)
	}
	if err := applyBashIsolationOverrides(cfg, false, false, true); err == nil {
		t.Fatal("required sandbox disabled")
	}
	for _, mode := range []cliPermissionMode{cliPermissionBypass, cliPermissionAcceptEdits, cliPermissionDontAsk} {
		if err := applyCLIPermissionMode(cfg, mode); err != nil {
			t.Fatal(err)
		}
		if !cfg.Tools.Bash.Sandbox || !cfg.Tools.Bash.RequireSandbox {
			t.Fatalf("permission mode %s disabled isolation", mode)
		}
	}
	cmd := newRootCommand()
	for _, name := range []string{"allow-network", "require-sandbox", "no-sandbox"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
}

func TestStartupFailureReportsUninitializedSandbox(t *testing.T) {
	var output bytes.Buffer
	if err := writeHeadlessFailure(&output, "", "configuration", errors.New("synthetic configuration failure")); err != nil {
		t.Fatal(err)
	}
	var result app.HeadlessResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Sandbox.Status != "unavailable" || result.Sandbox.Network != "unknown" || result.Sandbox.Reason == "" {
		t.Fatalf("startup failure omitted isolation state: %+v", result.Sandbox)
	}
}
