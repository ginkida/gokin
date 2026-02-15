package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/security"
)

type JournalEntry struct {
	Timestamp time.Time      `json:"ts"`
	Event     string         `json:"event"`
	Details   map[string]any `json:"details,omitempty"`
}

type RecoverySnapshot struct {
	Timestamp      time.Time `json:"ts"`
	SessionID      string    `json:"session_id"`
	Processing     bool      `json:"processing"`
	PendingMessage string    `json:"pending_message,omitempty"`
	HistoryLen     int       `json:"history_len"`
	PlanID         string    `json:"plan_id,omitempty"`
	CurrentStepID  int       `json:"current_step_id,omitempty"`
}

type ExecutionJournal struct {
	mu           sync.Mutex
	journalPath  string
	recoveryPath string
	redactor     *security.SecretRedactor
}

func NewExecutionJournal(workDir string) (*ExecutionJournal, error) {
	dir := filepath.Join(workDir, ".gokin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create journal dir: %w", err)
	}
	return &ExecutionJournal{
		journalPath:  filepath.Join(dir, "execution_journal.jsonl"),
		recoveryPath: filepath.Join(dir, "recovery_snapshot.json"),
		redactor:     security.NewSecretRedactor(),
	}, nil
}

func (j *ExecutionJournal) Append(event string, details map[string]any) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	// Redact secrets from details before writing to disk
	if j.redactor != nil && details != nil {
		details = j.redactor.RedactMap(details)
	}

	f, err := os.OpenFile(j.journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	entry := JournalEntry{
		Timestamp: time.Now(),
		Event:     event,
		Details:   details,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

func (j *ExecutionJournal) SaveRecovery(snapshot RecoverySnapshot) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	// Redact secrets from pending message before writing to disk
	if j.redactor != nil && snapshot.PendingMessage != "" {
		snapshot.PendingMessage = j.redactor.Redact(snapshot.PendingMessage)
	}

	snapshot.Timestamp = time.Now()
	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(j.recoveryPath, b, 0o600)
}

func (j *ExecutionJournal) LoadRecovery() (*RecoverySnapshot, error) {
	if j == nil {
		return nil, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := os.ReadFile(j.recoveryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snap RecoverySnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (j *ExecutionJournal) Tail(n int) ([]JournalEntry, error) {
	if j == nil {
		return nil, nil
	}
	if n <= 0 {
		n = 20
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	f, err := os.Open(j.journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	out := make([]JournalEntry, 0, len(lines))
	for _, ln := range lines {
		var e JournalEntry
		if err := json.Unmarshal([]byte(ln), &e); err == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
