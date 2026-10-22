package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/app"
)

type sdkStreamTestRunner struct {
	run     func(context.Context, string, app.HeadlessOptions) (app.HeadlessResult, error)
	control func(context.Context, json.RawMessage) (map[string]any, error)
}

func (r *sdkStreamTestRunner) RunHeadlessWithOptions(ctx context.Context, prompt string, opts app.HeadlessOptions) (app.HeadlessResult, error) {
	return r.run(ctx, prompt, opts)
}
func (r *sdkStreamTestRunner) HandleSDKControl(ctx context.Context, request json.RawMessage) (map[string]any, error) {
	return r.control(ctx, request)
}

func sdkTestReply(opts app.HeadlessOptions, text string, err error) (app.HeadlessResult, error) {
	result := app.HeadlessResult{Type: "result", Result: text, Status: "success"}
	if err != nil {
		result.Status = "cancelled"
	}
	if writeErr := json.NewEncoder(opts.Stdout).Encode(result); writeErr != nil {
		return result, writeErr
	}
	return result, err
}

func TestSDKControlsInterruptLiveTurnAndKeepSingleWriter(t *testing.T) {
	input, host := io.Pipe()
	output, sink := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = host.Close(); _ = output.Close(); _ = sink.Close() })
	started := make(chan struct{})
	finished := make(chan struct{})
	runner := &sdkStreamTestRunner{
		run: func(ctx context.Context, prompt string, opts app.HeadlessOptions) (app.HeadlessResult, error) {
			if prompt == "first" {
				close(started)
				<-ctx.Done()
				close(finished)
				return sdkTestReply(opts, prompt, ctx.Err())
			}
			select {
			case <-finished:
			default:
				t.Error("second turn overlapped the first writer")
			}
			return sdkTestReply(opts, prompt, nil)
		},
		control: func(context.Context, json.RawMessage) (map[string]any, error) {
			return map[string]any{"version": "fixture"}, nil
		},
	}
	frames := make(chan map[string]any, 8)
	go func() {
		decoder := json.NewDecoder(output)
		for {
			var frame map[string]any
			if decoder.Decode(&frame) != nil {
				return
			}
			frames <- frame
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessInputStream(t.Context(), runner, input, app.HeadlessOptions{Stdout: sink}, "session")
		_ = sink.Close()
	}()
	send := func(text string) {
		t.Helper()
		if _, err := io.WriteString(host, text+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		t.Helper()
		select {
		case frame := <-frames:
			return frame
		case <-time.After(5 * time.Second):
			t.Fatal("control protocol did not respond")
			return nil
		}
	}
	send(`{"type":"user","prompt":"first"}`)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn did not start")
	}
	send(`{"type":"control_request","request_id":"version-1","request":{"subtype":"get_binary_version"}}`)
	frame := next()
	if frame["type"] != "control_response" || frame["response"].(map[string]any)["request_id"] != "version-1" {
		t.Fatal("control blocked behind active model", frame)
	}
	send(`{"type":"user","prompt":"second"}`)
	send(`{"type":"control_request","request_id":"interrupt-1","request":{"subtype":"interrupt"}}`)
	_ = host.Close()
	var turns []string
	acknowledged := false
	for range 3 {
		frame := next()
		if frame["type"] == "result" {
			turns = append(turns, frame["result"].(string))
		} else {
			acknowledged = frame["response"].(map[string]any)["request_id"] == "interrupt-1"
		}
	}
	if !acknowledged || strings.Join(turns, "|") != "first|second" {
		t.Fatal("lost interrupt or FIFO turn", acknowledged, turns)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF did not drain the connection")
	}
}

