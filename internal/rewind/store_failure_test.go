package rewind

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/fileutil"
	"gokin/internal/undo"
)

func TestRestoreVisibleWriteFailureDisablesFurtherCodeRestore(t *testing.T) {
	s, session, manager, work, _ := fixture(t)
	path := filepath.Join(work, "file")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	id := begin(t, s, session, "edit")
	edit(t, manager, path, "after")
	_, err := s.restore(id, true, false, nil, func(ops []undo.Operation) error {
		if err := undo.ApplyOperations(ops); err != nil {
			return err
		}
		return fmt.Errorf("parent fsync: %w", fileutil.ErrAtomicWriteCommitUncertain)
	})
	if !errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
		t.Fatalf("expected visible-write durability failure, got %v", err)
	}
	contents(t, path, "before")
	if !errors.Is(s.failure, fileutil.ErrAtomicWriteCommitUncertain) {
		t.Fatalf("store did not retain uncertainty: %v", s.failure)
	}
	_, again := s.Restore(id, true, false, nil)
	if again != err {
		t.Fatalf("subsequent restore did not fail closed with original error: %v", again)
	}
}

func TestRestoreReplayUncertaintyPreservesCauseAndDisablesCodeRestore(t *testing.T) {
	s, session, manager, work, _ := fixture(t)
	paths := []string{filepath.Join(work, "first"), filepath.Join(work, "second")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := begin(t, s, session, "edit both")
	for _, path := range paths {
		edit(t, manager, path, "after")
	}
	conversationErr := errors.New("conversation restore failed")
	calls := 0
	_, err := s.restore(id, true, true, func(*Snapshot) (func() error, error) {
		return nil, conversationErr
	}, func(ops []undo.Operation) error {
		calls++
		if calls == 1 {
			return undo.ApplyOperations(ops)
		}
		if err := undo.ApplyOperations(ops[:1]); err != nil {
			return err
		}
		return fmt.Errorf("parent fsync: %w", fileutil.ErrAtomicWriteCommitUncertain)
	})
	if !errors.Is(err, conversationErr) || !errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
		t.Errorf("restore lost an error cause: %v", err)
	}
	if !errors.Is(s.failure, fileutil.ErrAtomicWriteCommitUncertain) {
		t.Errorf("store did not retain replay uncertainty: %v", s.failure)
	}
	contents(t, paths[0], "after")
	contents(t, paths[1], "before")
	_, again := s.Restore(id, true, false, nil)
	if again != err {
		t.Errorf("subsequent restore did not return original failure: %v", again)
	}
}
