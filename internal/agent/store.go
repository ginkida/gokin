package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gokin/internal/fileutil"
)

// AgentStore provides persistent storage for agent states.
type AgentStore struct {
	dir string
	mu  sync.RWMutex

	// Explicit per-store write ceiling keeps every constructor on the same
	// read/write contract. persistedWriteLimit clamps it to the hard read limit,
	// so no store can persist a file that recovery refuses by size.
	maxPersistedWriteBytes int64
}

// Persisted state is durable but untrusted input after a crash or manual
// modification. Bound reads so a huge/sparse JSON file cannot make recovery
// allocate until the process is OOM-killed.
const maxPersistedRecoveryFileBytes int64 = 128 << 20

// NewAgentStore creates a new agent store.
// configDir should be the base config directory (e.g., ~/.config/gokin).
func NewAgentStore(configDir string) (*AgentStore, error) {
	dir := filepath.Join(configDir, "agents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create agents directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to secure agents directory: %w", err)
	}

	return &AgentStore{
		dir:                    dir,
		maxPersistedWriteBytes: maxPersistedRecoveryFileBytes,
	}, nil
}

func (s *AgentStore) persistedWriteLimit() int64 {
	if s.maxPersistedWriteBytes <= 0 || s.maxPersistedWriteBytes > maxPersistedRecoveryFileBytes {
		return maxPersistedRecoveryFileBytes
	}
	return s.maxPersistedWriteBytes
}

func (s *AgentStore) validatePersistedWriteSize(kind string, data []byte) error {
	limit := s.persistedWriteLimit()
	if int64(len(data)) > limit {
		return fmt.Errorf("%s exceeds %d-byte persisted recovery limit", kind, limit)
	}
	return nil
}

// validateStoreIdentifier keeps every persisted identifier a single portable
// filename component. Resume IDs can come from CLI/tool input, while checkpoint
// JSON is durable untrusted input after a crash; neither may escape s.dir or use
// Windows alternate-data-stream syntax.
func validateStoreIdentifier(kind, id string) error {
	if id == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if len(id) > 240 {
		return fmt.Errorf("invalid %s: identifier is too long", kind)
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("invalid %s %q: only letters, digits, '.', '-', and '_' are allowed", kind, id)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("invalid %s %q", kind, id)
	}
	return nil
}

// readStableRegularFile rejects symlinks and other non-regular objects before
// reading persisted recovery data. Comparing the opened handle with Lstat both
// before and after the read also detects a path replaced by another store call
// (or external actor) during the operation instead of accepting data from a
// different inode after a check/use race.
func readStableRegularFile(path string) ([]byte, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("persisted path is not a regular file: %s", path)
	}
	if pathInfo.Size() > maxPersistedRecoveryFileBytes {
		return nil, nil, fmt.Errorf("persisted file exceeds %d-byte limit: %s", maxPersistedRecoveryFileBytes, path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	openedBefore, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !openedBefore.Mode().IsRegular() || !os.SameFile(pathInfo, openedBefore) {
		return nil, nil, fmt.Errorf("persisted path changed before read: %s", path)
	}
	if openedBefore.Size() > maxPersistedRecoveryFileBytes {
		return nil, nil, fmt.Errorf("persisted file exceeds %d-byte limit: %s", maxPersistedRecoveryFileBytes, path)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxPersistedRecoveryFileBytes+1))
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	closed = true
	if readErr != nil {
		return nil, nil, readErr
	}
	if int64(len(data)) > maxPersistedRecoveryFileBytes {
		return nil, nil, fmt.Errorf("persisted file exceeds %d-byte limit: %s", maxPersistedRecoveryFileBytes, path)
	}
	if statErr != nil {
		return nil, nil, statErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if !os.SameFile(openedBefore, openedAfter) ||
		openedBefore.Size() != openedAfter.Size() ||
		!openedBefore.ModTime().Equal(openedAfter.ModTime()) {
		return nil, nil, fmt.Errorf("persisted file changed while being read: %s", path)
	}

	pathAfter, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(openedAfter, pathAfter) {
		return nil, nil, fmt.Errorf("persisted path changed while being read: %s", path)
	}
	return data, pathAfter, nil
}

// Save saves an agent's state to disk.
func (s *AgentStore) Save(agent *Agent) error {
	if agent == nil {
		return fmt.Errorf("cannot save nil agent")
	}
	// Snapshot the agent before taking the store-wide I/O lock. Different
	// agents should not be prevented from snapshotting their state by a slow
	// write, and this avoids a store->agent lock-order edge.
	state := agent.GetState()
	return s.SaveState(state)
}

// SaveState saves an agent state directly.
func (s *AgentStore) SaveState(state *AgentState) error {
	if state == nil {
		return fmt.Errorf("cannot save nil agent state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveState(state)
}

// saveState is the internal save implementation.
func (s *AgentStore) saveState(state *AgentState) error {
	if state == nil {
		return fmt.Errorf("cannot save nil agent state")
	}
	if err := validateStoreIdentifier("agent ID", state.ID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal agent state: %w", err)
	}
	if err := s.validatePersistedWriteSize("agent state", data); err != nil {
		return err
	}

	filePath := filepath.Join(s.dir, state.ID+".json")
	// Atomic (temp+fsync+rename) — this is the SOLE persisted copy of the
	// agent's state, overwritten in place across its lifetime. A raw
	// os.WriteFile left it truncated/corrupt if the process died mid-write
	// (OOM/SIGKILL/panic), permanently losing the only thing crash-recovery
	// exists to protect. Matches the idiom 3 sibling files in this same
	// package already use (delegation_metrics.go, prompt_optimizer.go,
	// strategy_optimizer.go) and internal/loops/storage.go.
	if err := fileutil.AtomicWrite(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write agent state: %w", err)
	}

	return nil
}

// Load loads an agent state from disk.
func (s *AgentStore) Load(agentID string) (*AgentState, error) {
	if err := validateStoreIdentifier("agent ID", agentID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, _, err := s.loadStateLocked(agentID)
	return state, err
}

// loadStateLocked loads one state and returns the metadata for the exact file
// that was read. The caller must hold s.mu for reading or writing.
func (s *AgentStore) loadStateLocked(agentID string) (*AgentState, os.FileInfo, error) {
	filePath := filepath.Join(s.dir, agentID+".json")
	data, fileInfo, err := readStableRegularFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("agent not found: %s", agentID)
		}
		return nil, nil, fmt.Errorf("failed to read agent state: %w", err)
	}

	var state AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal agent state: %w", err)
	}
	if err := validateStoreIdentifier("persisted agent ID", state.ID); err != nil {
		return nil, nil, err
	}
	if state.ID != agentID {
		return nil, nil, fmt.Errorf("agent state identity mismatch: requested %q, file contains %q", agentID, state.ID)
	}

	return &state, fileInfo, nil
}

// Delete removes an agent state from disk.
func (s *AgentStore) Delete(agentID string) error {
	if err := validateStoreIdentifier("agent ID", agentID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, agentID+".json")
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete agent state: %w", err)
	}

	return nil
}

