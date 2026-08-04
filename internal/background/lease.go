package background

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gokin/internal/securefs"
)

var ErrWorkerLeaseBusy = errors.New("background worker lease is busy")

type WorkerLease struct {
	file *os.File
}

func (s *Store) AcquireWorkerLease(id string) (*WorkerLease, error) {
	path, err := s.lockPath(id)
	if err != nil {
		return nil, err
	}
	return acquireLeaseAt(path)
}

// AcquireWorkerLeaseWithin is the starting worker's acquisition. It retries
// briefly because WorkerLeaseHeld probes liveness by TAKING and releasing this
// same exclusive lock: a concurrent `gokin agents`/`stop` poll therefore holds
// it for a moment, and a single non-blocking attempt would make a legitimately
// starting worker fail to claim its own job.
func (s *Store) AcquireWorkerLeaseWithin(id string, timeout time.Duration) (*WorkerLease, error) {
	path, err := s.lockPath(id)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return acquireLeaseAt(path)
	}
	deadline := time.Now().Add(timeout)
	for {
		lease, lockErr := acquireLeaseAt(path)
		if lockErr == nil {
			return lease, nil
		}
		if !errors.Is(lockErr, ErrWorkerLeaseBusy) {
			return nil, lockErr
		}
		if time.Now().After(deadline) {
			return nil, ErrWorkerLeaseBusy
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func acquireLeaseAt(path string) (*WorkerLease, error) {
	file, err := securefs.OpenPrivateReadWrite(path)
	if err != nil {
		return nil, err
	}
	if err := lockWorkerFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &WorkerLease{file: file}, nil
}

func (s *Store) acquireMetadataLease(id string) (*WorkerLease, error) {
	path, err := s.metadataLockPath(id)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		lease, lockErr := acquireLeaseAt(path)
		if lockErr == nil {
			return lease, nil
		}
		if !errors.Is(lockErr, ErrWorkerLeaseBusy) {
			return nil, fmt.Errorf("acquire background metadata lock: %w", lockErr)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("acquire background metadata lock: %w", ErrWorkerLeaseBusy)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (l *WorkerLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unlockWorkerFile(file)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func (s *Store) WorkerLeaseHeld(id string) (bool, error) {
	lease, err := s.AcquireWorkerLease(id)
	if errors.Is(err, ErrWorkerLeaseBusy) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe background worker lease: %w", err)
	}
	return false, lease.Release()
}
