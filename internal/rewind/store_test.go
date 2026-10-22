package rewind

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/undo"
)

func fixture(t *testing.T) (*Store, *chat.Session, *undo.Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession()
	session.SetWorkDir(work)
	store, err := Open(filepath.Join(root, "snapshots"), session.GetID(), work)
	if err != nil {
		t.Fatal(err)
	}
	manager := undo.NewManager()
	manager.SetObserver(func(ops []undo.Operation) {
		if err := store.Record(ops); err != nil {
			t.Error(err)
		}
	})
	return store, session, manager, work, filepath.Join(root, "snapshots")
}
func edit(t *testing.T, m *undo.Manager, path, value string) {
	t.Helper()
	old, err := os.ReadFile(path)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	change := undo.NewFileChange(path, "write", old, []byte(value), fresh)
	change.Mode = 0600
	if !m.Record(*change) {
		t.Fatal("record rejected")
	}
}
func begin(t *testing.T, s *Store, session *chat.Session, prompt string) string {
	t.Helper()
	id, err := s.Begin(prompt, session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	session.AddUserMessage(prompt)
	return id
}
func contents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v; want %q", path, data, err, want)
	}
}

func TestRestartAndBothRestore(t *testing.T) {
	s, session, m, work, root := fixture(t)
	path := filepath.Join(work, "file")
	begin(t, s, session, "first")
	edit(t, m, path, "one")
	selected := begin(t, s, session, "second")
	edit(t, m, path, "two")
	begin(t, s, session, "third")
	edit(t, m, path, "three")
	restarted, err := Open(root, session.GetID(), work)
	if err != nil {
		t.Fatal(err)
	}
	count, err := restarted.Restore(selected, true, true, func(snapshot *Snapshot) (func() error, error) {
		if snapshot.Prompt != "second" || len(snapshot.State.History) != 1 {
			t.Fatalf("wrong snapshot: %+v", snapshot)
		}
		previous := session.GetState()
		if err := session.RestoreFromState(snapshot.State); err != nil {
			return nil, err
		}
		return func() error { return session.RestoreFromState(previous) }, nil
	})
	if err != nil || count != 2 {
		t.Fatalf("restore=%d %v", count, err)
	}
	contents(t, path, "one")
	if len(session.GetHistory()) != 1 || len(restarted.Points()) != 2 {
		t.Fatal("conversation did not rewind before selected prompt")
	}
	again, err := Open(root, session.GetID(), work)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Points()) != 2 {
		t.Fatal("rewind not persisted")
	}
}
func TestCodeOnlyAndEarlierRestore(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	path := filepath.Join(work, "file")
	first := begin(t, s, session, "first")
	edit(t, m, path, "one")
	second := begin(t, s, session, "second")
	edit(t, m, path, "two")
	if _, err := s.Restore(second, true, false, nil); err != nil {
		t.Fatal(err)
	}
	contents(t, path, "one")
	if len(session.GetHistory()) != 2 || len(s.Points()) != 2 {
		t.Fatal("code-only changed conversation")
	}
	if _, err := s.Restore(first, true, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("first rewind failed to remove newly created file")
	}
}
func TestConversationOnlyKeepsFileHistory(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	path := filepath.Join(work, "file")
	first := begin(t, s, session, "first")
	edit(t, m, path, "one")
	begin(t, s, session, "second")
	edit(t, m, path, "two")
	if _, err := s.Restore(first, false, true, func(snapshot *Snapshot) (func() error, error) { return nil, session.RestoreFromState(snapshot.State) }); err != nil {
		t.Fatal(err)
	}
	contents(t, path, "two")
	if _, err := s.Restore(first, true, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("conversation-only lost later file snapshots")
	}
}
func TestUndoRedoAndBeyondRollingHistory(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	path := filepath.Join(work, "file")
	first := begin(t, s, session, "many edits")
	for i := 0; i < 130; i++ {
		edit(t, m, path, fmt.Sprint(i))
	}
	if m.Count() != undo.DefaultMaxChanges {
		t.Fatal("fixture did not exceed rolling history")
	}
	if _, err := m.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Redo(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UndoLastGroup(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(first, true, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("timeline depended on rolling history")
	}
}
func TestConflictPreflightsAllFiles(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	a, b := filepath.Join(work, "a"), filepath.Join(work, "b")
	first := begin(t, s, session, "edit two files")
	edit(t, m, a, "one")
	edit(t, m, b, "two")
	if err := os.WriteFile(a, []byte("manual"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := s.Restore(first, true, true, func(*Snapshot) (func() error, error) { called = true; return nil, nil }); err == nil {
		t.Fatal("conflicting file was overwritten")
	}
	if called {
		t.Fatal("conversation changed before code preflight")
	}
	contents(t, a, "manual")
	contents(t, b, "two")
}
func TestConversationPublicationFailureRollsBackCode(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	path := filepath.Join(work, "file")
	first := begin(t, s, session, "edit")
	edit(t, m, path, "current")
	want := errors.New("save failed")
	if _, err := s.Restore(first, true, true, func(*Snapshot) (func() error, error) { return nil, want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	contents(t, path, "current")
	if len(s.Points()[0].Events) != 1 {
		t.Fatal("failed restore changed journal")
	}
}
func TestChecksumAndSymlinkRefuseWithoutWrites(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		s, session, m, work, _ := fixture(t)
		path := filepath.Join(work, "file")
		first := begin(t, s, session, "edit")
		edit(t, m, path, "current")
		hash := s.Points()[0].Events[0]
		if err := os.WriteFile(filepath.Join(s.dir, hash+".json"), []byte(`[]`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Restore(first, true, false, nil); err == nil {
			t.Fatal("corrupt snapshot accepted")
		}
		contents(t, path, "current")
	})
	t.Run("symlink", func(t *testing.T) {
		s, session, m, work, _ := fixture(t)
		path := filepath.Join(work, "file")
		first := begin(t, s, session, "edit")
		edit(t, m, path, "current")
		target := filepath.Join(work, "external")
		if err := os.Rename(path, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skip(err)
		}
		if _, err := s.Restore(first, true, false, nil); err == nil {
			t.Fatal("symlink accepted")
		}
		contents(t, target, "current")
	})
}
func TestRetentionAndCollection(t *testing.T) {
	s, session, _, _, _ := fixture(t)
	for i := 0; i < MaxPoints+3; i++ {
		begin(t, s, session, fmt.Sprint(i))
	}
	points := s.Points()
	if len(points) != MaxPoints || points[0].Prompt != "3" {
		t.Fatal("incorrect turn retention")
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != MaxPoints+1 {
		t.Fatalf("unreferenced snapshots retained: %d", len(entries))
	}
}
func TestJournalFailureDoesNotOfferPartialCodeRestore(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	path := filepath.Join(work, "file")
	first := begin(t, s, session, "edit")
	m.SetObserver(nil)
	indexPath := filepath.Join(s.dir, "index.json")
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(indexPath, 0700); err != nil {
		t.Fatal(err)
	}
	change := undo.NewFileChange(path, "write", nil, []byte("current"), true)
	if err := os.WriteFile(path, change.NewContent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Record([]undo.Operation{{Change: *change}}); err == nil {
		t.Fatal("persistence failure not reported")
	}
	if _, err := s.Restore(first, true, false, nil); err == nil {
		t.Fatal("incomplete journal offered code restoration")
	}
	contents(t, path, "current")
	if !strings.Contains(s.Points()[0].Error, "failed") {
		t.Fatal("missing incomplete snapshot diagnostic")
	}
}

func TestForkCheckpointsHaveIndependentOwnership(t *testing.T) {
	s, session, m, work, root := fixture(t)
	path := filepath.Join(work, "file")
	first := begin(t, s, session, "edit")
	edit(t, m, path, "current")
	if _, err := s.Fork("new-branch"); err != nil {
		t.Fatal(err)
	}
	fork, err := Open(root, "new-branch", work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fork.Restore(first, true, true, func(snapshot *Snapshot) (func() error, error) {
		if snapshot.State.ID != "new-branch" {
			t.Fatal("fork checkpoint has source identity")
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(fork.Points()[0].Events) != 0 || len(s.Points()[0].Events) != 1 {
		t.Fatal("fork changed source journal")
	}
	if _, err := os.Stat(filepath.Join(s.dir, s.Points()[0].Events[0]+".json")); err != nil {
		t.Fatal("fork pruning removed source snapshots", err)
	}
}
func TestUntrackedChangeSurvivesConversationOnlyCollapse(t *testing.T) {
	s, session, m, work, _ := fixture(t)
	first := begin(t, s, session, "first")
	edit(t, m, filepath.Join(work, "file"), "one")
	begin(t, s, session, "second")
	m.MarkUntracked(filepath.Join(work, "large"), "snapshot limit")
	if _, err := s.Restore(first, false, true, func(*Snapshot) (func() error, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(first, true, false, nil); err == nil {
		t.Fatal("partial code checkpoint offered after conversation collapse")
	}
}