type storedAgentState struct {
	state    *AgentState
	fileInfo os.FileInfo
}

// scanStatesLocked validates all state-shaped files before returning a
// deterministic snapshot. The caller must hold s.mu. Invalid JSON, mismatched
// identities, symlinks, and other non-regular entries fail closed rather than
// being presented (or later deleted) as ordinary agent state.
func (s *AgentStore) scanStatesLocked() ([]storedAgentState, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory: %w", err)
	}

	states := make([]storedAgentState, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		agentID := entry.Name()[:len(entry.Name())-len(".json")]
		if err := validateStoreIdentifier("agent state filename", agentID); err != nil {
			return nil, err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("agent state %s is not a regular file", agentID)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect agent state %s: %w", agentID, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("agent state %s is not a regular file", agentID)
		}

		state, fileInfo, err := s.loadStateLocked(agentID)
		if err != nil {
			return nil, fmt.Errorf("load agent state %s during scan: %w", agentID, err)
		}
		states = append(states, storedAgentState{state: state, fileInfo: fileInfo})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].state.ID < states[j].state.ID })
	return states, nil
}

// List returns all stored agent IDs in deterministic order.
func (s *AgentStore) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	states, err := s.scanStatesLocked()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(states))
	for _, stored := range states {
		ids = append(ids, stored.state.ID)
	}
	return ids, nil
}

