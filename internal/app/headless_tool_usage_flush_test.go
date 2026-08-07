package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/toolusage"
)

// Headless never reaches gracefulShutdown, so it has to flush the durable
// stores itself. Without that the lifetime tool counts are lost in exactly the
// mode that runs evals and scripts — a live run wrote nothing at all until this
// was fixed, while looking like a working instrument.
//
// The batching threshold is well above one call, so a count that reaches disk
// here can only have come from the teardown flush.
func TestRunHeadlessFlushesToolUsageLedger(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("probe", map[string]any{}).
		EnqueueText("done")
	tool := &appHeadlessScriptedTool{
		name:    "probe",
		results: []tools.ToolResult{tools.NewSuccessResult("executed")},
	}
	app, exec := newHeadlessPolicyTestApp(t, mock, tool)

	path := filepath.Join(t.TempDir(), "tool_usage.json")
	app.toolUsage = toolusage.NewLedger(path)
	app.phaseMetrics = NewPhaseMetrics()
	app.toolMetrics = NewToolMetrics()
	// Mirror the builder's production wiring: the foreground counts arrive
	// through the executor's phase observer, so a test that skips it would
	// prove nothing about the path that actually runs.
	exec.SetPhaseObserver(func(tool string, d time.Duration, success bool) {
		app.recordToolPhaseOutcome(tool, d, success)
	})

	if _, err := captureHeadlessStdoutResult(t, func() error {
		return app.RunHeadless(context.Background(), "run the probe")
	}); err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("headless left no usage ledger on disk: %v", err)
	}
	var parsed struct {
		Counts map[string]int64 `json:"counts"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Counts["probe"] != 1 {
		t.Fatalf("ledger counts = %v, want probe:1 persisted by the headless flush", parsed.Counts)
	}
}
