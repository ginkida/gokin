package undo

import (
	"os"
	"path/filepath"
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