// Cleanup removes agent states older than the specified duration.
func (s *AgentStore) Cleanup(maxAge time.Duration) (int, error) {
	if maxAge < 0 {
		return 0, fmt.Errorf("agent state max age must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	states, err := s.scanStatesLocked()
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-maxAge)
	cleaned := 0
	for _, stored := range states {
		if !stored.fileInfo.ModTime().Before(cutoff) {
			continue
		}
		filePath := filepath.Join(s.dir, stored.state.ID+".json")
		if err := os.Remove(filePath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cleaned, fmt.Errorf("remove expired agent state %s: %w", stored.state.ID, err)
		}
		cleaned++
	}

	return cleaned, nil
}

// Exists checks if an agent state exists.
func (s *AgentStore) Exists(agentID string) bool {
	if validateStoreIdentifier("agent ID", agentID) != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, _, err := s.loadStateLocked(agentID)
	return err == nil
}

// SaveCheckpoint saves an agent checkpoint to disk.
func (s *AgentStore) SaveCheckpoint(cp *AgentCheckpoint) error {
	if cp == nil {
		return fmt.Errorf("cannot save nil checkpoint")
	}
	if err := validateStoreIdentifier("checkpoint ID", cp.CheckpointID); err != nil {
		return err
	}
	if cp.AgentState == nil {
		return fmt.Errorf("checkpoint %q has no agent state", cp.CheckpointID)
	}
	if err := validateStoreIdentifier("checkpoint agent ID", cp.AgentState.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create checkpoints subdirectory
	checkpointsDir := filepath.Join(s.dir, "checkpoints")
	if err := os.MkdirAll(checkpointsDir, 0700); err != nil {
		return fmt.Errorf("failed to create checkpoints directory: %w", err)
	}
	if err := os.Chmod(checkpointsDir, 0700); err != nil {
		return fmt.Errorf("failed to secure checkpoints directory: %w", err)
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}
	if err := s.validatePersistedWriteSize("checkpoint", data); err != nil {
		return err
	}

	filePath := filepath.Join(checkpointsDir, cp.CheckpointID+".json")
	// Atomic — same rationale as saveState. Each checkpoint has a unique
	// filename (agentID-unixnano), but a torn write still corrupts THAT
	// checkpoint. Recovery now fails closed on any unreadable checkpoint so it
	// cannot replay an older state after silently skipping the newest one.
	if err := fileutil.AtomicWrite(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write checkpoint: %w", err)
	}

	return nil
}

// LoadCheckpoint loads a checkpoint from disk.
func (s *AgentStore) LoadCheckpoint(checkpointID string) (*AgentCheckpoint, error) {
	if err := validateStoreIdentifier("checkpoint ID", checkpointID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadCheckpointLocked(checkpointID)
}

// loadCheckpointLocked loads one checkpoint while the caller holds s.mu for
// reading or writing. Keeping scans and their loads under one lock prevents a
// store mutation between ownership/timestamp inspection and selection.
func (s *AgentStore) loadCheckpointLocked(checkpointID string) (*AgentCheckpoint, error) {
	filePath := filepath.Join(s.dir, "checkpoints", checkpointID+".json")
	data, _, err := readStableRegularFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checkpoint not found: %s", checkpointID)
		}
		return nil, fmt.Errorf("failed to read checkpoint: %w", err)
	}

	var cp AgentCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal checkpoint: %w", err)
	}
	if err := validateStoreIdentifier("persisted checkpoint ID", cp.CheckpointID); err != nil {
		return nil, err
	}
	if cp.CheckpointID != checkpointID {
		return nil, fmt.Errorf("checkpoint identity mismatch: requested %q, file contains %q", checkpointID, cp.CheckpointID)
	}
	if cp.AgentState == nil {
		return nil, fmt.Errorf("checkpoint %q has no agent state", checkpointID)
	}
	if err := validateStoreIdentifier("checkpoint agent ID", cp.AgentState.ID); err != nil {
		return nil, err
	}

	return &cp, nil
}

type storedCheckpoint struct {
	checkpoint *AgentCheckpoint
}

// scanCheckpointsLocked returns a deterministic oldest-to-newest snapshot of
// every persisted checkpoint. Timestamp is authoritative; CheckpointID is the
// stable tie-breaker when two snapshots have the same timestamp.
//
// The caller must hold s.mu. Any checkpoint-shaped path that cannot be safely
// read fails the entire scan: silently skipping it could make recovery or
// retention treat an older checkpoint as the newest durable state.
func (s *AgentStore) scanCheckpointsLocked() ([]storedCheckpoint, error) {
	checkpointsDir := filepath.Join(s.dir, "checkpoints")
	entries, err := os.ReadDir(checkpointsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read checkpoints directory: %w", err)
	}

	checkpoints := make([]storedCheckpoint, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		checkpointID := entry.Name()[:len(entry.Name())-len(".json")]
		if err := validateStoreIdentifier("checkpoint filename", checkpointID); err != nil {
			return nil, err
		}

		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("checkpoint %s is not a regular file", checkpointID)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect checkpoint %s: %w", checkpointID, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("checkpoint %s is not a regular file", checkpointID)
		}

		checkpoint, err := s.loadCheckpointLocked(checkpointID)
		if err != nil {
			return nil, fmt.Errorf("load checkpoint %s during scan: %w", checkpointID, err)
		}
		checkpoints = append(checkpoints, storedCheckpoint{checkpoint: checkpoint})
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		left := checkpoints[i].checkpoint
		right := checkpoints[j].checkpoint
		if left.Timestamp.Equal(right.Timestamp) {
			return left.CheckpointID < right.CheckpointID
		}
		return left.Timestamp.Before(right.Timestamp)
	})
	return checkpoints, nil
}