func TestSDKControlCancellationAndPanicRemainCorrelated(t *testing.T) {
	input, host := io.Pipe()
	defer input.Close()
	defer host.Close()
	started := make(chan struct{})
	runner := &sdkStreamTestRunner{
		run: func(_ context.Context, prompt string, opts app.HeadlessOptions) (app.HeadlessResult, error) {
			return sdkTestReply(opts, prompt, nil)
		},
		control: func(ctx context.Context, data json.RawMessage) (map[string]any, error) {
			if strings.Contains(string(data), "panic") {
				panic("private diagnostic")
			}
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessInputStream(t.Context(), runner, input, app.HeadlessOptions{Stdout: &output}, "session")
	}()
	_, _ = io.WriteString(host, "{\"type\":\"control_request\",\"request_id\":\"slow\",\"request\":{\"subtype\":\"slow\"}}\n")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("control did not start")
	}
	_, _ = io.WriteString(host, "{\"type\":\"control_cancel_request\",\"request_id\":\"slow\"}\n{\"type\":\"control_request\",\"request_id\":\"panic\",\"request\":{\"subtype\":\"panic\"}}\n{\"type\":\"user\",\"prompt\":\"still usable\"}\n")
	_ = host.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled control left a blocked connection")
	}
	lines := nonEmptyLines(output.String())
	if len(lines) != 3 || strings.Contains(output.String(), "private diagnostic") {
		t.Fatal("bad control cancellation output", output.String())
	}
	for i, id := range []string{"slow", "panic"} {
		var frame map[string]any
		_ = json.Unmarshal([]byte(lines[i]), &frame)
		response := frame["response"].(map[string]any)
		if response["request_id"] != id || response["subtype"] != "error" {
			t.Fatal("uncorrelated failure", frame)
		}
	}
}

func TestSDKControlOnlyStreamAndDuplicateJSON(t *testing.T) {
	runner := &sdkStreamTestRunner{
		run: func(context.Context, string, app.HeadlessOptions) (app.HeadlessResult, error) {
			t.Fatal("control became a prompt")
			return app.HeadlessResult{}, nil
		},
		control: func(context.Context, json.RawMessage) (map[string]any, error) {
			return nil, fmt.Errorf("unsupported request")
		},
	}
	var output bytes.Buffer
	err := runHeadlessInputStream(t.Context(), runner, strings.NewReader("{\"type\":\"control_request\",\"request_id\":\"one\",\"request\":{\"subtype\":\"unknown\"}}\n"), app.HeadlessOptions{Stdout: &output}, "session")
	if err != nil || !strings.Contains(output.String(), `"request_id":"one"`) || strings.Contains(output.String(), `"type":"result"`) {
		t.Fatal("control-only stream failed", err, output.String())
	}
	output.Reset()
	err = runHeadlessInputStream(t.Context(), runner, strings.NewReader(`{"type":"control_request","request_id":"one","request_id":"two","request":{"subtype":"unknown"}}`), app.HeadlessOptions{Stdout: &output}, "session")
	if err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatal("ambiguous request was accepted", err, output.String())
	}
}

func TestSDKConnectionCancellationClosesBlockedInput(t *testing.T) {
	input, host := io.Pipe()
	defer input.Close()
	defer host.Close()
	ctx, cancel := context.WithCancel(t.Context())
	var output bytes.Buffer
	runner := &recordingHeadlessInputRunner{}
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessInputStream(ctx, runner, input, app.HeadlessOptions{Stdout: &output}, "session")
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked reader outlived connection cancellation")
	}
	if _, err := host.Write([]byte("late\n")); err == nil {
		t.Fatal("input remained open after teardown")
	}
}

func TestSDKOutputFramesStayAtomic(t *testing.T) {
	var output bytes.Buffer
	writer := &headlessConnectionWriter{out: &output}
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range 50 {
				if err := writeHeadlessControl(writer, fmt.Sprintf("%d-%d", worker, index), map[string]any{"value": strings.Repeat("x", 1024)}, nil); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	workers.Wait()
	lines := nonEmptyLines(output.String())
	if len(lines) != 400 {
		t.Fatal("lost JSON frames", len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal("interleaved JSON frame")
		}
	}
}
