package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/app"
	"gokin/internal/chat"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"github.com/google/uuid"
)

func TestRunPolicyCLIResumeContinueForkAndReset(t *testing.T) {
	stateDir, work := testkit.ResolvedTempDir(t), testkit.ResolvedTempDir(t)
	t.Setenv("HOME", stateDir)
	for key, value := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(stateDir, "config"), "XDG_DATA_HOME": filepath.Join(stateDir, "data"),
		"XDG_CACHE_HOME": filepath.Join(stateDir, "cache"), "CLAUDE_CONFIG_DIR": filepath.Join(stateDir, "claude"),
		"GOKIN_PROVIDER_HEALTH_FILE": filepath.Join(stateDir, "health.json"), "GOKIN_ENGINE_MODE": "tools",
		"GOKIN_GLM_KEY": "test-key-for-fixture", "GLM_API_KEY": "", "GOKIN_API_KEY": "",
		"GOKIN_MODEL": "", "GOKIN_BACKEND": "", "GOKIN_NO_SELF_REVIEW": "",
	} {
		t.Setenv(key, value)
	}
	cfg := filepath.Join(stateDir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("api: {active_provider: glm, glm_key: test-key-for-fixture}\nmodel: {provider: glm, name: glm-5.3-flash, fallback_providers: []}\nengine: {mode: tools}\nmemory: {enabled: false}\nsession_memory: {enabled: false}\nhooks: {enabled: false}\nmcp: {enabled: false}\npermission: {enabled: false, rules: {}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var advertised []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		advertised = nil
		for _, tool := range request.Tools {
			advertised = append(advertised, tool.Name)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Acknowledged.\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	run := func(flags ...string) (*chat.SessionState, []string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		args := []string{"--config", cfg, "--base-url", server.URL, "--no-plugins", "-p", "--output-format", "json", "--prompt", "Say acknowledged", "--timeout", "10s"}
		cmd := exec.CommandContext(ctx, binary, append(args, flags...)...)
		tools.KillProcessGroupOnCancel(cmd)
		cmd.Dir, cmd.Env = work, replaceEnvironmentValue(os.Environ(), "GOKIN_TEST_AGENT_CALLER_CLI", "1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("CLI: %v\n%s\n%s", err, &stdout, &stderr)
		}
		var result app.HeadlessResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "success" {
			t.Fatalf("result=%s err=%v stderr=%s", &stdout, err, &stderr)
		}
		saved, err := history.LoadFull(result.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		names := slices.Clone(advertised)
		mu.Unlock()
		return saved, names, stderr.String()
	}
	id := uuid.NewString()
	saved, _, _ := run("--session-id", id, "--tools", "read,grep", "--disallowed-tools", "Bash(git push*)", "--permission-mode", "default", "--allowed-tools", "write")
	if saved.RunPolicy == nil || saved.RunPolicy.PermissionMode != "default" || !slices.Equal(saved.RunPolicy.DeniedRules, []string{"bash(git push*)"}) {
		t.Fatalf("initial policy = %+v", saved.RunPolicy)
	}
	encoded, _ := json.Marshal(saved.RunPolicy)
	if strings.Contains(string(encoded), "write") {
		t.Fatal("pre-approval persisted in run policy")
	}
	for _, flags := range [][]string{
		{"--resume", id, "--tools", "read,write", "--permission-mode", "bypassPermissions", "--disallowed-tools", "git_commit"},
		{"--resume", id, "--tools", "read,repl_exec,harness"},
		{"--continue"},
		{"--resume", id, "--fork-session"},
	} {
		saved, names, stderr := run(flags...)
		if saved.RunPolicy == nil || !slices.Equal(saved.RunPolicy.ToolCeiling, []string{"read"}) || saved.RunPolicy.PermissionMode != "default" || !slices.Equal(saved.RunPolicy.DeniedRules, []string{"bash(git push*)", "git_commit"}) || !slices.Contains(names, "read") || slices.Contains(names, "write") || slices.Contains(names, "grep") {
			t.Fatalf("flags=%v policy=%+v schema=%v", flags, saved.RunPolicy, names)
		}
		if strings.Count(stderr, "restrictions kept") != 1 {
			t.Fatalf("expected one notice: %s", stderr)
		}
		if slices.Contains(flags, "--fork-session") && saved.ID == id {
			t.Fatal("fork retained source identity")
		}
	}
	saved, names, _ := run("--resume", id, "--reset-session-policy", "--tools", "@edit", "--permission-mode", "bypassPermissions")
	if saved.RunPolicy == nil || saved.RunPolicy.PermissionMode != "bypassPermissions" || len(saved.RunPolicy.DeniedRules) != 0 || !slices.Contains(names, "write") {
		t.Fatalf("reset did not widen: %+v %v", saved.RunPolicy, names)
	}
	saved, _, stderr := run("--resume", id, "--reset-session-policy", "--disallowed-tools", "bash(git commit*)")
	if saved.RunPolicy.ToolCeiling != nil || !strings.Contains(stderr, "bash(git commit*) does not restrict the git_commit tool") {
		t.Fatalf("unrestricted reset/warning: %+v %s", saved.RunPolicy, stderr)
	}
	legacy := chat.NewSession()
	legacy.SetID(uuid.NewString())
	legacy.SetWorkDir(work)
	legacy.SetProvider("glm")
	legacy.AddUserMessage("legacy context")
	if err := history.SaveFull(legacy); err != nil {
		t.Fatal(err)
	}
	saved, names, stderr = run("--resume", legacy.GetID())
	if saved.RunPolicy != nil || !slices.Contains(names, "write") || strings.Contains(stderr, "restrictions kept") {
		t.Fatalf("legacy changed: %+v %s", saved.RunPolicy, stderr)
	}
	// Presets are intentionally exclusive to --tools.
	if _, err := resolveCLIDeniedToolRules([]string{"@read"}); err == nil {
		t.Fatal("deny rules accepted a preset")
	}
}
