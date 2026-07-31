package app

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestTrySteerHeadlessInjectsFollowUpIntoActiveLoop(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("probe", map[string]any{}).
		EnqueueText("done").
		EnqueueText("adjusted")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mock.OnSend = func(ctx context.Context) {
		once.Do(func() {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{
		name:    "probe",
		results: []tools.ToolResult{tools.NewSuccessResult("ok")},
	})
	done := make(chan error, 1)
	go func() {
		_, err := application.RunHeadlessWithOptions(context.Background(), "initial", HeadlessOptions{
			OutputFormat:         HeadlessOutputStreamJSON,
			Stdout:               io.Discard,
			Stderr:               io.Discard,
			InlineExternalSteers: true,
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("headless model call did not start")
	}
	if !application.TrySteerHeadless("also verify the edge case") {
		t.Fatal("active headless loop rejected external steer")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunHeadlessWithOptions: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("headless run did not finish")
	}

	calls := mock.Calls()
	if len(calls) < 3 {
		t.Fatalf("model calls = %d, want follow-up after tool", len(calls))
	}
	found := false
	if strings.Contains(calls[2].Message, "also verify the edge case") {
		found = true
	}
	for _, content := range calls[2].History {
		for _, part := range content.Parts {
			if strings.Contains(part.Text, "also verify the edge case") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("external steer was not injected into next model iteration")
	}
}
