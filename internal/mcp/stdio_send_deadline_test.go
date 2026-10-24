package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type observedMCPPipeReader struct {
	io.Reader
	entered chan struct{}
	once    sync.Once
}

func (r *observedMCPPipeReader) Read(data []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	return r.Reader.Read(data)
}

func TestMCPHandshakeDeadlineUnblocksStdioWrite(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	transport := &StdioTransport{stdin: writer, encoder: json.NewEncoder(writer)}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	client := &Client{ctx: t.Context(), transport: transport, config: &ServerConfig{Name: "blocked-reader"}, pending: map[int64]chan *JSONRPCMessage{}}
	done := make(chan error, 1)
	go func() { _, err := client.request(ctx, "initialize", map[string]any{}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handshake lost deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP handshake blocked in Send past its deadline")
		reader.Close()
		<-done
	}
}

func TestQueuedStdioSendCancellationDoesNotCloseActiveWriter(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	transport := &StdioTransport{stdin: writer, encoder: json.NewEncoder(writer)}
	active := make(chan error, 1)
	go func() { active <- transport.SendContext(t.Context(), &JSONRPCMessage{Method: "active"}) }()
	// Read one byte: the first send owns the encoder but still blocks on the pipe.
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	queued := make(chan error, 1)
	go func() { queued <- transport.SendContext(ctx, &JSONRPCMessage{Method: "queued"}) }()
	select {
	case err := <-queued:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued send: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("queued cancellation waited for the active writer")
	}
	go io.Copy(io.Discard, reader)
	if err := <-active; err != nil {
		t.Errorf("queued cancellation broke active send: %v", err)
	}
}

func TestMCPStdioCloseUnblocksOwnedReader(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	observed := &observedMCPPipeReader{Reader: reader, entered: make(chan struct{})}
	stderrDone := make(chan struct{})
	close(stderrDone)
	transport := &StdioTransport{cmd: cmd, stdout: reader, scanner: bufio.NewScanner(observed), stderrDone: stderrDone}
	done := make(chan error, 1)
	go func() { _, err := transport.Receive(); done <- err }()
	<-observed.entered
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("closed transport retained a blocked reader")
		writer.Close()
		<-done
	}
}
