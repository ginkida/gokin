package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestRunnerGetResultExposesLiveAgentTranscript(t *testing.T) {
	const streamedText = "tests are still running\n"
	mock := &cancelToolHistoryClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{
			Text: streamedText,
			FunctionCalls: []*genai.FunctionCall{{
				ID: "live-output-write", Name: "write",
				Args: map[string]any{"file_path": "result.txt"},
			}},
		},
		{Done: true, FinishReason: genai.FinishReasonStop},
	}})

	started := make(chan struct{})
	release := make(chan struct{})
	writeTool := &cancelToolHistoryWrite{started: started, release: release}
	registry := tools.NewRegistry()
	if err := registry.Register(writeTool); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(context.Background(), mock, registry, t.TempDir())
	agentID := runner.SpawnAsync(context.Background(), "general", "run the long tests", 3, "")
	if agentID == "" {
		t.Fatal("SpawnAsync returned an empty agent ID")
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not reach the blocking tool")
	}

	live, ok := runner.GetResult(agentID)
	if !ok {
		t.Fatal("running agent has no result ledger entry")
	}
	if live.Status != AgentStatusRunning || live.Completed {
		t.Fatalf("live status/completed = %s/%v, want running/false", live.Status, live.Completed)
	}
	if live.OutputFile == "" {
		t.Fatal("running agent did not expose OutputFile")
	}
	data, err := os.ReadFile(live.OutputFile)
	if err != nil {
		t.Fatalf("read live output: %v", err)
	}
	if string(data) != streamedText {
		t.Fatalf("live transcript = %q, want %q", data, streamedText)
	}

	if err := runner.Cancel(agentID); err != nil {
		t.Fatal(err)
	}
	close(release)
	final, err := runner.WaitWithTimeout(agentID, 2*time.Second)
	if err != nil {
		t.Fatalf("wait cancelled agent: %v", err)
	}
	if final.Status != AgentStatusCancelled || !final.Completed {
		t.Fatalf("final status/completed = %s/%v, want cancelled/true", final.Status, final.Completed)
	}
	if final.OutputFile != live.OutputFile {
		t.Fatalf("output path changed at completion: %q -> %q", live.OutputFile, final.OutputFile)
	}
	data, err = os.ReadFile(final.OutputFile)
	if err != nil {
		t.Fatalf("read final transcript: %v", err)
	}
	if count := strings.Count(string(data), streamedText); count != 1 {
		t.Fatalf("streamed text occurs %d times, want exactly once; transcript=%q", count, data)
	}
}

func TestAgentOutputSurvivesIsolatedWorkspaceCleanupBoundary(t *testing.T) {
	durableDir := t.TempDir()
	isolatedDir := t.TempDir()
	mock := &cancelToolHistoryClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueText("durable transcript")

	agent := NewAgent(
		AgentTypeGeneral,
		mock,
		tools.NewRegistry(),
		durableDir,
		1,
		"",
		nil,
		nil,
	)
	// Runner.newConfiguredAgent changes only workDir when isolation is enabled.
	// The transcript must remain rooted in the original durable workspace.
	agent.workDir = isolatedDir

	result, err := agent.Run(context.Background(), "answer once")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(durableDir, ".gokin", "agent-output") + string(os.PathSeparator)
	if !strings.HasPrefix(result.OutputFile, wantPrefix) {
		t.Fatalf("OutputFile = %q, want prefix %q", result.OutputFile, wantPrefix)
	}
	if strings.HasPrefix(result.OutputFile, isolatedDir+string(os.PathSeparator)) {
		t.Fatalf("OutputFile unexpectedly rooted in disposable workspace: %q", result.OutputFile)
	}
	data, err := os.ReadFile(result.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "durable transcript" {
		t.Fatalf("transcript = %q", data)
	}
}

func TestAgentOutputFilePathContainsRestoredIDs(t *testing.T) {
	base := t.TempDir()
	path := agentOutputFilePath(base, "../../escape")
	wantDir := filepath.Join(base, ".gokin", "agent-output")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("unsafe output path %q escaped %q", path, wantDir)
	}
}
