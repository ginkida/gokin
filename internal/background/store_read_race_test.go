package background

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/fileutil"
)

func TestPrivateStateReadBoundsRetriesAndClosesFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.json")
	if err := fileutil.AtomicWriteString(path, `{}`, 0600); err != nil {
		t.Fatal(err)
	}
	var opened []*os.File
	_, err := readPrivateRegularWithOpen(path, 1024, func(path string) (*os.File, error) {
		if err := fileutil.AtomicWriteString(path, `{}`, 0600); err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if file != nil {
			opened = append(opened, file)
		}
		return file, err
	})
	if !errors.Is(err, errStateFileReplaced) || len(opened) != stateReadAttempts {
		t.Fatalf("unbounded or unexpected retry: opens=%d err=%v", len(opened), err)
	}
	for _, file := range opened {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("retry leaked a descriptor: %v", err)
		}
	}
}

func TestStoreReadLocksAfterRepeatedReplacement(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{ID: id, State: StateStarting, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.jobPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath, _ := store.metadataLockPath(id)
	opens := 0
	job, err := store.loadWithReader(id, func(path string, limit int64) ([]byte, error) {
		return readPrivateRegularWithOpen(path, limit, func(path string) (*os.File, error) {
			opens++
			if opens <= stateReadAttempts {
				if err := fileutil.AtomicWrite(path, data, 0600); err != nil {
					return nil, err
				}
			} else {
				lease, err := acquireLeaseAt(lockPath)
				if lease != nil {
					_ = lease.Release()
				}
				if !errors.Is(err, ErrWorkerLeaseBusy) {
					return nil, fmt.Errorf("fallback did not hold the writer lock: %v", err)
				}
			}
			return os.Open(path)
		})
	})
	if err != nil || job.ID != id || opens != stateReadAttempts+1 {
		t.Fatalf("writer churn escaped the locked fallback: job=%+v opens=%d err=%v", job, opens, err)
	}
}

func TestPrivateStateReadPreservesRealErrors(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "oversized", "symlink", "open-error"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.json")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(root, "target.json")
				if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "oversized", "open-error":
				data := `{}`
				if kind == "oversized" {
					data = strings.Repeat("x", 1025)
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			opens := 0
			data, err := readPrivateRegularWithOpen(path, 1024, func(string) (*os.File, error) {
				opens++
				return nil, os.ErrPermission
			})
			if err == nil || data != nil || errors.Is(err, errStateFileReplaced) {
				t.Fatalf("invalid state accepted or misclassified: data=%s err=%v", data, err)
			}
			wantOpens := 0
			if kind == "open-error" {
				wantOpens = 1
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("lost open error: %v", err)
				}
			}
			if opens != wantOpens {
				t.Fatalf("non-transient error retried: opens=%d want=%d", opens, wantOpens)
			}
		})
	}
}

func TestPrivateStateReadRevalidatesReplacement(t *testing.T) {
	for _, kind := range []string{"symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.json")
			target := filepath.Join(root, "target.json")
			if err := fileutil.AtomicWriteString(path, `{}`, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("external contents"), 0600); err != nil {
				t.Fatal(err)
			}
			opens := 0
			data, err := readPrivateRegularWithOpen(path, 1024, func(path string) (*os.File, error) {
				opens++
				if err := fileutil.AtomicWriteString(path, `{}`, 0600); err != nil {
					return nil, err
				}
				file, err := os.Open(path)
				if err != nil {
					return nil, err
				}
				if kind == "symlink" {
					if err := os.Remove(path); err != nil {
						_ = file.Close()
						return nil, err
					}
					if err := os.Symlink(target, path); err != nil {
						_ = file.Close()
						t.Skipf("symlinks unavailable: %v", err)
					}
				} else if err := fileutil.AtomicWriteString(path, strings.Repeat("x", 1025), 0600); err != nil {
					_ = file.Close()
					return nil, err
				}
				return file, nil
			})
			if err == nil || data != nil || errors.Is(err, errStateFileReplaced) || opens != 1 {
				t.Fatalf("unsafe replacement not rejected on retry: opens=%d data=%s err=%v", opens, data, err)
			}
		})
	}
}

