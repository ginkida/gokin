package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// ErrSessionWriterLeaseBusy is returned when another goroutine or process
// already owns the writer lease for the requested persisted session.
var ErrSessionWriterLeaseBusy = errors.New("session writer lease already held")

const sessionWriterLockDirName = ".writer-locks"

var processSessionWriterLeases = struct {
	sync.Mutex
	held map[string]*SessionWriterLease
}{held: make(map[string]*SessionWriterLease)}

// SessionWriterLease grants exclusive write ownership of one persisted
// session. The underlying advisory OS lock is descriptor-owned, so a process
// crash releases it automatically without PID files or stale-lock recovery.
//
// Release must be called when the owning operation finishes. It is safe to
// call Release more than once.
type SessionWriterLease struct {
	file       *os.File
	registryID string
	sessionID  string
	active     atomic.Bool
	once       sync.Once
	releaseErr error
}

// SessionID returns the persisted session identity protected by this lease.
// It lets higher-level runtimes verify that an acquired lease still matches
// the in-memory Session before accepting ownership of it.
func (l *SessionWriterLease) SessionID() string {
	if l == nil {
		return ""
	}
	return l.sessionID
}

// IsActive reports whether Release has not begun for this lease.
func (l *SessionWriterLease) IsActive() bool {
	return l != nil && l.active.Load()
}

// AcquireSessionWriterLease acquires exclusive write ownership for sessionID
// in Gokin's standard full-session storage directory. Session IDs use the
// exact same portable validation contract as session persistence.
func AcquireSessionWriterLease(sessionID string) (*SessionWriterLease, error) {
	sessionsDir, err := getSessionsDir()
	if err != nil {
		return nil, fmt.Errorf("resolve session writer lease directory: %w", err)
	}
	return acquireSessionWriterLeaseAt(sessionsDir, sessionID)
}

// acquireSessionWriterLeaseAt is the storage-injected implementation used by
// focused tests. Production callers should use AcquireSessionWriterLease so
// leases and persisted sessions cannot accidentally use different roots.
func acquireSessionWriterLeaseAt(sessionsDir, sessionID string) (*SessionWriterLease, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, fmt.Errorf("acquire session writer lease: %w", err)
	}

	absSessionsDir, err := filepath.Abs(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session writer lease path: %w", err)
	}
	absSessionsDir = filepath.Clean(absSessionsDir)
	if err := ensurePrivateDir(absSessionsDir); err != nil {
		return nil, fmt.Errorf("prepare session storage for writer lease: %w", err)
	}

	lockDir := filepath.Join(absSessionsDir, sessionWriterLockDirName)
	if err := ensurePrivateDir(lockDir); err != nil {
		return nil, fmt.Errorf("prepare session writer lock directory: %w", err)
	}

	// Use the validated session basename directly so the lock file follows the
	// filesystem's own identity rules exactly like <sessionID>.json does. This
	// matters on case-folding or Unicode-normalizing filesystems: hashing the
	// raw string could create two locks for names that address one state file.
	lockPath := filepath.Join(lockDir, sessionID+".lock")
	registryID := filepath.Clean(lockPath)

	// Hold the registry mutex until the OS lock is acquired. Besides rejecting
	// same-process duplicates explicitly, this prevents two goroutines from
	// racing through separate opens before either publishes its lease.
	processSessionWriterLeases.Lock()
	defer processSessionWriterLeases.Unlock()
	if _, exists := processSessionWriterLeases.held[registryID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrSessionWriterLeaseBusy, sessionID)
	}

	file, err := openSessionWriterLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open writer lease for session %s: %w", sessionID, err)
	}
	for _, held := range processSessionWriterLeases.held {
		same, sameErr := sameSessionWriterLockFile(file, held.file)
		if sameErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("compare writer lease identity for session %s: %w", sessionID, sameErr)
		}
		if same {
			_ = file.Close()
			return nil, fmt.Errorf("%w: %s", ErrSessionWriterLeaseBusy, sessionID)
		}
	}
	if err := lockSessionWriterFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrSessionWriterLeaseBusy) {
			return nil, fmt.Errorf("%w: %s", ErrSessionWriterLeaseBusy, sessionID)
		}
		return nil, fmt.Errorf("lock writer lease for session %s: %w", sessionID, err)
	}

	lease := &SessionWriterLease{file: file, registryID: registryID, sessionID: sessionID}
	lease.active.Store(true)
	processSessionWriterLeases.held[registryID] = lease
	return lease, nil
}

func sameSessionWriterLockFile(left, right *os.File) (bool, error) {
	if left == nil || right == nil {
		return false, nil
	}
	leftInfo, err := left.Stat()
	if err != nil {
		return false, err
	}
	rightInfo, err := right.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

// Release relinquishes the writer lease. Repeated calls return the same result
// and never unlock a newer lease that may later be acquired for the session.
func (l *SessionWriterLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.active.Store(false)
		processSessionWriterLeases.Lock()
		defer processSessionWriterLeases.Unlock()

		var errs []error
		if l.file != nil {
			if err := unlockSessionWriterFile(l.file); err != nil {
				errs = append(errs, fmt.Errorf("unlock session writer lease: %w", err))
			}
			if err := l.file.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close session writer lease: %w", err))
			}
		}
		if processSessionWriterLeases.held[l.registryID] == l {
			delete(processSessionWriterLeases.held, l.registryID)
		}
		l.releaseErr = errors.Join(errs...)
	})
	return l.releaseErr
}

// openSessionWriterLockFile refuses symlinks and non-regular filesystem
// objects. Lock files are intentionally retained: unlinking an advisory-lock
// file can let another process lock a new inode while the original is active.
func openSessionWriterLockFile(path string) (*os.File, error) {
	for attempts := 0; attempts < 3; attempts++ {
		before, err := os.Lstat(path)
		if os.IsNotExist(err) {
			file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if os.IsExist(createErr) {
				continue
			}
			if createErr != nil {
				return nil, createErr
			}
			if err := verifySessionWriterLockFile(path, file, nil); err != nil {
				_ = file.Close()
				return nil, err
			}
			return file, nil
		}
		if err != nil {
			return nil, err
		}
		if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("session writer lock path %q is not a regular file", path)
		}

		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := verifySessionWriterLockFile(path, file, before); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	return nil, fmt.Errorf("session writer lock path changed while opening")
}

func verifySessionWriterLockFile(path string, file *os.File, before os.FileInfo) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || (before != nil && !os.SameFile(before, opened)) {
		return fmt.Errorf("session writer lock path %q changed while opening", path)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return fmt.Errorf("session writer lock path %q changed while opening", path)
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set session writer lock permissions: %w", err)
	}
	return nil
}
