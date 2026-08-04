package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

const (
	maxHistoryEntries          = 100
	maxHistoryLoadEntries      = 1000
	maxCommandHistoryFileBytes = 1 << 20
	maxCommandHistoryNameBytes = 4 << 10
	historyFileName            = "command_history.json"
)

// HistoryEntry represents a single command usage entry.
type HistoryEntry struct {
	Command   string    `json:"command"`
	Timestamp time.Time `json:"timestamp"`
	Count     int       `json:"count"`
}

// CommandHistory manages the history of used commands.
type CommandHistory struct {
	entries  map[string]*HistoryEntry
	filePath string
	mu       sync.RWMutex
	saveMu   sync.Mutex
	revision uint64
	savedRev uint64
	saved    bool
	// saveScheduled is guarded by mu and keeps bursts of palette activity from
	// spawning one filesystem-writing goroutine per command invocation.
	saveScheduled bool
}

// NewCommandHistory creates a new CommandHistory.
func NewCommandHistory() *CommandHistory {
	ch := &CommandHistory{
		entries: make(map[string]*HistoryEntry),
	}

	// Determine file path
	configDir, err := getConfigDir()
	if err == nil {
		ch.filePath = filepath.Join(configDir, historyFileName)
		if err := ch.load(); err != nil {
			logging.Debug("failed to load command history", "error", err)
		}
	}

	return ch
}

// getConfigDir returns the config directory path.
func getConfigDir() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "gokin"), nil
}

// RecordUsage records that a command was used.
func (ch *CommandHistory) RecordUsage(command string) {
	if command == "" || len(command) > maxCommandHistoryNameBytes {
		return
	}
	ch.mu.Lock()

	entry, exists := ch.entries[command]
	if exists {
		entry.Timestamp = time.Now()
		if entry.Count < math.MaxInt {
			entry.Count++
		}
	} else {
		ch.entries[command] = &HistoryEntry{
			Command:   command,
			Timestamp: time.Now(),
			Count:     1,
		}
	}

	// Prune if too many entries
	if len(ch.entries) > maxHistoryEntries {
		ch.pruneOldest()
	}
	ch.revision++
	launchSave := !ch.saveScheduled
	if launchSave {
		ch.saveScheduled = true
	}
	ch.mu.Unlock()

	if launchSave {
		go ch.runSaveLoop()
	}
}

func (ch *CommandHistory) runSaveLoop() {
	for {
		ch.mu.RLock()
		targetRevision := ch.revision
		ch.mu.RUnlock()

		if err := ch.save(); err != nil {
			ch.mu.Lock()
			ch.saveScheduled = false
			ch.mu.Unlock()
			logging.Debug("failed to save command history", "error", err)
			return
		}

		ch.mu.Lock()
		if ch.revision == targetRevision {
			ch.saveScheduled = false
			ch.mu.Unlock()
			return
		}
		ch.mu.Unlock()
	}
}

// GetRecentCommands returns the most recently used commands.
func (ch *CommandHistory) GetRecentCommands(limit int) []string {
	if limit <= 0 {
		return nil
	}
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	// Convert to slice for sorting
	entries := make([]*HistoryEntry, 0, len(ch.entries))
	for _, e := range ch.entries {
		entries = append(entries, e)
	}

	// Sort by timestamp (most recent first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	// Extract command names
	result := make([]string, 0, limit)
	for i, e := range entries {
		if i >= limit {
			break
		}
		result = append(result, e.Command)
	}

	return result
}

// GetUsageCount returns how many times a command has been used.
func (ch *CommandHistory) GetUsageCount(command string) int {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if entry, exists := ch.entries[command]; exists {
		return entry.Count
	}
	return 0
}

// IsRecent checks if a command is in the recent history.
func (ch *CommandHistory) IsRecent(command string, limit int) bool {
	recent := ch.GetRecentCommands(limit)
	for _, c := range recent {
		if c == command {
			return true
		}
	}
	return false
}

// GetTimestamp returns the last usage timestamp for a command.
// Returns zero time if the command is not found.
func (ch *CommandHistory) GetTimestamp(command string) time.Time {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if entry, exists := ch.entries[command]; exists {
		return entry.Timestamp
	}
	return time.Time{}
}

// pruneOldest removes the oldest entries to stay under the limit.
func (ch *CommandHistory) pruneOldest() {
	// Convert to slice for sorting
	entries := make([]*HistoryEntry, 0, len(ch.entries))
	for _, e := range ch.entries {
		entries = append(entries, e)
	}

	// Sort by timestamp (oldest first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	// Remove oldest entries
	toRemove := len(entries) - maxHistoryEntries
	for i := 0; i < toRemove && i < len(entries); i++ {
		delete(ch.entries, entries[i].Command)
	}
}

// load loads the history from disk.
func (ch *CommandHistory) load() error {
	if ch.filePath == "" {
		return nil
	}

	data, err := readPrivateHistoryFile(ch.filePath, maxCommandHistoryFileBytes)
	if err != nil {
		// errors.Is, not os.IsNotExist: the history reader wraps its errors, and
		// os.IsNotExist does not unwrap — a fresh install reported "read failed"
		// for a file that simply does not exist yet.
		if errors.Is(err, os.ErrNotExist) {
			ch.saved = true
			return nil
		}
		return err
	}

	var entries []HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	if len(entries) > maxHistoryLoadEntries {
		return fmt.Errorf("command history exceeds %d-entry limit", maxHistoryLoadEntries)
	}

	for i := range entries {
		e := entries[i]
		if e.Command == "" || len(e.Command) > maxCommandHistoryNameBytes {
			continue
		}
		if e.Count <= 0 {
			e.Count = 1
		}
		entry := e
		ch.entries[e.Command] = &entry
	}
	if len(ch.entries) > maxHistoryEntries {
		ch.pruneOldest()
	}
	ch.saved = true

	return nil
}

// Flush synchronously persists the latest command-history revision. Any queued
// asynchronous worker subsequently observes that revision and performs no
// stale overwrite.
func (ch *CommandHistory) Flush() error {
	return ch.save()
}

// save saves the history to disk.
func (ch *CommandHistory) save() error {
	ch.saveMu.Lock()
	defer ch.saveMu.Unlock()

	ch.mu.RLock()
	filePath := ch.filePath
	if filePath == "" {
		ch.mu.RUnlock()
		return nil
	}
	revision := ch.revision
	if ch.saved && ch.savedRev == revision {
		ch.mu.RUnlock()
		return nil
	}

	// Snapshot entry VALUES under lock — copying the pointers is not enough:
	// json.Marshal reads every pointee's fields OUTSIDE the lock, racing a
	// concurrent RecordUsage mutating the same *HistoryEntry (Count++,
	// Timestamp) under the write lock. -race caught this via the async
	// `go ch.save()` overlapping the next RecordUsage (the round-5
	// "snapshot the pointers, race on the pointees" class).
	entries := make([]HistoryEntry, 0, len(ch.entries))
	for _, e := range ch.entries {
		entries = append(entries, *e)
	}
	ch.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Timestamp.Equal(entries[j].Timestamp) {
			return entries[i].Command < entries[j].Command
		}
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	// Marshal and write outside lock — disk I/O no longer blocks readers/writers
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	if err := writePrivateHistoryFile(filePath, data, maxCommandHistoryFileBytes); err != nil {
		return err
	}
	ch.savedRev = revision
	ch.saved = true
	return nil
}
