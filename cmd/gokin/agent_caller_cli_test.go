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
	"testing"
	"time"

	"gokin/internal/app"
	backgroundstore "gokin/internal/background"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// Use this test binary as the real CLI in subprocess tests. Detached children
// inherit the marker, so os.Executable launches the same CLI entry point.
func TestMain(m *testing.M) {
	if os.Getenv("GOKIN_TEST_AGENT_CALLER_CLI") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAgentCallerCLIHeadlessAndBackgroundHaveSameDeniedRoundProfile(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "print", true: "background"}[detached], func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(root, "m.go"), "package main\n// before\n")
			write(filepath.Join(root, "go.mod"), "module example.com/agentcaller\n\ngo 1.23\n")
			cfg := filepath.Join(root, "config.yaml")
			write(cfg, "api: {active_provider: glm, glm_key: test-key-for-fixture}\nmodel: {provider: glm, name: glm-5.3-flash, fallback_providers: []}\nengine: {mode: tools}\nmemory: {enabled: false}\nsession_memory: {enabled: false}\nhooks: {enabled: false}\nmcp: {enabled: false}\ntools: {sandbox: false}\npermission:\n  rules: {bash: deny}\n")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				round := requests.Add(1)
				if round > 4 {
					// Reproduce the reported idle interval without using a provider:
					// an unwanted completion exchange waits for the caller timeout.
					select {
					case <-r.Context().Done():
					case <-time.After(5 * time.Second):
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
				calls := []struct {
					name string
					args map[string]any
				}{
					{"read", map[string]any{"file_path": "m.go"}},
					{"edit", map[string]any{"file_path": "m.go", "old_string": "// before", "new_string": "// after\n// documented"}},
					{"bash", map[string]any{"command": "go test ./..."}},
				}
				stop := "end_turn"
				if round <= 3 {
					call := calls[round-1]
					block, _ := json.Marshal(map[string]any{"type": "tool_use", "id": fmt.Sprintf("call-%d", round), "name": call.name, "input": call.args})
					fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":%s}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", block)
					stop = "tool_use"
				} else {
					io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Updated m.go; verification was denied.\"}}\n\n")
				}
				fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
			}))
			defer server.Close()
			env := os.Environ()
			for key, value := range map[string]string{
				"GOKIN_TEST_AGENT_CALLER_CLI": "1", "XDG_CONFIG_HOME": filepath.Join(root, "config"),
				"XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"),
				"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude"), "GOKIN_PROVIDER_HEALTH_FILE": filepath.Join(root, "health.json"),
				"GOKIN_GLM_KEY": "test-key-for-fixture", "GLM_API_KEY": "", "GOKIN_API_KEY": "",
				"GOKIN_MODEL": "", "GOKIN_BACKEND": "", "GOKIN_ENGINE_MODE": "tools", "GOKIN_NO_SELF_REVIEW": "",
			} {
				env = replaceEnvironmentValue(env, key, value)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			run := func(args ...string) (string, string, error) {
				cmd := exec.CommandContext(ctx, binary, args...)
				tools.KillProcessGroupOnCancel(cmd)
				cmd.Dir, cmd.Env = root, env
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				return stdout.String(), stderr.String(), err
			}
			args := []string{"--config", cfg, "--base-url", server.URL, "--no-plugins", "--tools", "read,edit,bash", "--permission-mode", "acceptEdits", "--timeout", "3s", "--prompt", "Update the comment in m.go"}
			if detached {
				args = append(args, "--bg")
			} else {
				args = append(args, "-p", "--output-format", "json")
			}
			started := time.Now()
			stdout, stderr, err := run(args...)
			if err != nil {
				t.Fatalf("CLI: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
			}
			if detached {
				store, err := backgroundstore.NewStoreAt(filepath.Join(root, "data", "gokin", "background"))
				if err != nil {
					t.Fatal(err)
				}
				jobs, err := store.List(root, true)
				if err != nil || len(jobs) != 1 {
					t.Fatalf("jobs=%v err=%v", jobs, err)
				}
				id := jobs[0].ID
				// Exercise the public follower and result commands against the
				// actual detached worker, including job metadata finalization.
				if out, errOut, err := run("logs", id, "--follow"); err != nil {
					t.Fatalf("logs: %v\n%s\n%s", err, out, errOut)
				}
				stdout, stderr, err = run("result", id, "--json")
				if err != nil {
					t.Fatalf("result: %v\n%s\n%s", err, stdout, stderr)
				}
			}
			var result app.HeadlessResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("decode result: %v\n%s\n%s", err, stdout, stderr)
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
				t.Fatal(err)
			}
			timing, _ := receipt["timing"].(map[string]any)
			if timing == nil || timing["startup_ms"] == nil || timing["run_ms"] == nil {
				t.Fatalf("missing CLI lifecycle timing: %s", stdout)
			}
			if detached {
				agentsJSON, agentsStderr, err := run("agents", "--all", "--json")
				if err != nil {
					t.Fatalf("agents: %v stdout=%s stderr=%s", err, agentsJSON, agentsStderr)
				}
				var jobs []map[string]any
				if err := json.Unmarshal([]byte(agentsJSON), &jobs); err != nil || len(jobs) != 1 {
					t.Fatalf("agents JSON=%s err=%v", agentsJSON, err)
				}
				job := jobs[0]
				var previous time.Time
				for _, key := range []string{"started_at", "running_at", "turn_started_at", "turn_ended_at", "ended_at"} {
					value, _ := job[key].(string)
					stamp, err := time.Parse(time.RFC3339Nano, value)
					if err != nil || stamp.Before(previous) {
						t.Fatalf("invalid job %s: %s", key, agentsJSON)
					}
					previous = stamp
				}
				jobTiming, _ := job["timing"].(map[string]any)
				if jobTiming["startup_ms"] == nil || jobTiming["run_ms"] == nil || jobTiming["shutdown_ms"] == nil {
					t.Fatalf("missing job timing: %s", agentsJSON)
				}
			}
			if result.Status != "success" || result.Verification.Status != "denied" || result.Incomplete || requests.Load() != 4 || result.Usage.ModelRounds != 4 {
				t.Fatalf("result=%+v requests=%d stderr=%s", result, requests.Load(), stderr)
			}
			data, err := os.ReadFile(filepath.Join(root, "m.go"))
			if err != nil || !strings.Contains(string(data), "// after\n// documented") {
				t.Fatalf("completed edit lost: %s %v", data, err)
			}
			t.Logf("provider requests=%d, CLI elapsed=%v, model duration=%dms", requests.Load(), time.Since(started), result.DurationMS)
		})
	}
}