func TestPrivateStateReadRetriesAtomicReplacement(t *testing.T) {
	for _, record := range []string{"job", "control"} {
		t.Run(record, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), record+".json")
			if err := fileutil.AtomicWriteString(path, `{"generation":1}`, 0600); err != nil {
				t.Fatal(err)
			}
			opens := 0
			data, err := readPrivateRegularWithOpen(path, 1024, func(path string) (*os.File, error) {
				opens++
				if opens == 1 {
					if err := fileutil.AtomicWriteString(path, `{"generation":2}`, 0600); err != nil {
						return nil, err
					}
				}
				return os.Open(path)
			})
			if err != nil || string(data) != `{"generation":2}` || opens != 2 {
				t.Fatalf("atomic replacement was surfaced instead of retried: data=%s opens=%d err=%v", data, opens, err)
			}
		})
	}
}

func TestStoreReadersDuringAtomicReplacement(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{ID: id, State: StateStarting, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireWorkerLease(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := store.MarkRunning(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	control, err := store.EnqueueControl(id, "verify the concurrent job")
	if err != nil {
		t.Fatal(err)
	}
	controlPath, _ := store.controlPath(id, control.ID)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var writes atomic.Int64
	var reads atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 7)
	start := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for ctx.Err() == nil {
			if _, err := store.Update(id, func(job *Job) error {
				job.SessionID = fmt.Sprintf("session-%d", writes.Load())
				// Control commits use the same metadata lease and atomic replace.
				return store.writeControl(control)
			}); err != nil {
				errs <- fmt.Errorf("writer: %w", err)
				return
			}
			writes.Add(1)
		}
	}()
	for reader := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for ctx.Err() == nil {
				var readErr error
				switch reader {
				case 0, 1:
					var job Job
					job, readErr = store.Load(id)
					if readErr == nil {
						_, readErr = store.Reconcile(job)
					}
				case 2:
					_, readErr = store.Resolve(id[:8])
				case 3:
					var jobs []Job
					jobs, readErr = store.List("", false)
					if readErr == nil && (len(jobs) != 1 || jobs[0].ID != id) {
						readErr = fmt.Errorf("list dropped the active job: %+v", jobs)
					}
				case 4:
					_, readErr = store.RefreshControlCounts(Job{ID: id})
				case 5:
					_, readErr = store.loadControl(controlPath, id, control.ID)
				}
				if readErr != nil {
					errs <- fmt.Errorf("reader %d: %w", reader, readErr)
					return
				}
				reads.Add(1)
				// Keep six polling readers from monopolizing the full-suite host;
				// the writer still replaces job and control records continuously.
				time.Sleep(250 * time.Microsecond)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if writes.Load() == 0 || reads.Load() == 0 {
		t.Fatalf("stress test made no progress: writes=%d reads=%d", writes.Load(), reads.Load())
	}
	t.Logf("atomic replacements=%d successful reads=%d", writes.Load(), reads.Load())
	if _, err := store.Finish(id, StateSucceeded, 0); err != nil {
		t.Fatal(err)
	}
	job, err := store.Load(id)
	if err != nil || !job.Terminal() || !strings.HasPrefix(job.SessionID, "session-") {
		t.Fatalf("terminal state lost: %+v err=%v", job, err)
	}
}

func TestStoreResolveRetriesMissingDirectoryEntry(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{ID: id, State: StateStarting, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	job, err := store.resolveWithReadDir(id[:8], func(path string) ([]os.DirEntry, error) {
		reads++
		if reads == 1 {
			return nil, nil // An enumeration during replacement missed the entry.
		}
		return os.ReadDir(path)
	})
	if err != nil || job.ID != id || reads != 2 {
		t.Fatalf("transient prefix miss escaped: job=%+v scans=%d err=%v", job, reads, err)
	}
}

func TestStoreResolveMissingPrefixBoundsRetries(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	_, err = store.resolveWithReadDir("abcdef12", func(string) ([]os.DirEntry, error) {
		reads++
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "was not found") || reads != stateReadAttempts {
		t.Fatalf("missing job did not stay bounded: scans=%d err=%v", reads, err)
	}
	reads = 0
	_, err = store.resolveWithReadDir("abcdef12", func(string) ([]os.DirEntry, error) {
		reads++
		return nil, os.ErrPermission
	})
	if !errors.Is(err, os.ErrPermission) || reads != 1 {
		t.Fatalf("real enumeration error was retried: scans=%d err=%v", reads, err)
	}
}

func TestStoreListRetriesMissingDirectoryEntry(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{ID: id, State: StateStarting, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	jobs, err := store.listWithReadDir("", false, func(path string) ([]os.DirEntry, error) {
		reads++
		if reads == 1 {
			return nil, nil
		}
		return os.ReadDir(path)
	})
	if err != nil || len(jobs) != 1 || jobs[0].ID != id || reads != 2 {
		t.Fatalf("transient listing miss escaped: jobs=%+v scans=%d err=%v", jobs, reads, err)
	}
}
