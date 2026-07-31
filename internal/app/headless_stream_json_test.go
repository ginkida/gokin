package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestRunHeadlessWithOptions_StreamJSONEmitsProgressAndTerminalResult(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("inspect", map[string]any{"path": "main.go"}).
		EnqueueText("streamed answer")
	tool := &appHeadlessScriptedTool{
		name:    "inspect",
		results: []tools.ToolResult{tools.NewSuccessResult("inspection complete")},
	}
	application, _ := newHeadlessPolicyTestApp(t, mock, tool)

	var stdout bytes.Buffer
	returned, err := application.RunHeadlessWithOptions(context.Background(), "inspect then answer", HeadlessOptions{
		OutputFormat: HeadlessOutputStreamJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err != nil {
		t.Fatalf("RunHeadlessWithOptions: %v", err)
	}

	records := decodeHeadlessJSONL(t, stdout.Bytes())
	if len(records) < 4 {
		t.Fatalf("stream emitted only %d records: %s", len(records), stdout.String())
	}

	var (
		sawToolStart  bool
		sawToolResult bool
		sawDelta      bool
		resultCount   int
		lastSequence  uint64
	)
	for i, raw := range records {
		var header struct {
			Type     string `json:"type"`
			Sequence uint64 `json:"sequence"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			t.Fatalf("decode header %d: %v", i, err)
		}
		if header.Type == "result" {
			resultCount++
			if i != len(records)-1 {
				t.Fatalf("terminal result is record %d of %d", i+1, len(records))
			}
			continue
		}
		if header.Sequence != lastSequence+1 {
			t.Fatalf("event sequence = %d after %d", header.Sequence, lastSequence)
		}
		lastSequence = header.Sequence
		switch header.Type {
		case "tool_start":
			sawToolStart = true
		case "tool_result":
			sawToolResult = true
		case "assistant_delta":
			sawDelta = true
		}
	}
	if !sawToolStart || !sawToolResult || !sawDelta {
		t.Fatalf("missing progress events: start=%v result=%v delta=%v\n%s",
			sawToolStart, sawToolResult, sawDelta, stdout.String())
	}
	if resultCount != 1 {
		t.Fatalf("terminal result count = %d", resultCount)
	}

	var terminal HeadlessResult
	if err := json.Unmarshal(records[len(records)-1], &terminal); err != nil {
		t.Fatalf("decode terminal result: %v", err)
	}
	if terminal.Status != "success" || terminal.Result != "streamed answer" {
		t.Fatalf("terminal result = %+v", terminal)
	}
	if returned.Status != terminal.Status || returned.Result != terminal.Result {
		t.Fatalf("returned result diverges from terminal JSON: %+v vs %+v", returned, terminal)
	}
}

func TestRunHeadlessWithOptions_StreamJSONEarlyFailureIsSingleTerminalRecord(t *testing.T) {
	var stdout bytes.Buffer
	var application *App
	_, err := application.RunHeadlessWithOptions(context.Background(), "answer", HeadlessOptions{
		OutputFormat: HeadlessOutputStreamJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil {
		t.Fatal("nil app unexpectedly succeeded")
	}
	records := decodeHeadlessJSONL(t, stdout.Bytes())
	if len(records) != 1 {
		t.Fatalf("early failure emitted %d records: %s", len(records), stdout.String())
	}
	var terminal HeadlessResult
	if err := json.Unmarshal(records[0], &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "result" || terminal.Status != "error" || terminal.Error == nil || terminal.Error.Kind != "app_init" {
		t.Fatalf("terminal failure = %+v", terminal)
	}
}

func TestStreamJSONPresenterDoesNotEmitThinking(t *testing.T) {
	var stdout bytes.Buffer
	presenter := newStreamJSONPresenter(&stdout, "session-1", nil)
	presenter.StreamThinking("private chain of thought")
	presenter.StreamText("public answer")
	presenter.Finish()

	if strings.Contains(stdout.String(), "private chain of thought") {
		t.Fatalf("thinking leaked into stream: %s", stdout.String())
	}
	records := decodeHeadlessJSONL(t, stdout.Bytes())
	if len(records) != 1 {
		t.Fatalf("records = %d, want one assistant delta", len(records))
	}
}

func TestStreamJSONPresenterRedactsToolEventSecrets(t *testing.T) {
	var stdout bytes.Buffer
	presenter := newStreamJSONPresenter(&stdout, "session-1", nil)
	presenter.ToolStart("bash", map[string]any{
		"command": "curl -H 'Authorization: Bearer secret-token-value-123456' https://example.test",
	})
	presenter.ToolEnd("bash", nil, tools.NewErrorResult(
		"failed with api_key=super-secret-value-123456"))

	output := stdout.String()
	for _, secret := range []string{"secret-token-value-123456", "super-secret-value-123456"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q leaked into stream: %s", secret, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("stream does not show redaction marker: %s", output)
	}
}

func TestHeadlessStreamStateKeepsSequenceAcrossPresenters(t *testing.T) {
	var stdout bytes.Buffer
	state := NewHeadlessStreamState()
	first := newStreamJSONPresenter(&stdout, "session-1", state)
	first.Warning("one")
	second := newStreamJSONPresenter(&stdout, "session-1", state)
	second.Warning("two")

	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	for want := uint64(1); want <= 2; want++ {
		var event HeadlessStreamEvent
		if err := dec.Decode(&event); err != nil {
			t.Fatalf("decode event %d: %v", want, err)
		}
		if event.Sequence != want {
			t.Fatalf("event sequence = %d, want %d", event.Sequence, want)
		}
	}
}

func TestRunHeadlessWithOptions_StreamJSONWriteFailureIsTerminal(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("answer")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	returned, err := application.RunHeadlessWithOptions(context.Background(), "answer", HeadlessOptions{
		OutputFormat: HeadlessOutputStreamJSON,
		Stdout:       closedPipeWriter{},
		Stderr:       io.Discard,
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want closed pipe", err)
	}
	if returned.Status != "error" || returned.Error == nil || returned.Error.Kind != "output" {
		t.Fatalf("returned output failure = %+v", returned)
	}
}

type closedPipeWriter struct{}

func (closedPipeWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func decodeHeadlessJSONL(t *testing.T, data []byte) []json.RawMessage {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var records []json.RawMessage
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode stream-json %q: %v", string(data), err)
		}
		records = append(records, raw)
	}
	return records
}
