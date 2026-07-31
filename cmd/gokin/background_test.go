package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	backgroundstore "gokin/internal/background"

	"github.com/spf13/cobra"
)

func TestBackgroundChildArgsForcesStreamWorkerMode(t *testing.T) {
	got := backgroundChildArgs([]string{
		"--bg",
		"--output-format=json",
		"--input-format", "text",
		"--debug", "api",
		"--",
		"-prompt-that-starts-with-dash",
	})
	want := []string{
		"--debug", "api",
		"--print", "--output-format", "stream-json", "--input-format", "text",
		"--",
		"-prompt-that-starts-with-dash",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backgroundChildArgs = %#v, want %#v", got, want)
	}
	for _, arg := range got {
		if arg == "--bg" || arg == "--background" {
			t.Fatalf("child retained detach flag: %#v", got)
		}
	}
}

func TestRespawnChildArgsPreserveExplicitRuntimeFlagsAndProvider(t *testing.T) {
	root := &cobra.Command{Use: "gokin"}
	var configPath, providerValue string
	var allowed, directories []string
	var fork, background bool
	root.PersistentFlags().StringVar(&configPath, "config", "", "")
	root.PersistentFlags().StringVar(&providerValue, "provider", "", "")
	root.PersistentFlags().StringSliceVar(&allowed, "allowed-tools", nil, "")
	root.PersistentFlags().StringArrayVar(&directories, "add-dir", nil, "")
	root.PersistentFlags().BoolVar(&fork, "fork-session", false, "")
	root.PersistentFlags().BoolVar(&background, "background", false, "")
	child := &cobra.Command{Use: "respawn"}
	root.AddCommand(child)

	if err := root.PersistentFlags().Set("config", "relative-config.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentFlags().Set("allowed-tools", `"Bash(foo,bar)"`); err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentFlags().Set("add-dir", "relative-extra"); err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentFlags().Set("fork-session", "true"); err != nil {
		t.Fatal(err)
	}

	got, err := respawnChildArgs(child, "session-42", "ollama", "-continue carefully")
	if err != nil {
		t.Fatal(err)
	}
	absoluteConfig, _ := filepath.Abs("relative-config.yaml")
	absoluteDir, _ := filepath.Abs("relative-extra")
	for _, want := range []string{
		"--config=" + absoluteConfig,
		`--allowed-tools="Bash(foo,bar)"`,
		"--add-dir=" + absoluteDir,
		"--fork-session=true",
		"--provider", "ollama",
		"--resume", "session-42",
		"--print",
		"--output-format", "stream-json",
		"--input-format", "text",
		"--", "-continue carefully",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("respawnChildArgs = %#v, missing %q", got, want)
		}
	}
	if slices.Contains(got, "--background=true") {
		t.Fatalf("respawn child retained detach flag: %#v", got)
	}
}

func TestValidateRespawnInvocationRejectsConflictingSessionFlags(t *testing.T) {
	root := &cobra.Command{Use: "gokin"}
	var resume string
	root.PersistentFlags().StringVar(&resume, "resume", "", "")
	child := &cobra.Command{Use: "respawn"}
	root.AddCommand(child)
	if err := root.PersistentFlags().Set("resume", "another-session"); err != nil {
		t.Fatal(err)
	}
	if err := validateRespawnInvocation(child); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("validateRespawnInvocation() error = %v", err)
	}
}

func TestRespawnChildArgsReadParsedInheritedFlags(t *testing.T) {
	root := &cobra.Command{Use: "gokin"}
	var baseURLValue string
	root.PersistentFlags().StringVar(&baseURLValue, "base-url", "", "")
	var got []string
	child := &cobra.Command{
		Use: "respawn",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			got, err = respawnChildArgs(cmd, "session-42", "ollama", "continue")
			return err
		},
	}
	root.AddCommand(child)
	root.SetArgs([]string{"respawn", "--base-url", "http://127.0.0.1:18765"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--base-url=http://127.0.0.1:18765") {
		t.Fatalf("respawnChildArgs = %#v, missing parsed inherited base URL", got)
	}
}

func TestBackgroundWorkerPublishesAndFinalizesDurableState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	id := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: id, State: backgroundstore.StateStarting,
		WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(backgroundJobEnv, id)

	worker, err := beginBackgroundWorker()
	if err != nil {
		t.Fatalf("beginBackgroundWorker: %v", err)
	}
	if os.Getenv(backgroundJobEnv) != "" {
		t.Fatal("worker marker leaked to child-process environment")
	}
	if err := worker.setSessionID("session-42"); err != nil {
		t.Fatal(err)
	}
	running, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != backgroundstore.StateRunning || running.PID != os.Getpid() || running.SessionID != "session-42" {
		t.Fatalf("running state = %+v", running)
	}
	worker.finish(nil)
	finished, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != backgroundstore.StateSucceeded || finished.ExitCode != 0 {
		t.Fatalf("finished state = %+v", finished)
	}
}

func TestBackgroundAgentsCommandJSONAndWorkspaceFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	id := backgroundstore.NewJobID()
	parentID := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: id, ParentJobID: parentID, State: backgroundstore.StateStarting,
		WorkDir: cwd, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	command := newBackgroundAgentsCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--json", "--cwd", cwd})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var jobs []backgroundstore.Job
	if err := json.Unmarshal(out.Bytes(), &jobs); err != nil {
		t.Fatalf("decode agents JSON: %v\n%s", err, out.String())
	}
	if len(jobs) != 1 || jobs[0].ID != id || jobs[0].ParentJobID != parentID {
		t.Fatalf("jobs = %+v", jobs)
	}

	command = newBackgroundAgentsCmd()
	out.Reset()
	command.SetOut(&out)
	command.SetArgs([]string{"--cwd", cwd})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "parent="+parentID[:8]) {
		t.Fatalf("text agents output omitted lineage: %q", out.String())
	}
}

func TestBackgroundRespawnRejectsLiveAndUnresolvedJobs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	liveID := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: liveID, SessionID: "live-session", State: backgroundstore.StateStarting,
		WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	command := newBackgroundRespawnCmd()
	command.SetArgs([]string{liveID, "continue"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("live respawn error = %v", err)
	}

	unresolvedID := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: unresolvedID, SessionID: "unresolved-session", State: backgroundstore.StateStarting,
		WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(unresolvedID, 1234); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueControl(unresolvedID, "uncommitted input"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(unresolvedID, backgroundstore.StateFailed, 1); err != nil {
		t.Fatal(err)
	}
	command = newBackgroundRespawnCmd()
	command.SetArgs([]string{unresolvedID[:8], "continue"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unresolved input") {
		t.Fatalf("unresolved respawn error = %v", err)
	}
}

func TestBackgroundLogsCommandTailsPrivateLogs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	id := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: id, State: backgroundstore.StateStarting,
		WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath, _ := store.StdoutPath(id)
	stderrPath, _ := store.StderrPath(id)
	if err := os.WriteFile(stdoutPath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrPath, []byte("warning\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := newBackgroundLogsCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{id[:8], "--lines", "2"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "one") || !strings.Contains(stdout.String(), "two\nthree") {
		t.Fatalf("stdout tail = %q", stdout.String())
	}
	if stderr.String() != "warning\n" {
		t.Fatalf("stderr tail = %q", stderr.String())
	}

	for _, path := range []string{stdoutPath, stderrPath} {
		if !strings.HasPrefix(path, filepath.Join(store.Root(), "logs")+string(os.PathSeparator)) {
			t.Fatalf("log escaped private root: %s", path)
		}
	}
}
