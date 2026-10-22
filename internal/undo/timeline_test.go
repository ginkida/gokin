package undo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func timelineRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func TestTimelineCapturesUndoAndRedoTransactions(t *testing.T) {
	for _, kind := range []string{"single", "group", "exact"} {
		t.Run(kind, func(t *testing.T) {
			m := NewManager()
			var events []Operation
			m.SetObserver(func(ops []Operation) { events = append(events, ops...) })
			path := filepath.Join(timelineRoot(t), "file")
			var ids []string
			for _, text := range []string{"one", "two"} {
				old, err := os.ReadFile(path)
				fresh := os.IsNotExist(err)
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				change := NewFileChange(path, "write", old, []byte(text), fresh)
				change.Mode = 0600
				ids = append(ids, change.ID)
				if kind == "group" {
					m.SetActiveGroup("request")
				}
				m.Record(*change)
			}
			var err error
			switch kind {
			case "single":
				_, err = m.Undo()
				if err == nil {
					_, err = m.Redo()
				}
			case "group":
				_, err = m.UndoLastGroup()
				if err == nil {
					_, err = m.RedoLastGroup()
				}
			case "exact":
				_, err = m.UndoChanges(ids)
				if err == nil {
					_, err = m.RedoChanges(ids)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyOperations(ReverseOperations(events)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("timeline did not restore baseline")
			}
			if err := ApplyOperations(events); err != nil {
				t.Fatal(err)
			}
			value, _ := os.ReadFile(path)
			if string(value) != "two" {
				t.Fatal("timeline replay lost final content")
			}
		})
	}
}
func TestTimelineRollsBackAfterDirectoryRemovalFailure(t *testing.T) {
	root := timelineRoot(t)
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manual"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	operations := []Operation{{Change: *NewFileChange(path, "edit", []byte("old"), []byte("new"), false), Undo: true}, {Change: FileChange{FilePath: dir, Tool: "mkdir"}, Undo: true}}
	if err := ApplyOperations(operations); err == nil {
		t.Fatal("nonempty directory removal succeeded")
	}
	value, _ := os.ReadFile(path)
	if string(value) != "new" {
		t.Fatal("earlier file revert was not rolled back")
	}
}
func TestTimelineRefusesHardLinksAndSymlinkParents(t *testing.T) {
	root := timelineRoot(t)
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "hard")
	if err := os.Link(path, link); err != nil {
		t.Skip(err)
	}
	op := Operation{Change: *NewFileChange(path, "edit", []byte("old"), []byte("current"), false), Undo: true}
	if err := ApplyOperations([]Operation{op}); err == nil {
		t.Fatal("hard-linked path restored")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "symlink")
	if err := os.Symlink(root, parent); err != nil {
		t.Skip(err)
	}
	op.Change.FilePath = filepath.Join(parent, "file")
	if err := ApplyOperations([]Operation{op}); err == nil {
		t.Fatal("symlink parent followed")
	}
	value, _ := os.ReadFile(path)
	if string(value) != "current" {
		t.Fatal("linked path modified")
	}
}

func TestTimelineDirectoryMoveAndChildEditsRoundTrip(t *testing.T) {
	for _, editFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("edit-first=%t", editFirst), func(t *testing.T) {
			root := timelineRoot(t)
			old, next := filepath.Join(root, "old"), filepath.Join(root, "new")
			if err := os.Mkdir(old, 0700); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(old, "f.go")
			if err := os.WriteFile(child, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			move := Operation{Change: *NewFileChange(next, "move", []byte(old), nil, false)}
			if !editFirst {
				child = filepath.Join(next, "f.go")
			}
			edit := Operation{Change: *NewFileChange(child, "edit", []byte("before"), []byte("after"), false)}
			ops := []Operation{move, edit}
			if editFirst {
				ops = []Operation{edit, move}
			}
			if err := ApplyOperations(ops); err != nil {
				t.Fatalf("forward: %v", err)
			}
			if err := ApplyOperations(ReverseOperations(ops)); err != nil {
				t.Fatalf("reverse: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(old, "f.go")); err != nil || string(got) != "before" {
				t.Fatalf("baseline=%q %v", got, err)
			}
			if err := ApplyOperations(ops); err != nil {
				t.Fatalf("replay: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(next, "f.go")); err != nil || string(got) != "after" {
				t.Fatalf("replayed=%q %v", got, err)
			}
		})
	}
}

func TestTimelineNestedDirectoryMovesPreserveChildState(t *testing.T) {
	root := timelineRoot(t)
	old, next, detached := filepath.Join(root, "old"), filepath.Join(root, "new"), filepath.Join(root, "detached")
	if err := os.MkdirAll(filepath.Join(old, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(old, "f"), filepath.Join(old, "sub", "g")} {
		if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ops := []Operation{
		{Change: *NewFileChange(filepath.Join(old, "f"), "edit", []byte("before"), []byte("middle"), false)},
		{Change: *NewFileChange(next, "move", []byte(old), nil, false)},
		{Change: *NewFileChange(filepath.Join(next, "f"), "edit", []byte("middle"), []byte("after"), false)},
		{Change: *NewFileChange(detached, "move", []byte(filepath.Join(next, "sub")), nil, false)},
		{Change: *NewFileChange(filepath.Join(detached, "g"), "edit", []byte("before"), []byte("after"), false)},
	}
	for i := 0; i < 2; i++ {
		if err := ApplyOperations(ops); err != nil {
			t.Fatalf("forward %d: %v", i, err)
		}
		for _, path := range []string{filepath.Join(next, "f"), filepath.Join(detached, "g")} {
			if got, err := os.ReadFile(path); err != nil || string(got) != "after" {
				t.Fatalf("forward child=%q %v", got, err)
			}
		}
		if err := ApplyOperations(ReverseOperations(ops)); err != nil {
			t.Fatalf("reverse %d: %v", i, err)
		}
		for _, path := range []string{filepath.Join(old, "f"), filepath.Join(old, "sub", "g")} {
			if got, err := os.ReadFile(path); err != nil || string(got) != "before" {
				t.Fatalf("restored child=%q %v", got, err)
			}
		}
	}
}

func TestTimelineDirectoryMoveRollbackRestoresChildren(t *testing.T) {
	root := timelineRoot(t)
	old, next, nonempty := filepath.Join(root, "old"), filepath.Join(root, "new"), filepath.Join(root, "nonempty")
	for _, dir := range []string{old, nonempty} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ops := []Operation{
		{Change: *NewFileChange(next, "move", []byte(old), nil, false)},
		{Change: *NewFileChange(filepath.Join(next, "file"), "edit", []byte("before"), []byte("after"), false)},
		{Change: FileChange{FilePath: nonempty, Tool: "mkdir"}, Undo: true},
	}
	err := ApplyOperations(ops)
	if err == nil || !strings.Contains(err.Error(), "restore failed (rolled back)") {
		t.Fatalf("expected runtime removal failure with complete rollback, got %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(old, "file")); err != nil || string(got) != "before" {
		t.Fatalf("rollback child=%q %v", got, err)
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatalf("rollback left moved directory: %v", err)
	}
}

func TestTimelineMappedDescendantRejectsSymlinkAncestry(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			root := timelineRoot(t)
			old, next, outside := filepath.Join(root, "old"), filepath.Join(root, "new"), timelineRoot(t)
			diskDir, child, content := old, filepath.Join(next, "sub", "f"), "before"
			if reverse {
				diskDir, child, content = next, filepath.Join(old, "sub", "f"), "after"
			}
			if err := os.Mkdir(diskDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "f"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(diskDir, "sub")); err != nil {
				t.Fatal(err)
			}
			move := Operation{Change: *NewFileChange(next, "move", []byte(old), nil, false)}
			edit := Operation{Change: *NewFileChange(child, "edit", []byte("before"), []byte("after"), false)}
			ops := []Operation{move, edit}
			if reverse {
				ops = ReverseOperations([]Operation{edit, move})
			}
			err := ApplyOperations(ops)
			if err == nil || !strings.Contains(err.Error(), "symbolic link") {
				t.Errorf("mapped symlink accepted: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(outside, "f")); err != nil || string(got) != content {
				t.Errorf("outside file overwritten: %q %v", got, err)
			}
			if _, err := os.Stat(diskDir); err != nil {
				t.Errorf("preflight already moved the directory: %v", err)
			}
		})
	}
}

func TestTimelineExecutionRechecksDescendantAncestry(t *testing.T) {
	root, outside := timelineRoot(t), timelineRoot(t)
	old, next := filepath.Join(root, "old"), filepath.Join(root, "new")
	if err := os.MkdirAll(filepath.Join(next, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(next, "sub", "f"), filepath.Join(outside, "f")} {
		if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ops := ReverseOperations([]Operation{
		{Change: *NewFileChange(filepath.Join(old, "sub", "f"), "edit", []byte("before"), []byte("after"), false)},
		{Change: *NewFileChange(next, "move", []byte(old), nil, false)},
	})
	m := NewManager()
	err := applyOperations(ops, func(op Operation) error {
		if err := m.applyOperation(op); err != nil {
			return err
		}
		if op.Change.Tool == "move" && op.Undo {
			if err := os.Rename(filepath.Join(old, "sub"), filepath.Join(root, "saved-sub")); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(old, "sub"))
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("late symlink accepted: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "f")); err != nil || string(got) != "after" {
		t.Errorf("outside file overwritten: %q %v", got, err)
	}
}