// ListCheckpoints returns all checkpoint IDs for an agent.
func (s *AgentStore) ListCheckpoints(agentID string) ([]string, error) {
	if agentID != "" {
		if err := validateStoreIdentifier("agent ID", agentID); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	checkpoints, err := s.scanCheckpointsLocked()
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(checkpoints))
	for _, stored := range checkpoints {
		checkpoint := stored.checkpoint
		if agentID == "" || checkpoint.AgentState.ID == agentID {
			ids = append(ids, checkpoint.CheckpointID)
		}
	}

	return ids, nil
}

// GetLatestCheckpoint returns the most recent checkpoint for an agent.
func (s *AgentStore) GetLatestCheckpoint(agentID string) (*AgentCheckpoint, error) {
	if agentID != "" {
		if err := validateStoreIdentifier("agent ID", agentID); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	checkpoints, err := s.scanCheckpointsLocked()
	if err != nil {
		return nil, err
	}
	for i := len(checkpoints) - 1; i >= 0; i-- {
		checkpoint := checkpoints[i].checkpoint
		if agentID == "" || checkpoint.AgentState.ID == agentID {
			return checkpoint, nil
		}
	}
	return nil, fmt.Errorf("no checkpoints found for agent: %s", agentID)
}

// CleanupCheckpoints removes old checkpoints, keeping only the most recent N.
func (s *AgentStore) CleanupCheckpoints(agentID string, keepCount int) (int, error) {
	if err := validateStoreIdentifier("agent ID", agentID); err != nil {
		return 0, err
	}
	if keepCount < 0 {
		return 0, fmt.Errorf("checkpoint keep count must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	checkpoints, err := s.scanCheckpointsLocked()
	if err != nil {
		return 0, err
	}

	agentCheckpoints := make([]storedCheckpoint, 0, len(checkpoints))
	for _, stored := range checkpoints {
		if stored.checkpoint.AgentState.ID == agentID {
			agentCheckpoints = append(agentCheckpoints, stored)
		}
	}

	if len(agentCheckpoints) <= keepCount {
		return 0, nil
	}

	// scanCheckpointsLocked sorted by persisted Timestamp, then CheckpointID.
	toRemove := agentCheckpoints[:len(agentCheckpoints)-keepCount]
	removed := 0
	for _, stored := range toRemove {
		checkpointID := stored.checkpoint.CheckpointID
		filePath := filepath.Join(s.dir, "checkpoints", checkpointID+".json")
		if err := os.Remove(filePath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("remove checkpoint %s during retention: %w", checkpointID, err)
		}
		removed++
	}

	return removed, nil
}

// DeleteCheckpoint removes a single checkpoint file by its ID.
func (s *AgentStore) DeleteCheckpoint(checkpointID string) error {
	if err := validateStoreIdentifier("checkpoint ID", checkpointID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, "checkpoints", checkpointID+".json")
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}
	return nil
}

// ConsumeCheckpoint atomically marks a checkpoint as claimed by removing its
// durable file. The boolean is false when another recovery path already
// consumed it. Unlike DeleteCheckpoint's intentionally idempotent cleanup
// contract, recovery callers must observe this distinction or two Runner
// instances sharing a store can both execute the same checkpoint.
func (s *AgentStore) ConsumeCheckpoint(checkpointID string) (bool, error) {
	if err := validateStoreIdentifier("checkpoint ID", checkpointID); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, "checkpoints", checkpointID+".json")
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to consume checkpoint: %w", err)
	}
	return true, nil
}

// ListErrorCheckpoints returns all checkpoints with TriggerReason "error".
func (s *AgentStore) ListErrorCheckpoints() ([]*AgentCheckpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	checkpoints, err := s.scanCheckpointsLocked()
	if err != nil {
		return nil, err
	}
	result := make([]*AgentCheckpoint, 0, len(checkpoints))
	for _, stored := range checkpoints {
		if stored.checkpoint.TriggerReason == "error" {
			result = append(result, stored.checkpoint)
		}
	}
	return result, nil
}

// CleanupOldCheckpointFiles removes all checkpoint files older than maxAge.
func (s *AgentStore) CleanupOldCheckpointFiles(maxAge time.Duration) (int, error) {
	if maxAge < 0 {
		return 0, fmt.Errorf("checkpoint max age must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	checkpoints, err := s.scanCheckpointsLocked()
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-maxAge)
	cleaned := 0
	for _, stored := range checkpoints {
		checkpoint := stored.checkpoint
		if !checkpoint.Timestamp.Before(cutoff) {
			continue
		}
		path := filepath.Join(s.dir, "checkpoints", checkpoint.CheckpointID+".json")
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cleaned, fmt.Errorf("remove expired checkpoint %s: %w", checkpoint.CheckpointID, err)
		}
		cleaned++
	}
	return cleaned, nil
}
