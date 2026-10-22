package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/undo"
)

func TestTurnRewindAutomaticCaptureThroughHeadlessToolsAndResume(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "notes.txt")
	mock := testkit.NewMockClient()
	writer := tools.NewWriteTool(work)
	manager := undo.NewManager()
	writer.SetUndoManager(manager)
	a, _ := newHeadlessPolicyTestApp(t, mock, writer)
	a.workDir = work
	a.session.SetWorkDir(work)
	a.session.SetProvider("mock")
	a.undoManager = manager
	a.sessionManager, err = chat.NewSessionManager(a.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.ReleaseSessionWriterLease() })
	var out bytes.Buffer
	for i, prompt := range []string{"Write one.", "Write two."} {
		mock.EnqueueToolCall("write", map[string]any{"file_path": path, "content": []string{"one", "two"}[i]}).EnqueueText("The requested edit is complete.")
		out.Reset()
		if result, err := a.RunHeadlessWithOptions(t.Context(), prompt, HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &out, Stderr: io.Discard}); err != nil {
			t.Fatalf("headless turn %d: %+v %v; output=%s; calls=%d", i, result, err, out.String(), len(mock.Calls()))
		}
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil || len(points) != 2 {
		t.Fatalf("automatic capture missing: %+v %v", points, err)
	}
	savedID := a.session.GetID()
	a.sessionManager.Stop()
	if err := a.ReleaseSessionWriterLease(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), tools.NewWriteTool(work))
	restarted.workDir = work
	restarted.session.SetWorkDir(work)
	restarted.session.SetProvider("mock")
	restarted.undoManager = undo.NewManager()
	restarted.sessionManager, err = chat.NewSessionManager(restarted.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ResumeSession(savedID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.ReleaseSessionWriterLease() })
	if _, err := restarted.RewindToPoint(t.Context(), points[1].ID, "both"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "one" {
		t.Fatalf("resumed tool edits were not restored: %q %v", data, err)
	}
	if len(restarted.session.GetHistory()) >= len(a.session.GetHistory()) {
		t.Fatal("resumed conversation did not rewind")
	}
}
