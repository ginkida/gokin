package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// Binary files used to return Success=true with a "Binary file..." text
// payload — Kimi treated that as "file read, I'm done" and would then
// confidently describe contents it never saw. The fix: Success=false so
// the model knows to recover (use a different tool, give up on the
// file, or explicitly ask).
func TestReadTool_BinaryFileReturnsErrorNotSuccess(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "blob.bin")
	// Embed null bytes in the first 512 bytes so isBinaryFile trips even
	// if the .bin extension isn't in the extension-based denylist.
	data := []byte{0x00, 0x01, 0x02, 0x00, 'H', 'i', 0x00, 0xFF}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rt := NewReadTool(dir)
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": target,
	})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success {
		t.Fatal("binary file read should NOT report Success=true — model will believe it read the file")
	}
	if !strings.Contains(result.Error, "Binary file") {
		t.Errorf("expected 'Binary file' marker in error: %q", result.Error)
	}
	if !strings.Contains(result.Error, "Do not claim") {
		t.Errorf("expected anti-hallucination directive: %q", result.Error)
	}
}
