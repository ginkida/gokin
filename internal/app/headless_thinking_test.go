package app

import (
	"bytes"
	"context"
	"io"
	"testing"

	"gokin/internal/config"
	"gokin/internal/router"
	"gokin/internal/testkit"
)

func TestHeadlessGLMSelectsThinkingWithoutRouting(t *testing.T) {
	for _, tc := range []struct {
		name, prompt, effort, mode string
		manual                     int32
		wantAdaptive, wantLow      bool
	}{
		{name: "simple_auto", prompt: "What is 2+2?", wantAdaptive: true, wantLow: true},
		{name: "coding_auto", prompt: "Fix the failing unit test for ValidateEmail in internal/validation/email.go. Keep the API unchanged and run go test.", wantAdaptive: true},
		{name: "explicit_effort", prompt: "What is 2+2?", effort: "max"},
		{name: "manual_budget", prompt: "What is 2+2?", manual: 24576},
		{name: "thinking_off", prompt: "Fix the failing unit test", mode: "off", wantAdaptive: true, wantLow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := testkit.NewMockClient().EnqueueText("Done.")
			initial := int32(16384)
			if tc.manual > 0 {
				initial = tc.manual
			}
			mock.SetThinkingBudget(initial)
			application, executor := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
			application.config.API.ActiveProvider = "glm"
			application.config.Model.Provider = "glm"
			application.config.Model.Name = config.DefaultGLMModel
			application.config.Model.ReasoningEffort = tc.effort
			application.config.Model.ThinkingBudget = tc.manual
			application.config.Model.ThinkingMode = tc.mode
			application.taskRouter = router.NewRouter(nil, executor, nil, mock, application.registry, false, application.workDir)
			application.taskRouter.SetThinkingMode(tc.mode)
			var observed int32
			mock.OnSend = func(context.Context) { observed = mock.ThinkingBudget() }
			var output bytes.Buffer
			result, err := application.RunHeadlessWithOptions(context.Background(), tc.prompt, HeadlessOptions{
				OutputFormat: HeadlessOutputJSON, Stdout: &output, Stderr: io.Discard,
			})
			if err != nil || result.Status != "success" {
				t.Fatalf("headless request failed: %v, %s", err, output.String())
			}
			if len(mock.Calls()) != 1 {
				t.Fatalf("reasoning selection unexpectedly called a model or delegated: %d requests", len(mock.Calls()))
			}
			if !tc.wantAdaptive {
				if observed != initial {
					t.Fatalf("explicit setting changed: budget=%d, want %d", observed, initial)
				}
				return
			}
			if observed == initial {
				t.Fatal("automatic headless request retained the factory max budget")
			}
			if tc.wantLow != (config.GLMEffortForBudget(observed) == "low") {
				t.Fatalf("unexpected reasoning level: budget=%d effort=%s", observed, config.GLMEffortForBudget(observed))
			}
		})
	}
}
