package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"gokin/internal/app"
	"gokin/internal/tools"

	"github.com/spf13/cobra"
)

func setHeadlessStdinTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := headlessStdinFirstByteTimeout
	headlessStdinFirstByteTimeout = timeout
	t.Cleanup(func() { headlessStdinFirstByteTimeout = previous })
}

func TestReadHeadlessStdinOpenIdlePipeTimesOut(t *testing.T) {
	setHeadlessStdinTimeout(t, 25*time.Millisecond)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetIn(reader)
	cmd.SetErr(&stderr)
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() { text, err := readHeadlessStdin(cmd); done <- result{text, err} }()
	select {
	case got := <-done:
		if got.err != nil || got.text != "" {
			t.Fatalf("idle stdin: %+v", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("readHeadlessStdin waited for EOF on an idle open pipe")
		writer.Close()
		<-done
		return
	}
	if time.Since(started) < headlessStdinFirstByteTimeout {
		t.Fatal("stdin skipped before first-byte bound")
	}
	if strings.Count(stderr.String(), "Warning:") != 1 || !strings.Contains(stderr.String(), "no stdin data received in 25ms") || !strings.Contains(stderr.String(), "< /dev/null") {
		t.Fatalf("timeout warning: %q", stderr.String())
	}
	if prompt, err := resolveHeadlessPrompt("prompt alone", nil, ""); err != nil || prompt != "prompt alone" {
		t.Fatalf("prompt-only input: %q %v", prompt, err)
	}
	if _, err := resolveHeadlessPrompt("", nil, ""); err == nil || !strings.Contains(err.Error(), "a prompt is required") {
		t.Fatalf("missing prompt error: %v", err)
	}
}

func TestReadHeadlessStdinWaitsForSlowFirstByte(t *testing.T) {
	setHeadlessStdinTimeout(t, 3*time.Second)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	producer := make(chan error, 1)
	go func() {
		time.Sleep(time.Second)
		_, err := io.WriteString(writer, "slow producer\n")
		writer.Close()
		producer <- err
	}()
	cmd := &cobra.Command{}
	cmd.SetIn(reader)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	text, readErr := readHeadlessStdin(cmd)
	reader.Close()
	if err := <-producer; err != nil {
		t.Fatal(err)
	}
	if readErr != nil || text != "slow producer\n" || stderr.Len() != 0 {
		t.Fatalf("slow first byte: %q %v stderr=%q", text, readErr, stderr.String())
	}
}

func TestReadHeadlessStdinFirstByteRemovesDeadline(t *testing.T) {
	setHeadlessStdinTimeout(t, 100*time.Millisecond)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	producer := make(chan error, 1)
	go func() {
		defer writer.Close()
		for index, piece := range []string{"first", " second", " third"} {
			if index > 0 {
				time.Sleep(150 * time.Millisecond)
			}
			if _, err := io.WriteString(writer, piece); err != nil {
				producer <- err
				return
			}
		}
		producer <- nil
	}()
	cmd := &cobra.Command{}
	cmd.SetIn(reader)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	started := time.Now()
	text, readErr := readHeadlessStdin(cmd)
	reader.Close()
	if err := <-producer; err != nil {
		t.Fatal(err)
	}
	if readErr != nil || text != "first second third" || stderr.Len() != 0 || time.Since(started) <= headlessStdinFirstByteTimeout {
		t.Fatalf("trickling stdin: %q %v stderr=%q", text, readErr, stderr.String())
	}
}

func TestReadHeadlessStdinImmediateEOFDoesNotWarn(t *testing.T) {
	reader, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cmd := &cobra.Command{}
	cmd.SetIn(reader)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	started := time.Now()
	text, readErr := readHeadlessStdin(cmd)
	if readErr != nil || text != "" || stderr.Len() != 0 || time.Since(started) > time.Second {
		t.Fatalf("EOF: %q %v stderr=%q elapsed=%v", text, readErr, stderr.String(), time.Since(started))
	}
}

func TestReadHeadlessStdinPreservesReadErrors(t *testing.T) {
	failure := errors.New("test stdin failure")
	for _, prefix := range []string{"", "x"} {
		cmd := &cobra.Command{}
		cmd.SetIn(io.MultiReader(strings.NewReader(prefix), iotest.ErrReader(failure)))
		if _, err := readHeadlessStdin(cmd); !errors.Is(err, failure) {
			t.Fatalf("stdin error with prefix %q: %v", prefix, err)
		}
	}
}

func TestHeadlessStreamInputWaitsOnIdleOpenPipe(t *testing.T) {
	setHeadlessStdinTimeout(t, 20*time.Millisecond)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessInputStream(ctx, &recordingHeadlessInputRunner{}, reader, app.HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard}, "test-session")
	}()
	select {
	case err := <-done:
		t.Fatalf("stream protocol stopped waiting for input: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream input did not honor cancellation")
	}
}

