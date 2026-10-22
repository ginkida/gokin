//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessLSPServerFixture(t *testing.T) { testkit.RunLSPFixture(t) }

func TestHeadlessLSPCLIHelper(t *testing.T) {
	input := os.Getenv("GOKIN_TEST_HEADLESS_LSP_ARGS")
	if input == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"gokin"}, args...)
	main()
	// Check before this helper exits: process exit would close stdin and let
	// a compliant server quit on EOF even without application teardown.
	data, err := os.ReadFile(os.Getenv("GOKIN_TEST_HEADLESS_LSP_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(bytes.SplitN(data, []byte("\n"), 2)[0], &started); err != nil || started.PID <= 0 {
		t.Fatalf("missing language-server process identity: %s %v", data, err)
	}
	if err := syscall.Kill(started.PID, 0); err != syscall.ESRCH {
		_ = syscall.Kill(-started.PID, syscall.SIGKILL)
		t.Fatalf("language server still alive after headless entry point returned: %v", err)
	}
	os.Exit(0)
}

func TestHeadlessCLIClosesStartedLanguageServers(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	work, plugin := filepath.Join(root, "work"), filepath.Join(root, "plugin")
	logPath := filepath.Join(root, "lsp.jsonl")
	write(filepath.Join(work, "source.py"), []byte("x = 1\n"))
	manifest, err := json.Marshal(map[string]any{
		"name": "fixture",
		"lspServers": map[string]any{"python": map[string]any{
			"command": binary, "args": []string{"-test.run=^TestHeadlessLSPServerFixture$"},
			"extensionToLanguage": map[string]string{".py": "python"},
			"env":                 map[string]string{"GOKIN_LSP_FIXTURE": "1", "GOKIN_LSP_LOG": logPath},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
		stop := "end_turn"
		if requests.Add(1) == 1 {
			io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"lsp-call\",\"name\":\"lsp\",\"input\":{\"operation\":\"hover\",\"filePath\":\"source.py\",\"line\":1,\"character\":1}}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			stop = "tool_use"
		} else {
			io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Inspected.\"}}\n\n")
		}
		fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
	}))
	defer server.Close()
	cfg := filepath.Join(root, "config.yaml")
	write(cfg, []byte("api: {active_provider: glm, glm_key: test-key-for-fixture}\nmodel: {provider: glm, name: glm-5.3-flash, fallback_providers: []}\nengine: {mode: tools}\nmemory: {enabled: false}\nsession_memory: {enabled: false}\nhooks: {enabled: false}\nmcp: {enabled: false}\n"))
	args, _ := json.Marshal([]string{"--config", cfg, "--headless", "--output-format", "json", "--prompt", "Inspect source.py with lsp hover", "--base-url", server.URL, "--plugin-dir", plugin, "--no-session-persistence", "--tools", "lsp", "--permission-mode", "bypassPermissions", "--max-turns", "3", "--timeout", "15s"})
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestHeadlessLSPCLIHelper$")
	tools.KillProcessGroupOnCancel(command)
	command.Dir = work
	command.Env = append(os.Environ(),
		"GOKIN_TEST_HEADLESS_LSP_ARGS="+string(args),
		"GOKIN_TEST_HEADLESS_LSP_LOG="+logPath,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"),
		"GOKIN_PROVIDER_HEALTH_FILE="+filepath.Join(root, "health.json"),
		"GOKIN_GLM_KEY=test-key-for-fixture", "GLM_API_KEY=", "GOKIN_API_KEY=", "GOKIN_MODEL=", "GOKIN_BACKEND=", "GOKIN_ENGINE_MODE=tools",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("headless CLI: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), `"method":"textDocument/hover"`) {
		t.Fatalf("headless run did not start/query LSP: %s %v\n%s\n%s", data, err, stdout.String(), stderr.String())
	}
}
