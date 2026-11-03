package tools

import (
	"context"
	"strings"
	"testing"
)

// fakeGitHubToken builds a token-shaped value at runtime so no credential-like
// literal sits in the source (secret scanners and push protection).
func fakeGitHubToken() string { return "gh" + "p_" + strings.Repeat("a1", 18) }

// The env tool must show only what commands receive. Before this change it
// read the raw process environment and let the model turn masking off with
// mask_secrets=false, so a parent agent's session token was one call away.
func TestEnvTool_WithholdsNonAllowlistedVariables(t *testing.T) {
	token := "ab-cd_ef-" + strings.Repeat("9z", 10)
	session := "Zx9qLm2Pw8" + "Rt4Yv6Bn1Kc3Hd"
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", token)
	t.Setenv("MY_SESSION_BLOB", session)
	t.Setenv("GOPATH", "/tmp/gopath-for-env-test")

	tool := NewEnvTool()
	calls := []map[string]any{
		{},
		{"mask_secrets": false},
		{"prefix": "CLAUDE", "mask_secrets": false},
		{"name": "CLAUDE_CODE_MESSAGING_TOKEN", "mask_secrets": false},
		{"name": "MY_SESSION_BLOB"},
	}
	for _, args := range calls {
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		for _, leaked := range []string{token, session} {
			if strings.Contains(res.Content, leaked) {
				t.Fatalf("args %v leaked %q:\n%s", args, leaked, res.Content)
			}
		}
	}

	res, _ := tool.Execute(context.Background(), map[string]any{})
	if !strings.Contains(res.Content, "GOPATH=/tmp/gopath-for-env-test") {
		t.Fatalf("allowlisted variable missing from listing:\n%s", res.Content)
	}
	res, _ = tool.Execute(context.Background(), map[string]any{"name": "CLAUDE_CODE_MESSAGING_TOKEN"})
	if !strings.Contains(res.Content, "not exposed") {
		t.Fatalf("withheld lookup must say why, got %q", res.Content)
	}
}

func TestEnvTool_RedactsCredentialInsideAllowlistedValue(t *testing.T) {
	tok := fakeGitHubToken()
	t.Setenv("GOPROXY", "https://user:"+tok+"@proxy.example")
	res, _ := NewEnvTool().Execute(context.Background(), map[string]any{"name": "GOPROXY"})
	if strings.Contains(res.Content, tok) {
		t.Fatalf("token inside allowlisted value leaked: %s", res.Content)
	}
}

func TestEnvTool_SchemaHasNoMaskOptOut(t *testing.T) {
	decl := NewEnvTool().Declaration()
	if _, ok := decl.Parameters.Properties["mask_secrets"]; ok {
		t.Fatal("mask_secrets must not be model-controllable")
	}
	if EnvToolDeclaration().Description != decl.Description {
		t.Fatal("EnvTool.Declaration must delegate to EnvToolDeclaration")
	}
}
