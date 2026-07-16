package chat

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestSessionWriterLeaseRejectsSameProcessDuplicateAndReleases(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	first, err := acquireSessionWriterLeaseAt(dir, "session-one")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if got := first.SessionID(); got != "session-one" {
		t.Fatalf("SessionID() = %q, want session-one", got)
	}
	if !first.IsActive() {
		t.Fatal("newly acquired lease is not active")
	}

	if _, err := acquireSessionWriterLeaseAt(dir, "session-one"); !errors.Is(err, ErrSessionWriterLeaseBusy) {
		t.Fatalf("duplicate acquire error = %v, want ErrSessionWriterLeaseBusy", err)
	}

	other, err := acquireSessionWriterLeaseAt(dir, "session-two")
	if err != nil {
		t.Fatalf("different session acquire: %v", err)
	}
	if err := other.Release(); err != nil {
		t.Fatalf("release different session: %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if first.IsActive() {
		t.Fatal("released lease still reports active")
	}
	if err := first.Release(); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}

	reacquired, err := acquireSessionWriterLeaseAt(dir, "session-one")
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("release reacquired lease: %v", err)
	}
}

func TestSessionWriterLeaseConcurrentAcquireHasOneOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	const contenders = 24

	start := make(chan struct{})
	results := make(chan struct {
		lease *SessionWriterLease
		err   error
	}, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := acquireSessionWriterLeaseAt(dir, "contended-session")
			results <- struct {
				lease *SessionWriterLease
				err   error
			}{lease: lease, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var owner *SessionWriterLease
	busy := 0
	for result := range results {
		switch {
		case result.err == nil:
			if owner != nil {
				t.Fatal("more than one contender acquired the same session lease")
			}
			owner = result.lease
		case errors.Is(result.err, ErrSessionWriterLeaseBusy):
			busy++
		default:
			t.Fatalf("unexpected acquire error: %v", result.err)
		}
	}
	if owner == nil || busy != contenders-1 {
		t.Fatalf("owner = %v, busy = %d; want one owner and %d busy", owner != nil, busy, contenders-1)
	}
	if err := owner.Release(); err != nil {
		t.Fatalf("release winner: %v", err)
	}
}

func TestSessionWriterLeaseUsesDefaultPrivateStorage(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	lease, err := AcquireSessionWriterLease("default-storage")
	if err != nil {
		t.Fatalf("AcquireSessionWriterLease: %v", err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	}()

	sessionsDir := filepath.Join(dataHome, "gokin", "sessions")
	lockDir := filepath.Join(sessionsDir, sessionWriterLockDirName)
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		t.Fatalf("read lock directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "default-storage.lock" {
		t.Fatalf("lock entries = %v, want the exact session lock file", entries)
	}
	if runtime.GOOS != "windows" {
		assertPathMode(t, sessionsDir, 0o700)
		assertPathMode(t, lockDir, 0o700)
		assertPathMode(t, filepath.Join(lockDir, entries[0].Name()), 0o600)
	}
}

func TestSessionWriterLeaseHardensExistingStorageAndRejectsUnsafeLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	lockDir := filepath.Join(dir, sessionWriterLockDirName)
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := sessionWriterLockPathForTest(lockDir, "existing-lock")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireSessionWriterLeaseAt(dir, "existing-lock")
	if err != nil {
		t.Fatalf("acquire existing lock: %v", err)
	}
	if runtime.GOOS != "windows" {
		assertPathMode(t, dir, 0o700)
		assertPathMode(t, lockDir, 0o700)
		assertPathMode(t, path, 0o600)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release existing lock: %v", err)
	}

	unsafeID := "unsafe-lock"
	unsafePath := sessionWriterLockPathForTest(lockDir, unsafeID)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, unsafePath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := acquireSessionWriterLeaseAt(dir, unsafeID); err == nil {
		t.Fatal("acquire accepted a symlink lock file")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "untouched" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestSessionWriterLeaseRejectsSamePhysicalLockThroughAlias(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	first, err := acquireSessionWriterLeaseAt(dir, "alias-one")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() {
		if err := first.Release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()

	lockDir := filepath.Join(dir, sessionWriterLockDirName)
	firstPath := sessionWriterLockPathForTest(lockDir, "alias-one")
	secondPath := sessionWriterLockPathForTest(lockDir, "alias-two")
	if err := os.Link(firstPath, secondPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := acquireSessionWriterLeaseAt(dir, "alias-two"); !errors.Is(err, ErrSessionWriterLeaseBusy) {
		t.Fatalf("physical-alias acquire error = %v, want ErrSessionWriterLeaseBusy", err)
	}
}

func TestSessionWriterLeaseRejectsInvalidIDBeforeCreatingStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	for _, id := range []string{"", "../escape", "bad:name", "-option", "has space"} {
		if _, err := acquireSessionWriterLeaseAt(dir, id); err == nil {
			t.Errorf("acquire accepted invalid ID %q", id)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("invalid IDs created storage: stat error = %v", err)
	}
}

func TestSessionWriterLeaseReleasedWhenOwnerProcessDies(t *testing.T) {
	if os.Getenv("GOKIN_SESSION_WRITER_LEASE_HELPER") == "1" {
		t.Skip("parent-only test")
	}

	dir := filepath.Join(t.TempDir(), "sessions")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionWriterLeaseCrashHelper$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		"GOKIN_SESSION_WRITER_LEASE_HELPER=1",
		"GOKIN_SESSION_WRITER_LEASE_DIR="+dir,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	marker := make(chan error, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr == nil && line != "locked\n" {
			readErr = fmt.Errorf("unexpected helper output %q", line)
		}
		marker <- readErr
	}()
	select {
	case err := <-marker:
		if err != nil {
			t.Fatalf("helper did not acquire lease: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for helper to acquire lease")
	}

	if _, err := acquireSessionWriterLeaseAt(dir, "crash-session"); !errors.Is(err, ErrSessionWriterLeaseBusy) {
		t.Fatalf("cross-process duplicate error = %v, want ErrSessionWriterLeaseBusy", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed helper unexpectedly exited successfully")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		lease, err := acquireSessionWriterLeaseAt(dir, "crash-session")
		if err == nil {
			if releaseErr := lease.Release(); releaseErr != nil {
				t.Fatalf("release recovered lease: %v", releaseErr)
			}
			break
		}
		if !errors.Is(err, ErrSessionWriterLeaseBusy) || time.Now().After(deadline) {
			t.Fatalf("acquire after owner death: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionWriterLeaseCrashHelper(t *testing.T) {
	if os.Getenv("GOKIN_SESSION_WRITER_LEASE_HELPER") != "1" {
		return
	}
	lease, err := acquireSessionWriterLeaseAt(os.Getenv("GOKIN_SESSION_WRITER_LEASE_DIR"), "crash-session")
	if err != nil {
		t.Fatalf("helper acquire: %v", err)
	}
	helperSessionWriterLease = lease
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatalf("helper marker: %v", err)
	}
	select {}
}

var helperSessionWriterLease *SessionWriterLease

func sessionWriterLockPathForTest(lockDir, sessionID string) string {
	return filepath.Join(lockDir, sessionID+".lock")
}

func assertPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}
