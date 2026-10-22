package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gokin/internal/app"
	"gokin/internal/config"
	"gokin/internal/security"
)

// This opt-in test uses a caller-selected binary and four prompts per model.
// It never writes the credential to disk or modifies the user's session store.
func TestLiveGLMHeadlessSessionLifecycle(t *testing.T) {
	for _, model := range []string{"", config.GLMFlagshipModel} {
		name := model
		if name == "" {
			name = "default-flash"
		}
		t.Run(name, func(t *testing.T) { testLiveGLMHeadlessSessionLifecycle(t, model) })
	}
}

func testLiveGLMHeadlessSessionLifecycle(t *testing.T, model string) {
	configPath := os.Getenv("GOKIN_LIVE_GLM_CONFIG")
	binary := os.Getenv("GOKIN_LIVE_GOKIN_BIN")
	if configPath == "" || binary == "" {
		t.Skip("set GOKIN_LIVE_GLM_CONFIG and GOKIN_LIVE_GOKIN_BIN to run live session acceptance")
	}
	base, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatal(err)
	}
	provider := config.GetProvider("glm")
	credential := security.GetProviderKey(provider.EnvVars, provider.GetKey(&base.API), base.API.APIKey)
	if !credential.IsSet() {
		t.Fatal("GLM credential is required for live session acceptance")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	work := filepath.Join(root, "workspace")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	isolatedConfig := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(isolatedConfig, []byte(`api: {active_provider: glm}
model: {max_output_tokens: 4096, reasoning_effort: low, fallback_providers: []}
engine: {mode: tools}
memory: {enabled: false}
session_memory: {enabled: false}
hooks: {enabled: false}
mcp: {enabled: false}
`), 0600); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"GOKIN_GLM_KEY="+credential.Value,
		"GOKIN_API_KEY=", "GLM_API_KEY=", "GOKIN_DEEPSEEK_KEY=", "GOKIN_DEEPSEEK_KEY=",
		"GOKIN_MODEL=", "GOKIN_BACKEND=", "GOKIN_ENGINE_MODE=tools",
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"),
		"GOKIN_PROVIDER_HEALTH_FILE="+filepath.Join(root, "health.json"),
	)
	run := func(label, prompt, marker string, selection ...string) app.HeadlessResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		args := []string{"--config", isolatedConfig, "--headless", "--output-format", "json",
			"--provider", "glm", "--tools", "",
			"--max-turns", "2", "--timeout", "90s", "--max-budget-usd", "0.10"}
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, selection...)
		args = append(args, "--prompt", prompt)
		command := exec.CommandContext(ctx, binary, args...)
		command.Dir, command.Env = work, environment
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("%s: %v: %s", label, err, strings.ReplaceAll(stderr.String(), credential.Value, "[redacted]"))
		}
		var result app.HeadlessResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("%s emitted invalid JSON: %v", label, err)
		}
		if result.Status != "success" || !strings.Contains(result.Result, marker) {
			t.Fatalf("%s: status=%s, expected marker absent=%t", label, result.Status, !strings.Contains(result.Result, marker))
		}
		if result.Usage.InputTokens == 0 || result.Usage.ModelRounds != 1 {
			t.Fatalf("%s did not report one measured model round: %+v", label, result.Usage)
		}
		t.Logf("%s passed; input=%d output=%d", label, result.Usage.InputTokens, result.Usage.OutputTokens)
		return result
	}
	sessionID, marker, forkMarker := uuid.NewString(), "gokin-memory-"+uuid.NewString(), "gokin-fork-"+uuid.NewString()
	first := run("create", "Remember this exact marker for this conversation: "+marker+". Reply with only the marker. Do not use tools.", marker, "--session-id", sessionID)
	second := run("resume", "What exact marker did I ask you to remember? Reply with only the marker. Do not use tools.", marker, "--resume", sessionID)
	if first.SessionID != sessionID || second.SessionID != sessionID {
		t.Fatal("explicit session identity was not preserved")
	}
	fork := run("fork", "In this fork replace the remembered marker with "+forkMarker+". Reply with only the new marker. Do not use tools.", forkMarker, "--resume", sessionID, "--fork-session")
	if fork.SessionID == "" || fork.SessionID == sessionID {
		t.Fatal("fork did not receive a separate session identity")
	}
	parent := run("parent-after-fork", "What exact marker did I ask you to remember? Reply with only the marker. Do not use tools.", marker, "--resume", sessionID)
	if parent.SessionID != sessionID || strings.Contains(parent.Result, forkMarker) {
		t.Fatal("fork modified its parent conversation")
	}
}
