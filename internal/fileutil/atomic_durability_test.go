package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestAtomicWriteDurabilityOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	ops := systemAtomicWriteOps()

	var order []string
	systemChmod := ops.chmod
	systemSyncFile := ops.syncFile
	systemReplace := ops.replace
	systemSyncParent := ops.syncParent
	ops.chmod = func(file *os.File, mode os.FileMode) error {
		order = append(order, "chmod")
		return systemChmod(file, mode)
	}
	ops.syncFile = func(file *os.File) error {
		order = append(order, "sync-file")
		return systemSyncFile(file)
	}
	ops.replace = func(oldPath, newPath string) error {
		order = append(order, "replace")
		return systemReplace(oldPath, newPath)
	}
	ops.syncParent = func(path string) error {
		order = append(order, "sync-parent")
		return systemSyncParent(path)
	}

	if err := atomicWrite(path, []byte("durable"), 0o600, ops); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	if got, want := strings.Join(order, ","), "chmod,sync-file,replace,sync-parent"; got != want {
		t.Fatalf("operation order = %q, want %q", got, want)
	}
}

func TestAtomicWriteFailureCleanup(t *testing.T) {
	errInjected := errors.New("injected failure")
	tests := []struct {
		name       string
		inject     func(*atomicWriteOps)
		wantTarget string
	}{
		{
			name: "chmod",
			inject: func(ops *atomicWriteOps) {
				ops.chmod = func(*os.File, os.FileMode) error { return errInjected }
			},
			wantTarget: "old",
		},
		{
			name: "file sync",
			inject: func(ops *atomicWriteOps) {
				ops.syncFile = func(*os.File) error { return errInjected }
			},
			wantTarget: "old",
		},
		{
			name: "replace",
			inject: func(ops *atomicWriteOps) {
				ops.replace = func(string, string) error { return errInjected }
			},
			wantTarget: "old",
		},
		{
			name: "parent sync",
			inject: func(ops *atomicWriteOps) {
				ops.syncParent = func(string) error { return errInjected }
			},
			wantTarget: "new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			ops := systemAtomicWriteOps()
			tt.inject(&ops)

			err := atomicWrite(path, []byte("new"), 0o600, ops)
			if !errors.Is(err, errInjected) {
				t.Fatalf("atomicWrite error = %v, want injected failure", err)
			}
			assertNoAtomicTemps(t, dir)

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile target: %v", readErr)
			}
			if got := string(data); got != tt.wantTarget {
				t.Fatalf("committed target = %q, want %q", got, tt.wantTarget)
			}
		})
	}
}

func TestAtomicWriteConcurrentWritersPublishWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared-state.json")

	const writers = 32
	want := make(map[string]struct{}, writers)
	payloads := make([]string, writers)
	for i := range writers {
		payloads[i] = fmt.Sprintf("writer-%02d:%s", i, strings.Repeat(string(rune('a'+i%26)), 8*1024))
		want[payloads[i]] = struct{}{}
	}

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for _, payload := range payloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- AtomicWrite(path, []byte(payload), 0o600)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent AtomicWrite: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile final target: %v", err)
	}
	if _, ok := want[string(data)]; !ok {
		t.Fatal("final target contains a partial or mixed concurrent write")
	}
	assertNoAtomicTemps(t, dir)
}

func TestAtomicWriteAppliesRequestedModeOnReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not implement POSIX permission bits")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("secret"), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("replacement mode = %#o, want 0600", got)
	}
}

func assertNoAtomicTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".gokin-*.tmp"))
	if err != nil {
		t.Fatalf("Glob atomic temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("atomic temp files left behind: %v", matches)
	}
}
