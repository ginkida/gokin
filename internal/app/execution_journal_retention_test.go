package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExecutionJournalCompactsToNewestCompleteLines(t *testing.T) {
	journal, err := NewExecutionJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldEntry, err := json.Marshal(JournalEntry{
		Timestamp: time.Unix(1, 0),
		Event:     "old_event",
		Details:   map[string]any{"payload": strings.Repeat("x", 32<<10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldLine := append(oldEntry, '\n')
	repetitions := int(maxJournalFileBytes/int64(len(oldLine))) + 2
	oversized := bytes.Repeat(oldLine, repetitions)
	if err := os.WriteFile(journal.journalPath, oversized, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := journal.Append("sentinel", map[string]any{"sequence": 1}); err != nil {
		t.Fatalf("Append after retention threshold: %v", err)
	}
	info, err := os.Stat(journal.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > journalRetentionBytes+maxJournalEntryBytes {
		t.Fatalf("compacted journal size = %d, want <= %d",
			info.Size(), journalRetentionBytes+maxJournalEntryBytes)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("compacted journal mode = %o, want 600", info.Mode().Perm())
	}

	f, err := os.Open(journal.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxJournalEntryBytes)
	lineCount := 0
	lastEvent := ""
	for scanner.Scan() {
		lineCount++
		var entry JournalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("retention left a partial JSONL line %d: %v", lineCount, err)
		}
		lastEvent = entry.Event
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lineCount == 0 || lastEvent != "sentinel" {
		t.Fatalf("retained lines = %d, last event = %q", lineCount, lastEvent)
	}
}

func TestExecutionJournalTailReturnsOnlyNewestEntriesInOrder(t *testing.T) {
	journal, err := NewExecutionJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		if err := journal.Append(fmt.Sprintf("event_%03d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := journal.Tail(3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"event_097", "event_098", "event_099"}
	if len(entries) != len(want) {
		t.Fatalf("Tail(3) returned %d entries", len(entries))
	}
	for i := range want {
		if entries[i].Event != want[i] {
			t.Fatalf("Tail(3)[%d] = %q, want %q", i, entries[i].Event, want[i])
		}
	}
}
