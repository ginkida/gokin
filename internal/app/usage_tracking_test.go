package app

import (
	"context"
	"io"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
)

func TestExecuteTrackedCountsEveryMetadataFreeModelCall(t *testing.T) {
	zeroMetadata := func(text string) testkit.ResponseScript {
		return testkit.ResponseScript{Chunks: []client.ResponseChunk{
			{Text: text},
			{Done: true},
		}}
	}
	mock := testkit.NewMockClient().
		EnqueueScript(zeroMetadata("first draft")).
		EnqueueScript(zeroMetadata("second draft"))
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	application.setPresenter(newStdoutPresenter(io.Discard))

	var usage turnUsageAccumulator
	if _, _, err := application.executeTracked(context.Background(), nil, "same prompt", &usage); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	firstInput := usage.input
	if _, _, err := application.executeTracked(context.Background(), nil, "same prompt", &usage); err != nil {
		t.Fatalf("second Execute: %v", err)
	}

	if usage.samples != 2 {
		t.Fatalf("samples = %d, want 2", usage.samples)
	}
	if firstInput == 0 || usage.input != firstInput*2 {
		t.Fatalf("fallback input = first %d total %d, want both requests", firstInput, usage.input)
	}
	if usage.output == 0 {
		t.Fatalf("fallback output was not recorded: %+v", usage)
	}
}

func TestExecuteTrackedCapturesAttemptBeforePanicRecovery(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("unreachable")
	mock.OnSend = func(context.Context) { panic("provider callback panic") }
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	var usage turnUsageAccumulator
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("executeTracked did not propagate the panic")
			}
		}()
		_, _, _ = application.executeTracked(
			context.Background(), nil, "attempt before panic", &usage)
	}()

	if usage.samples != 1 || usage.input == 0 {
		t.Fatalf("panic attempt usage = %+v, want one input-bearing sample", usage)
	}
}
