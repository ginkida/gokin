package undo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gokin/internal/fileutil"
)

// Operation is a committed file edit or an explicit undo of that edit.
// Keeping both directions makes turn checkpoints independent of the bounded
// interactive undo stack, including after /undo, /redo and session restart.
type Operation struct {
	Change  FileChange `json:"change"`
	Undo    bool       `json:"undo,omitempty"`
	Missing string     `json:"missing,omitempty"`
}

// MarkUntracked prevents a partial checkpoint from being offered as a full
// code restore when a tool could not capture one of its file changes.
func (m *Manager) MarkUntracked(path, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observe([]Operation{{Change: FileChange{FilePath: path}, Missing: reason}})
}

// SetObserver installs a synchronous journal writer. It must not call Manager
// methods: notifications run under the same lock as the committed mutation.
func (m *Manager) SetObserver(observer func([]Operation)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observer = observer
}

func (m *Manager) observe(operations []Operation) {
	if m.observer != nil {
		m.observer(operations)
	}
}

func (m *Manager) observeChanges(changes []*FileChange, reverse bool) {
	operations := make([]Operation, len(changes))
	for i, change := range changes {
		operations[i] = Operation{Change: *change, Undo: reverse}
	}
	m.observe(operations)
}

// ReverseOperations returns the inverse transaction in execution order.
func ReverseOperations(operations []Operation) []Operation {
	result := make([]Operation, len(operations))
	for i, operation := range operations {
		operation.Undo = !operation.Undo
		result[len(operations)-1-i] = operation
	}
	return result
}

// ApplyOperations preflights the entire timeline before writing any path and
// rolls back successful writes if a later operation fails. The caller must
// exclude concurrent tools while restoring the conversation and journal.
func ApplyOperations(operations []Operation) error {
	return applyOperations(operations, NewManager().applyOperation)
}

func applyOperations(operations []Operation, apply func(Operation) error) error {
	if err := preflightOperations(operations); err != nil {
		return err
	}
	for i, operation := range operations {
		if err := apply(operation); err != nil {
			if errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
				return fmt.Errorf("restore failed after a visible write; not rolled back: %w", err)
			}
			rollback := ReverseOperations(operations[:i])
			if rollbackErr := preflightOperations(rollback); rollbackErr != nil {
				return fmt.Errorf("restore failed: %w; rollback incomplete: %v", err, rollbackErr)
			}
			for _, previous := range rollback {
				if rollbackErr := apply(previous); rollbackErr != nil {
					return fmt.Errorf("restore failed: %w; rollback incomplete: %v", err, rollbackErr)
				}
			}
			return fmt.Errorf("restore failed (rolled back): %w", err)
		}
	}
	return nil
}

func preflightOperations(operations []Operation) error {
	view := newPathStateView()
	for _, operation := range operations {
		if operation.Missing != "" {
			return fmt.Errorf("checkpoint contains an uncaptured file change: %s", operation.Missing)
		}
		change := operation.Change
		for _, path := range changePaths(&change) {
			if err := validateRestorePath(path); err != nil {
				return err
			}
		}
		for _, dir := range change.CreatedDirs {
			if err := validateRestorePath(dir); err != nil {
				return err
			}
			relative, err := filepath.Rel(dir, change.FilePath)
			if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
				return fmt.Errorf("invalid checkpoint directory ancestry")
			}
		}
		var err error
		if operation.Undo {
			err = simulateUndo(&change, view)
		} else {
			err = simulateRedo(&change, view)
		}
		if err != nil {
			return fmt.Errorf("cannot restore checkpoint: %w", err)
		}
	}
	return nil
}

func validateRestorePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("checkpoint path must be absolute and clean")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkpoint path contains a symbolic link: %s", current)
		}
		if err == nil && current == path && info.Mode().IsRegular() {
			linked, err := restoreHasHardLinks(path, info)
			if err != nil {
				return err
			}
			if linked {
				return fmt.Errorf("checkpoint path has multiple hard links: %s", path)
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

// ExclusiveRestore prevents a journal callback from racing the restore. Code
// restoration invalidates the rolling undo/redo stacks, whose old postimages
// would otherwise describe file states that no longer exist.
func (m *Manager) ExclusiveRestore(code bool, apply func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := apply(); err != nil {
		return err
	}
	if code {
		m.tracker.Clear()
		m.undone = nil
		m.mutations++
	}
	return nil
}

func (m *Manager) applyOperation(operation Operation) error {
	// Earlier operations can change ancestry after the transaction preflight.
	for _, path := range append(changePaths(&operation.Change), operation.Change.CreatedDirs...) {
		if err := validateRestorePath(path); err != nil {
			return err
		}
	}
	if operation.Undo {
		return m.revertChange(&operation.Change)
	}
	return m.applyChange(&operation.Change)
}
