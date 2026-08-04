package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInputHistoryRejectsOversizedFile(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	dir := filepath.Join(dataDir, "gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxInputHistoryFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	m := NewInputModel(DefaultStyles(), t.TempDir())
	if err := m.LoadHistory(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadHistory oversized error = %v", err)
	}
}

func TestInputHistorySaveFailurePreservesPreviousFile(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"preserved"})
	if err := m.SaveHistory(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "gokin", historyFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	m.history = []string{strings.Repeat("x", maxInputHistoryEntryBytes+1)}
	if err := m.SaveHistory(); err == nil || !strings.Contains(err.Error(), "entry exceeds") {
		t.Fatalf("SaveHistory oversized entry error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed SaveHistory replaced the previous durable history")
	}
}

func TestInputHistoryBoundsLiveRecallToPersistableSuffix(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.AddToHistory(strings.Repeat("x", maxInputHistoryEntryBytes+1))
	if got := m.GetHistory(); len(got) != 0 {
		t.Fatalf("oversized live entry entered recall history: %d entries", len(got))
	}

	entry := strings.Repeat("x", maxInputHistoryEntryBytes)
	history := make([]string, 17)
	for i := range history {
		history[i] = entry
	}
	m.SetHistory(history)
	got := m.GetHistory()
	if len(got) >= len(history) || len(got) == 0 {
		t.Fatalf("bounded live history retained %d of %d entries", len(got), len(history))
	}
	total := 0
	for _, item := range got {
		total += len("q:") + len(strconv.Quote(item)) + 1
	}
	if total > maxInputHistoryFileBytes {
		t.Fatalf("bounded live history encodes to %d bytes, limit %d", total, maxInputHistoryFileBytes)
	}
}

func TestInputHistoryRejectsExcessiveRecordCount(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	dir := filepath.Join(dataDir, "gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFile)
	data := []byte(strings.Repeat("legacy\n", maxInputHistoryRecords+1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewInputModel(DefaultStyles(), t.TempDir())
	if err := m.LoadHistory(); err == nil || !strings.Contains(err.Error(), "record limit") {
		t.Fatalf("LoadHistory excessive records error = %v", err)
	}
}

func TestCommandHistoryLoadPrunesToNewestEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFileName)
	entries := make([]HistoryEntry, maxHistoryEntries+20)
	base := time.Unix(1_700_000_000, 0)
	for i := range entries {
		entries[i] = HistoryEntry{
			Command:   fmt.Sprintf("cmd-%03d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Count:     i + 1,
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	history := &CommandHistory{entries: make(map[string]*HistoryEntry), filePath: path}
	if err := history.load(); err != nil {
		t.Fatal(err)
	}
	if got := len(history.entries); got != maxHistoryEntries {
		t.Fatalf("loaded entries = %d, want %d", got, maxHistoryEntries)
	}
	if history.GetUsageCount("cmd-000") != 0 || history.GetUsageCount("cmd-119") != 120 {
		t.Fatalf("load did not retain newest entries")
	}
	if got := history.GetRecentCommands(0); got != nil {
		t.Fatalf("GetRecentCommands(0) = %v, want nil", got)
	}
}

func TestCommandHistoryRejectsOversizedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxCommandHistoryFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	history := &CommandHistory{entries: make(map[string]*HistoryEntry), filePath: path}
	if err := history.load(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("command history oversized error = %v", err)
	}
}

func TestCommandHistoryFlushPersistsLatestConcurrentRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gokin")
	path := filepath.Join(dir, historyFileName)
	history := &CommandHistory{entries: make(map[string]*HistoryEntry), filePath: path}

	const writers = 20
	const recordsPerWriter = 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < recordsPerWriter; j++ {
				history.RecordUsage("cmd")
			}
		}()
	}
	wg.Wait()
	if err := history.Flush(); err != nil {
		t.Fatal(err)
	}

	reloaded := &CommandHistory{entries: make(map[string]*HistoryEntry), filePath: path}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if got, want := reloaded.GetUsageCount("cmd"), writers*recordsPerWriter; got != want {
		t.Fatalf("persisted usage count = %d, want %d", got, want)
	}
}
