package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var errAgentRunFileLeaseBusy = errors.New("durable agent run lease busy")

// agentRunFileLease owns an advisory lock descriptor. The lock file itself is
// intentionally retained: unlinking an advisory-lock file permits a racing
// process to create and lock a new inode while the old inode is still locked.
type agentRunFileLease struct {
	file *os.File
	once sync.Once
}

func (s *AgentStore) acquireAgentRunFileLease(agentID string) (*agentRunFileLease, error) {
	if s == nil {
		return nil, nil
	}
	lockDir := filepath.Join(s.dir, "run-locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("create agent run lock directory: %w", err)
	}

	digest := sha256.Sum256([]byte(agentID))
	path := filepath.Join(lockDir, hex.EncodeToString(digest[:])+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open agent run lock: %w", err)
	}
	if err := lockAgentRunFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &agentRunFileLease{file: file}, nil
}

func (l *agentRunFileLease) Release() {
	if l == nil || l.file == nil {
		return
	}
	l.once.Do(func() {
		_ = unlockAgentRunFile(l.file)
		_ = l.file.Close()
	})
}
