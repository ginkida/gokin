package ui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// On a fresh install neither the state directory nor the history file exists.
// The reader wraps its errors, and os.IsNotExist cannot unwrap — so the callers'
// "no history yet, that's fine" branches were dead and boot logged a failure.
// errors.Is must see through the wrapping for both the missing DIRECTORY and the
// missing FILE.
func TestHistoryReadReportsNotExistThroughWrapping(t *testing.T) {
	root := t.TempDir()

	missingDir := filepath.Join(root, "never-created", "history")
	_, err := readPrivateHistoryFile(missingDir, 1024)
	if err == nil {
		t.Fatal("reading a missing history directory returned no error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory error is not recognisable as not-exist: %v", err)
	}

	missingFile := filepath.Join(root, "history")
	_, err = readPrivateHistoryFile(missingFile, 1024)
	if err == nil {
		t.Fatal("reading a missing history file returned no error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error is not recognisable as not-exist: %v", err)
	}
}
