package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/security"
)

const (
	maxRecoverySnapshotBytes int64 = 8 << 20
	maxJournalEntryBytes           = 1 << 20
	// Bound long-lived project journals. /journal only renders the latest 30
	// events, while an unbounded JSONL had reached 5 MiB/14k lines in normal use
	// and Tail retained every line in memory. Compact atomically before an append
	// would cross 8 MiB, keeping the newest complete ~4 MiB of diagnostics.
	maxJournalFileBytes   int64 = 8 << 20
	journalRetentionBytes int64 = 4 << 20
	evalRuntimeDirEnv           = "GOKIN_EVAL_RUNTIME_DIR"
)

type JournalEntry struct {
	Timestamp time.Time      `json:"ts"`
	Event     string         `json:"event"`
	Details   map[string]any `json:"details,omitempty"`
}

type RecoverySnapshot struct {
	Timestamp  time.Time `json:"ts"`
	SessionID  string    `json:"session_id"`
	Processing bool      `json:"processing"`
	// PendingMessage holds the head of the type-ahead queue (legacy field,
	// kept so older tooling reading pending_message still works).
	PendingMessage string `json:"pending_message,omitempty"`
	// PendingMessages is the full type-ahead queue, oldest first.
	PendingMessages []string `json:"pending_messages,omitempty"`
	HistoryLen      int      `json:"history_len"`
	PlanID          string   `json:"plan_id,omitempty"`
	CurrentStepID   int      `json:"current_step_id,omitempty"`
}

type ExecutionJournal struct {
	mu           sync.Mutex
	journalPath  string
	recoveryPath string
	redactor     *security.SecretRedactor
}

func NewExecutionJournal(workDir string) (*ExecutionJournal, error) {
	return newExecutionJournalInDir(filepath.Join(workDir, ".gokin"))
}

// NewExecutionJournalInDir creates journal storage in a caller-owned absolute
// directory. The eval runner uses a sibling of the model-writable workspace so
// runtime evidence cannot be replaced through ordinary file or bash tools.
// Interactive sessions continue to use NewExecutionJournal and .gokin.
func NewExecutionJournalInDir(dir string) (*ExecutionJournal, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("execution journal directory must be absolute")
	}
	return newExecutionJournalInDir(dir)
}

func newExecutionJournalForWorkDir(workDir string) (*ExecutionJournal, error) {
	if dir := strings.TrimSpace(os.Getenv(evalRuntimeDirEnv)); dir != "" {
		return NewExecutionJournalInDir(dir)
	}
	return NewExecutionJournal(workDir)
}

func newExecutionJournalInDir(dir string) (*ExecutionJournal, error) {
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("secure journal dir: %w", err)
	}
	journal := &ExecutionJournal{
		journalPath:  filepath.Join(dir, "execution_journal.jsonl"),
		recoveryPath: filepath.Join(dir, "recovery_snapshot.json"),
		redactor:     security.NewSecretRedactor(),
	}
	for _, path := range []string{journal.journalPath, journal.recoveryPath} {
		if err := fileutil.SecurePrivateFile(path); err != nil {
			return nil, fmt.Errorf("secure journal file %q: %w", path, err)
		}
	}
	return journal, nil
}

func (j *ExecutionJournal) Append(event string, details map[string]any) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	// Redact secrets from details before writing to disk
	if j.redactor != nil && details != nil {
		details = j.redactor.RedactMap(details)
	}

	entry := JournalEntry{
		Timestamp: time.Now(),
		Event:     event,
		Details:   details,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal execution journal event %q: %w", event, err)
	}
	if len(b)+1 > maxJournalEntryBytes {
		return fmt.Errorf("execution journal event %q exceeds %d-byte limit", event, maxJournalEntryBytes)
	}
	if err := j.compactBeforeAppendLocked(int64(len(b) + 1)); err != nil {
		return fmt.Errorf("compact execution journal before event %q: %w", event, err)
	}

	f, err := fileutil.OpenPrivateAppend(j.journalPath)
	if err != nil {
		return fmt.Errorf("open execution journal %q: %w", j.journalPath, err)
	}
	defer f.Close()

	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write execution journal %q event %q: %w",
			j.journalPath, event, err)
	}
	return nil
}

// compactBeforeAppendLocked atomically retains the newest complete JSONL lines
// when the next append would cross the file cap. j.mu must be held. Reading only
// the retention window keeps recovery bounded even if an externally corrupted
// journal is much larger than the normal cap.
func (j *ExecutionJournal) compactBeforeAppendLocked(incomingBytes int64) error {
	info, err := os.Lstat(j.journalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		// Preserve the secure open helper's detailed symlink/special-file error.
		f, openErr := fileutil.OpenPrivateRead(j.journalPath)
		if f != nil {
			_ = f.Close()
		}
		return openErr
	}
	if info.Size()+incomingBytes <= maxJournalFileBytes {
		return nil
	}

	f, err := fileutil.OpenPrivateRead(j.journalPath)
	if err != nil {
		return err
	}
	defer f.Close()
	start := max(info.Size()-journalRetentionBytes, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	retained, err := io.ReadAll(io.LimitReader(f, journalRetentionBytes+1))
	if err != nil {
		return err
	}
	if start > 0 {
		newline := bytes.IndexByte(retained, '\n')
		if newline < 0 {
			retained = nil
		} else {
			retained = retained[newline+1:]
		}
	}
	if len(retained) > 0 && retained[len(retained)-1] != '\n' {
		retained = append(retained, '\n')
	}
	if err := fileutil.AtomicWrite(j.journalPath, retained, 0o600); err != nil {
		return err
	}
	return nil
}

func (j *ExecutionJournal) SaveRecovery(snapshot RecoverySnapshot) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	// Redact secrets from pending messages before writing to disk — BOTH the
	// legacy head field and the full queue (a queued message is user input and
	// can carry keys/tokens just like the head).
	if j.redactor != nil {
		if snapshot.PendingMessage != "" {
			snapshot.PendingMessage = j.redactor.Redact(snapshot.PendingMessage)
		}
		for i, m := range snapshot.PendingMessages {
			snapshot.PendingMessages[i] = j.redactor.Redact(m)
		}
	}

	snapshot.Timestamp = time.Now()
	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal recovery snapshot: %w", err)
	}
	if int64(len(b)) > maxRecoverySnapshotBytes {
		return fmt.Errorf("recovery snapshot exceeds %d-byte limit", maxRecoverySnapshotBytes)
	}
	if err := fileutil.SecurePrivateFile(j.recoveryPath); err != nil {
		return fmt.Errorf("secure recovery snapshot %q: %w", j.recoveryPath, err)
	}
	if err := fileutil.AtomicWrite(j.recoveryPath, b, 0o600); err != nil {
		return fmt.Errorf("write recovery snapshot %q: %w", j.recoveryPath, err)
	}
	return nil
}

func (j *ExecutionJournal) LoadRecovery() (*RecoverySnapshot, error) {
	if j == nil {
		return nil, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := fileutil.ReadPrivateFile(j.recoveryPath, maxRecoverySnapshotBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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

	f, err := fileutil.OpenPrivateRead(j.journalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	// Fixed-size ring: Tail(30) should never retain a multi-megabyte journal in
	// memory merely to discard all but its final lines.
	ring := make([]string, n)
	lineCount := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxJournalEntryBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ring[lineCount%n] = line
			lineCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	kept := min(lineCount, n)
	lines := make([]string, 0, kept)
	start := lineCount - kept
	for i := range kept {
		lines = append(lines, ring[(start+i)%n])
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
