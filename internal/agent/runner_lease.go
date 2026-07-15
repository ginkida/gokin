package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrAgentRunInProgress is returned when a second Runner path tries to execute
// the same logical agent concurrently. Callers may retry after the active run
// publishes its terminal result.
var ErrAgentRunInProgress = errors.New("agent run already in progress")

// agentRunLease is identity-bearing so a stale release can never delete a
// newer lease for the same ID. The sync.Once also makes panic/cancellation
// cleanup safe when more than one defensive defer reaches Release.
type agentRunLease struct {
	runner    *Runner
	agentID   string
	fileLease *agentRunFileLease
	once      sync.Once
}

func (r *Runner) acquireAgentRunLease(agentID string) (*agentRunLease, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("cannot acquire agent run lease: empty agent ID")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activeRuns == nil {
		r.activeRuns = make(map[string]*agentRunLease)
	}
	if _, exists := r.activeRuns[agentID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrAgentRunInProgress, agentID)
	}

	// The map above protects one Runner. A second Gokin process (or another
	// Runner backed by the same config directory) needs the same exclusion or it
	// can replay persisted state/checkpoints concurrently. Advisory locks are
	// tied to the open descriptor and are automatically released by the OS after
	// a crash, unlike create-once PID files which can strand recovery forever.
	var fileLease *agentRunFileLease
	if r.store != nil {
		var err error
		fileLease, err = r.store.acquireAgentRunFileLease(agentID)
		if err != nil {
			if errors.Is(err, errAgentRunFileLeaseBusy) {
				return nil, fmt.Errorf("%w: %s", ErrAgentRunInProgress, agentID)
			}
			return nil, fmt.Errorf("cannot acquire durable agent run lease for %s: %w", agentID, err)
		}
	}

	lease := &agentRunLease{runner: r, agentID: agentID, fileLease: fileLease}
	r.activeRuns[agentID] = lease
	return lease, nil
}

func (l *agentRunLease) Release() {
	if l == nil || l.runner == nil {
		return
	}
	l.once.Do(func() {
		l.runner.mu.Lock()
		// Always release this lease's own descriptor, even if defensive state
		// repair replaced/removed the map entry. Descriptor ownership is not the
		// same as map-entry ownership; skipping it would strand the OS lock.
		l.fileLease.Release()
		if l.runner.activeRuns[l.agentID] == l {
			// Release the OS lock before making the in-process slot visible. A
			// concurrent acquire cannot observe an empty map and transiently fail
			// against the old descriptor after terminal completion was published.
			delete(l.runner.activeRuns, l.agentID)
		}
		l.runner.mu.Unlock()
	})
}
