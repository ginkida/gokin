package undo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/fileutil"
)

func TestTimelineVisibleWriteFailureDoesNotClaimRollback(t *testing.T) {
	root := timelineRoot(t)
	paths := []string{filepath.Join(root, "first"), filepath.Join(root, "second")}
	var operations []Operation
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, Operation{Change: *NewFileChange(path, "edit", []byte("old"), []byte("new"), false), Undo: true})
	}
	m := NewManager()
	err := applyOperations(operations, func(op Operation) error {
		if err := m.applyOperation(op); err != nil {
			return err
		}
		// Inject the post-rename parent-fsync failure after the real write.
		if op.Change.FilePath == paths[1] {
			return fmt.Errorf("parent fsync: %w", fileutil.ErrAtomicWriteCommitUncertain)
		}
		return nil
	})
	if !errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
		t.Fatalf("expected visible-write durability failure, got %v", err)
	}
	if strings.Contains(err.Error(), "rolled back") && !strings.Contains(err.Error(), "not rolled back") {
		t.Fatalf("restore falsely claimed rollback: %v", err)
	}
	for _, path := range paths {
		if value, readErr := os.ReadFile(path); readErr != nil || string(value) != "old" {
			t.Fatalf("visible transaction prefix at %s = %q, %v", path, value, readErr)
		}
	}
}