func TestHeadlessCLIIdleStdinUsesPromptOrReportsMissingPrompt(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, supplied := range []bool{true, false} {
		t.Run(map[bool]string{true: "prompt supplied", false: "missing prompt"}[supplied], func(t *testing.T) {
			root := t.TempDir()
			// Gokin and Claude state live outside the workspace: on Linux the
			// sandbox cannot mask a missing secret dir inside a writable grant.
			state := t.TempDir()
			cfg := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(cfg, []byte("api: {active_provider: glm, glm_key: test-key-for-fixture}\nmodel: {provider: glm, name: glm-5.3-flash, fallback_providers: []}\nengine: {mode: tools}\nmemory: {enabled: false}\nsession_memory: {enabled: false}\nhooks: {enabled: false}\nmcp: {enabled: false}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--print", "--output-format", "stream-json", "--bare", "--no-session-persistence", "--config", cfg, "--base-url", "http://127.0.0.1:1"}
			wantError := "prompt is required"
			if supplied {
				// Setup refusal happens after prompt resolution, before provider
				// initialization. This exercises startup without a provider call.
				args = append(args, "--prompt", "prompt alone", "--setup")
				wantError = "--setup requires an interactive terminal"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, args...)
			tools.KillProcessGroupOnCancel(cmd)
			cmd.Dir = root
			cmd.Env = os.Environ()
			for key, value := range map[string]string{
				"GOKIN_TEST_AGENT_CALLER_CLI": "1", "XDG_CONFIG_HOME": filepath.Join(state, "config"),
				"XDG_DATA_HOME": filepath.Join(state, "data"), "XDG_CACHE_HOME": filepath.Join(state, "cache"),
				"CLAUDE_CONFIG_DIR": filepath.Join(state, "claude"), "GOKIN_PROVIDER_HEALTH_FILE": filepath.Join(state, "health.json"),
				"GOKIN_MODEL": "", "GOKIN_BACKEND": "", "GOKIN_ENGINE_MODE": "tools",
			} {
				cmd.Env = replaceEnvironmentValue(cmd.Env, key, value)
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close() // Keep the inherited input open for the entire run.
			cmd.Stdin = reader
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			started := time.Now()
			runErr := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("CLI hung on idle stdin: %v stdout=%s stderr=%s", ctx.Err(), stdout.String(), stderr.String())
			}
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("CLI exit: %v stdout=%s stderr=%s", runErr, stdout.String(), stderr.String())
			}
			var result app.HeadlessResult
			lines := nonEmptyLines(stdout.String())
			if len(lines) == 0 {
				t.Fatalf("missing result: stderr=%s", stderr.String())
			}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil || result.Error == nil || !strings.Contains(result.Error.Message, wantError) {
				t.Fatalf("CLI result=%+v err=%v stdout=%s stderr=%s", result, err, stdout.String(), stderr.String())
			}
			if strings.Count(stderr.String(), "Warning: no stdin data received in 3s") != 1 || time.Since(started) < 3*time.Second {
				t.Fatalf("CLI stdin bound/warning: elapsed=%v stderr=%s", time.Since(started), stderr.String())
			}
		})
	}
}
