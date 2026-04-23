package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

func seedMultilineFile(t *testing.T, path string, n int) {
	t.Helper()
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString(fmt.Sprintf("line %d\n", i))
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestReadText_TruncationHintWhenLimitHits(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "long.go")
	seedMultilineFile(t, target, 100)

	rt := NewReadTool(dir)
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": target,
		"limit":     5,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute: err=%v success=%v", err, result.Success)
	}
	if !strings.Contains(result.Content, "[Truncated:") {
		t.Errorf("missing truncation marker: %s", result.Content)
	}
	if !strings.Contains(result.Content, "offset=6") {
		t.Errorf("hint should suggest offset=6 (next line after limit=5): %s", result.Content)
	}
	if !strings.Contains(result.Content, "of at least") {
		t.Errorf("hint should note 'of at least N' to signal there's more: %s", result.Content)
	}
}

func TestReadText_NoTruncationHintWhenWholeFileFits(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "short.go")
	seedMultilineFile(t, target, 3)

	rt := NewReadTool(dir)
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": target,
		"limit":     100,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute: err=%v success=%v", err, result.Success)
	}
	if strings.Contains(result.Content, "[Truncated:") {
		t.Errorf("should NOT show truncation for complete read: %s", result.Content)
	}
}

// Edge: file has EXACTLY limit lines — the scanner reads all `limit` lines
// without ever advancing past them, so hasMoreAfterLimit stays false.
// Important because an over-eager hint here would train Kimi to always
// paginate, even on fully-consumed reads.
func TestReadText_ExactLimitDoesNotShowHint(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "exact.go")
	seedMultilineFile(t, target, 10)

	rt := NewReadTool(dir)
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": target,
		"limit":     10,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute: err=%v success=%v", err, result.Success)
	}
	if strings.Contains(result.Content, "[Truncated:") {
		t.Errorf("exact-match should not trigger hint: %s", result.Content)
	}
}
