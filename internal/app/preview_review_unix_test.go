//go:build unix

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func previewReviewExecutor(t *testing.T, maxFiles int) (string, *tools.Executor) {
	t.Helper()
	root := testkit.ResolvedTempDir(t)
	cfg := &config.Config{}
	cfg.Tools.ProactiveContext.Enabled = true
	cfg.Tools.ProactiveContext.MaxFiles = maxFiles
	cfg.Tools.ProactiveContext.MaxLinesPerFile = 40
	b := NewBuilder(cfg, root)
	t.Cleanup(b.cancel)
	b.session = chat.NewSession()
	b.registry = tools.NewRegistry()
	b.registry.MustRegister(tools.NewReadTool(root))
	b.executor = tools.NewExecutor(b.registry, nil, 5*time.Second)
	b.readTracker = tools.NewFileReadTracker()
	b.executor.SetReadTracker(b.readTracker)
	b.executor.SetToolCache(tools.NewToolResultCache(tools.DefaultCacheConfig()))
	if err := b.initIntegrations(); err != nil {
		t.Fatal(err)
	}
	return root, b.executor
}

func writePreviewReviewFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readPreviewReviewFile(t *testing.T, executor *tools.Executor, path string) tools.ToolResult {
	t.Helper()
	result, err := executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": path})
	if err != nil || !result.Success {
		t.Fatalf("read %s: %+v, %v", path, result, err)
	}
	return result
}

func TestCancelledPreviewDoesNotBlockSessionReset(t *testing.T) {
	root, executor := previewReviewExecutor(t, 1)
	source := writePreviewReviewFile(t, root, "foo.go", "package fixture\n")
	pipe := filepath.Join(root, "foo_test.go")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = executor.InvokeTool(ctx, "read", map[string]any{"file_path": source})
	}()
	// A nonblocking writer opens only once the preview has opened the FIFO.
	var writer *os.File
	deadline := time.After(3 * time.Second)
	for writer == nil {
		fd, err := syscall.Open(pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0600)
		if err == nil {
			writer = os.NewFile(uintptr(fd), pipe)
			break
		}
		select {
		case <-deadline:
			t.Fatal("preview did not open the FIFO")
		case <-time.After(time.Millisecond):
		}
	}
	resetDone := make(chan struct{})
	t.Cleanup(func() {
		// Nonempty content prevents the sibling scan from reopening the pipe.
		_, _ = writer.WriteString("package fixture\n")
		_ = writer.Close()
		select {
		case <-resetDone:
		case <-time.After(3 * time.Second):
			t.Error("reset did not complete after releasing preview I/O")
		}
	})
	cancel()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Error("executor did not abandon the cancelled read")
	}
	go func() { executor.ResetSession(); close(resetDone) }()
	select {
	case <-resetDone:
	case <-time.After(500 * time.Millisecond):
		t.Error("cancelled preview I/O blocked session reset")
	}
}

func TestUndeliveredPreviewRemainsAvailable(t *testing.T) {
	root, executor := previewReviewExecutor(t, 1)
	a := writePreviewReviewFile(t, root, "a.go", "package fixture\n")
	b := writePreviewReviewFile(t, root, "b.go", "package fixture\n")
	writePreviewReviewFile(t, root, "c.go", "package fixture\n")
	if first := readPreviewReviewFile(t, executor, a); !strings.Contains(first.Content, "### b.go") {
		t.Fatalf("first read did not preview b: %s", first.Content)
	}
	if duplicate := readPreviewReviewFile(t, executor, a); !strings.HasPrefix(duplicate.Content, "[Unchanged since turn ") {
		t.Fatalf("duplicate did not return a stub: %s", duplicate.Content)
	}
	// Remove a so it cannot consume the sole preview slot on the next read.
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if next := readPreviewReviewFile(t, executor, b); !strings.Contains(next.Content, "### c.go") {
		t.Fatalf("c was suppressed without ever being delivered: %s", next.Content)
	}
}

func TestChangedPreviewFileIsUncovered(t *testing.T) {
	root, executor := previewReviewExecutor(t, 3)
	a := writePreviewReviewFile(t, root, "a.go", "package fixture\n")
	writePreviewReviewFile(t, root, "b.go", "package fixture\n// old\n")
	c := writePreviewReviewFile(t, root, "c.go", "package fixture\n")
	if first := readPreviewReviewFile(t, executor, a); !strings.Contains(first.Content, "// old") {
		t.Fatalf("fixture did not preview b: %s", first.Content)
	}
	writePreviewReviewFile(t, root, "b.go", "package fixture\n// changed outside the executor\n")
	if next := readPreviewReviewFile(t, executor, c); !strings.Contains(next.Content, "// changed outside the executor") {
		t.Fatalf("changed b was suppressed by stale preview coverage: %s", next.Content)
	}
}
