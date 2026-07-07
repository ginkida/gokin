package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProjectMemory_WatcherRebindsAfterInstructionFileCreated (round 5) pins
// the fix: when StartWatching begins with NO instruction file present, it
// falls back to watching the project DIRECTORY. A directory's mtime only
// changes on child add/remove/rename, never on a content-only edit to an
// already-existing child — so once GOKIN.md is created and picked up, every
// SUBSEQUENT edit to its content used to be permanently invisible to the
// watcher (silently killing live-reload) because FileWatcher.path stayed
// bound to the directory forever. The fix rebinds the watcher to the
// concrete file the first time a change is observed while still in
// directory-fallback mode.
func TestProjectMemory_WatcherRebindsAfterInstructionFileCreated(t *testing.T) {
	workDir := resolvedTempDirForContext(t)

	pm := NewProjectMemory(workDir)
	if err := pm.Load(); err != nil {
		t.Fatalf("initial Load: %v", err)
	}

	reloaded := make(chan struct{}, 8)
	pm.OnReload(func() {
		select {
		case reloaded <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.StartWatching(ctx, 50); err != nil {
		t.Fatalf("StartWatching: %v", err)
	}
	defer pm.StopWatching()

	pm.mu.Lock()
	initialWatchPath := pm.watcher.Path()
	pm.mu.Unlock()
	if initialWatchPath != workDir {
		t.Fatalf("expected the watcher to fall back to the directory (no instruction file yet); watching %q, want %q", initialWatchPath, workDir)
	}

	// Give the watcher's initial mtime baseline a moment to settle before
	// mutating (mirrors the sibling FileWatcher tests) — some filesystems
	// have coarse mtime granularity, and checkChanges requires the new mtime
	// to differ from the baseline by > 100ms.
	time.Sleep(150 * time.Millisecond)

	// Create the instruction file — a directory-level change (add), which
	// the directory-mtime-based watch DOES detect.
	gokinPath := filepath.Join(workDir, "GOKIN.md")
	if err := os.WriteFile(gokinPath, []byte("# Rules\nOriginal content.\n"), 0644); err != nil {
		t.Fatalf("WriteFile(GOKIN.md): %v", err)
	}

	waitForReload(t, reloaded, "after creating GOKIN.md")

	// The rebind happens a few lines AFTER the onReload() call inside the
	// SAME callback invocation that just signaled `reloaded` — poll rather
	// than asserting immediately, or this races the callback goroutine's
	// own remaining work (a test-synchronization gap, not a production bug).
	deadline := time.Now().Add(2 * time.Second)
	var reboundPath string
	for time.Now().Before(deadline) {
		pm.mu.Lock()
		reboundPath = pm.watcher.Path()
		pm.mu.Unlock()
		if reboundPath == gokinPath {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if reboundPath != gokinPath {
		t.Fatalf("expected the watcher to rebind to %q after discovering the file, still watching %q", gokinPath, reboundPath)
	}

	// Now edit the file's CONTENT ONLY (no rename/create/delete) — this is
	// exactly the case a directory-mtime watch can NEVER see. Force the
	// mtime forward explicitly (some filesystems have coarse mtime
	// granularity) so the change is unambiguous.
	newContent := "# Rules\nEDITED content — this must trigger a reload.\n"
	if err := os.WriteFile(gokinPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("WriteFile(GOKIN.md) edit: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(gokinPath, future, future)

	waitForReload(t, reloaded, "after editing GOKIN.md's content")

	got := pm.GetInstructions()
	if !strings.Contains(got, "EDITED content") {
		t.Fatalf("GetInstructions() = %q, want it to reflect the content edit (proves the watcher actually re-read the file, not just fired stale)", got)
	}
}

func waitForReload(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for a reload %s", what)
	}
}

// resolvedTempDirForContext mirrors testkit.ResolvedTempDir without importing
// testkit (which would create an import cycle: testkit doesn't import
// context, but keeping this package's test deps minimal and self-contained
// avoids any risk of one forming later). macOS's /var/folders/... temp dirs
// are a symlink to /private/var/folders/..., which trips path-containment
// logic that resolves symlinks (FileWatcher itself doesn't validate paths,
// but keeping this consistent with the rest of the codebase's test hygiene).
func resolvedTempDirForContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}
