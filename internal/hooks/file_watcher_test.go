package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func awaitFileChange(t *testing.T, changes <-chan FileChange, path, kind string) {
	t.Helper()
	select {
	case change := <-changes:
		if change.Path != path || change.Event != kind {
			t.Fatalf("wrong file event: %+v, want %s %s", change, path, kind)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("missing %s for %s", kind, path)
	}
}

func noFileChange(t *testing.T, changes <-chan FileChange) {
	t.Helper()
	select {
	case change := <-changes:
		t.Fatalf("unrequested file event: %+v", change)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestFileWatcherExternalWritesAtomicSavesAndRetargeting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proof.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	changes := make(chan FileChange, 16)
	w := NewFileWatcher(t.Context(), func(_ context.Context, change FileChange) { changes <- change })
	defer w.Close()
	if err := w.SetPaths([]string{path}); err != nil {
		t.Fatal(err)
	}
	noFileChange(t, changes) // Starting a watcher does not create a file event.
	if err := os.WriteFile(path, []byte("external update"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, path, "change")
	temporary := filepath.Join(root, "atomic.tmp")
	if err := os.WriteFile(temporary, []byte("atomic replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, path, "change")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, path, "unlink")
	if err := os.WriteFile(path, []byte("created again"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, path, "add")
	newPath := filepath.Join(root, "not-yet/created/proof.txt")
	if err := w.SetPaths([]string{newPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new target"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, newPath, "add")
	if err := os.WriteFile(path, []byte("no longer watched"), 0600); err != nil {
		t.Fatal(err)
	}
	noFileChange(t, changes)
	if err := w.SetPaths([]string{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("removed dynamic list"), 0600); err != nil {
		t.Fatal(err)
	}
	noFileChange(t, changes)
}

func TestFileWatcherTracksSymlinkTargetsAndRecreatedParents(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	changes := make(chan FileChange, 16)
	w := NewFileWatcher(t.Context(), func(_ context.Context, change FileChange) { changes <- change })
	defer w.Close()
	if err := w.SetPaths([]string{link}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("changed through real target"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, link, "change")
	path := filepath.Join(root, "parent/proof.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPaths([]string{path}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(path), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, path, "unlink")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new parent"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitFileChange(t, changes, path, "add")
}

func TestFileWatcherCloseCancelsCallbackAndJoinsParentCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof")
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	w := NewFileWatcher(ctx, func(ctx context.Context, _ FileChange) { close(started); <-ctx.Done() })
	if err := w.SetPaths([]string{path}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("watch callback did not start")
	}
	cancel()
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
		t.Fatal("parent cancellation left watcher processes alive")
	}
	w.Close()
}
