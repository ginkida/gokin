package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/chat"
	"google.golang.org/genai"
)

func TestSessionArchiveRejectsInvalidIdentityAndOversizedRecordBeforeCreatingStorage(t *testing.T) {
	for _, sessionID := range []string{"", "../escape", "nested/escape", "bad id"} {
		workDir := t.TempDir()
		err := appendSessionArchiveData(workDir, sessionID, []byte("{}\n"))
		if err == nil {
			t.Errorf("archive accepted invalid session ID %q", sessionID)
		}
		if _, err := os.Stat(filepath.Join(workDir, ".gokin")); !os.IsNotExist(err) {
			t.Errorf("invalid session ID %q created storage: %v", sessionID, err)
		}
	}

	workDir := t.TempDir()
	err := appendSessionArchiveData(workDir, "valid-session", make([]byte, maxSessionArchiveRecordBytes+1))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized archive error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".gokin")); !os.IsNotExist(err) {
		t.Fatalf("oversized record created storage: %v", err)
	}
}

func TestSessionArchiveRotatesFullSegmentWithoutDeletingHistory(t *testing.T) {
	workDir := t.TempDir()
	application := &App{workDir: workDir}
	first := sessionArchiveRecord{Timestamp: time.Now(), SessionID: "rotate-session", Reason: "first"}
	if err := application.appendSessionArchive(first); err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(workDir, ".gokin", "session_archives")
	current := filepath.Join(archiveDir, "rotate-session.jsonl")
	if err := os.Truncate(current, maxSessionArchiveSegmentBytes); err != nil {
		t.Fatal(err)
	}

	second := sessionArchiveRecord{Timestamp: time.Now(), SessionID: "rotate-session", Reason: "second"}
	if err := application.appendSessionArchive(second); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatal(err)
	}
	rotated := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "rotate-session.") && entry.Name() != "rotate-session.jsonl" {
			rotated++
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != maxSessionArchiveSegmentBytes {
				t.Fatalf("rotated segment size = %d", info.Size())
			}
		}
	}
	if rotated != 1 {
		t.Fatalf("rotated segment count = %d, want 1", rotated)
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	var stored sessionArchiveRecord
	if err := json.Unmarshal(bytes.TrimSpace(data), &stored); err != nil {
		t.Fatalf("new current segment is not valid JSONL: %v", err)
	}
	if stored.Reason != "second" {
		t.Fatalf("current record reason = %q", stored.Reason)
	}
}

func TestInspectSessionArchiveSegmentsEnforcesLimit(t *testing.T) {
	workDir := t.TempDir()
	dir, err := prepareSessionArchiveDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("session-1.%020d.jsonl", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inspectSessionArchiveSegmentsWithLimit(dir, "session-1", 2); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("segment limit error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("segment limit deleted archive data: %d files remain", len(entries))
	}
}

func TestSessionArchiveConcurrentAppendsProduceCompleteJSONLines(t *testing.T) {
	application := &App{workDir: t.TempDir()}
	const records = 40
	var wg sync.WaitGroup
	errorsCh := make(chan error, records)
	for i := 0; i < records; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errorsCh <- application.appendSessionArchive(sessionArchiveRecord{
				Timestamp: time.Now(),
				SessionID: "concurrent-session",
				Reason:    fmt.Sprintf("record-%d", index),
			})
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(application.workDir, ".gokin", "session_archives", "concurrent-session.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != records {
		t.Fatalf("archive line count = %d, want %d", len(lines), records)
	}
	for index, line := range lines {
		var record sessionArchiveRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("line %d is corrupt: %v", index, err)
		}
	}
}

func TestSessionGovernanceDoesNotDropConcurrentMessage(t *testing.T) {
	session := chat.NewSession()
	history := make([]*genai.Content, 90)
	for i := range history {
		history[i] = genai.NewContentFromText(fmt.Sprintf("message-%d", i), genai.RoleUser)
	}
	session.SetHistory(history)
	application := &App{workDir: t.TempDir(), session: session}

	paused := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	sessionArchiveBeforeAppendForTest = func() {
		once.Do(func() { close(paused) })
		<-release
	}
	defer func() { sessionArchiveBeforeAppendForTest = nil }()

	done := make(chan struct{})
	go func() {
		application.enforceSessionMemoryGovernance("test")
		close(done)
	}()
	<-paused
	session.AddUserMessage("concurrent-new-message")
	close(release)
	<-done

	retained := session.GetHistory()
	if len(retained) != 91 {
		t.Fatalf("concurrent session mutation was overwritten: %d messages remain", len(retained))
	}
	last := retained[len(retained)-1]
	if len(last.Parts) == 0 || last.Parts[0].Text != "concurrent-new-message" {
		t.Fatalf("concurrent message was lost: %#v", last)
	}
}

func TestSessionGovernanceDurablyArchivesBeforeTrimmingHistory(t *testing.T) {
	session := chat.NewSession()
	history := make([]*genai.Content, 90)
	for i := range history {
		history[i] = genai.NewContentFromText(fmt.Sprintf("message-%d", i), genai.RoleUser)
	}
	session.SetHistory(history)
	application := &App{workDir: t.TempDir(), session: session}

	application.enforceSessionMemoryGovernance("test-success")
	if got := session.MessageCount(); got != sessionGovernanceKeepTail {
		t.Fatalf("retained message count = %d, want %d", got, sessionGovernanceKeepTail)
	}
	application.sessionArchiveMu.Lock()
	operations := application.sessionArchiveOperations
	archived := application.sessionArchivedMessages
	application.sessionArchiveMu.Unlock()
	if operations != 1 || archived != 25 {
		t.Fatalf("archive metrics = (%d operations, %d messages)", operations, archived)
	}

	path := filepath.Join(application.workDir, ".gokin", "session_archives", session.GetID()+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record sessionArchiveRecord
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatal(err)
	}
	if record.ArchivedCount != 25 || len(record.Messages) != 25 {
		t.Fatalf("durable archive = %d count, %d messages", record.ArchivedCount, len(record.Messages))
	}
}
