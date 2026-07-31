package background

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gokin/internal/fileutil"

	"github.com/google/uuid"
)

const (
	StateStarting    = "starting"
	StateRunning     = "running"
	StateFinishing   = "finishing"
	StateStopping    = "stopping"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateStopped     = "stopped"
	StateInterrupted = "interrupted"

	maxJobFileBytes = 1 << 20
	maxControlBytes = 64 << 10
	maxControls     = 20
	maxJobs         = 10_000
	startingGrace   = 10 * time.Second
)

// Job is the durable, non-secret control record for one detached Gokin run.
// Prompts, flags, provider credentials, and model output deliberately never
// enter this file; stdout/stderr live in private append-only log files.
type Job struct {
	ID          string    `json:"id"`
	ParentJobID string    `json:"parent_job_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	PID         int       `json:"pid,omitempty"`
	State       string    `json:"state"`
	WorkDir     string    `json:"work_dir"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	ExitCode    int       `json:"exit_code,omitempty"`
	// PendingInput was accepted but never claimed. AmbiguousInput was claimed
	// before a worker/turn ended without a delivery commit.
	PendingInput   int `json:"pending_input,omitempty"`
	AmbiguousInput int `json:"ambiguous_input,omitempty"`
}

const (
	ControlPending   = "pending"
	ControlClaimed   = "claimed"
	ControlDelivered = "delivered"
)

var ErrAmbiguousControl = errors.New("a previously claimed background control message has ambiguous delivery")

// Control is one durable message sent from another process to a live worker.
// Delivered records retain only metadata; Message is cleared after completion.
type Control struct {
	ID          string    `json:"id"`
	JobID       string    `json:"job_id"`
	Message     string    `json:"message,omitempty"`
	State       string    `json:"state"`
	Outcome     string    `json:"outcome,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ClaimedAt   time.Time `json:"claimed_at,omitempty"`
	DeliveredAt time.Time `json:"delivered_at,omitempty"`
}

func (j Job) Terminal() bool {
	switch j.State {
	case StateSucceeded, StateFailed, StateStopped, StateInterrupted:
		return true
	default:
		return false
	}
}

type Store struct {
	root string
}

func NewStore() (*Store, error) {
	root, err := dataRoot()
	if err != nil {
		return nil, err
	}
	return NewStoreAt(root)
}

func NewStoreAt(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("background store path is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve background store: %w", err)
	}
	store := &Store{root: filepath.Clean(absolute)}
	for _, dir := range []string{store.root, store.jobsDir(), store.logsDir(), store.locksDir(), store.inboxDir()} {
		if err := ensurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("prepare background store %q: %w", dir, err)
		}
	}
	// Nothing else ever removed a finished job, so every detached run left a
	// record, two logs, up to two locks and an inbox directory behind forever —
	// and past maxJobs entries `gokin agents` failed permanently with no way to
	// recover. Sweeping here (best effort) keeps the store bounded without
	// asking the user to clean up a directory they never see.
	store.Sweep(defaultSweepMaxAge, defaultSweepKeep)
	return store, nil
}

const (
	// A finished job's record is worth keeping long enough to read its logs the
	// next morning, not forever.
	defaultSweepMaxAge = 7 * 24 * time.Hour
	defaultSweepKeep   = 200
)

// Sweep removes terminal jobs that are older than maxAge, and any terminal job
// beyond the keep newest. It never touches a job whose worker lease is still
// held: a live worker's state is not ours to delete, and an unreadable or
// unprovable lease counts as held. Errors are deliberately swallowed per job —
// housekeeping must never make the store unusable.
func (s *Store) Sweep(maxAge time.Duration, keep int) {
	if s == nil {
		return
	}
	entries, err := os.ReadDir(s.jobsDir())
	if err != nil {
		return
	}
	type candidate struct {
		id       string
		finished time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		job, loadErr := s.Load(id)
		if loadErr != nil {
			continue
		}
		if !job.Terminal() {
			continue
		}
		finished := job.EndedAt
		if finished.IsZero() {
			finished = job.StartedAt
		}
		candidates = append(candidates, candidate{id: id, finished: finished})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].finished.After(candidates[j].finished)
	})

	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}
	for index, entry := range candidates {
		tooMany := keep > 0 && index >= keep
		tooOld := !cutoff.IsZero() && entry.finished.Before(cutoff)
		if !tooMany && !tooOld {
			continue
		}
		if held, leaseErr := s.WorkerLeaseHeld(entry.id); leaseErr != nil || held {
			continue
		}
		s.removeJobArtifacts(entry.id)
	}
}

func (s *Store) removeJobArtifacts(id string) {
	if path, err := s.jobPath(id); err == nil {
		_ = os.Remove(path)
	}
	if path, err := s.StdoutPath(id); err == nil {
		_ = os.Remove(path)
	}
	if path, err := s.StderrPath(id); err == nil {
		_ = os.Remove(path)
	}
	if path, err := s.controlDir(id); err == nil {
		_ = os.RemoveAll(path)
	}
	// Lock files last: dropping them before the record would let a concurrent
	// probe recreate an orphan for a job that no longer exists.
	if path, err := s.lockPath(id); err == nil {
		_ = os.Remove(path)
	}
	if path, err := s.metadataLockPath(id); err == nil {
		_ = os.Remove(path)
	}
}

func dataRoot() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "gokin", "background"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "gokin", "background"), nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is not a real directory")
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	after, err := dir.Stat()
	if err != nil {
		return err
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		return fmt.Errorf("directory identity changed while opening")
	}
	return dir.Chmod(0o700)
}

func NewJobID() string { return uuid.NewString() }

func ValidateJobID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("invalid background job ID %q", id)
	}
	return nil
}

func (s *Store) Root() string     { return s.root }
func (s *Store) jobsDir() string  { return filepath.Join(s.root, "jobs") }
func (s *Store) logsDir() string  { return filepath.Join(s.root, "logs") }
func (s *Store) locksDir() string { return filepath.Join(s.root, "locks") }
func (s *Store) inboxDir() string { return filepath.Join(s.root, "inbox") }

func (s *Store) jobPath(id string) (string, error) {
	if err := ValidateJobID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.jobsDir(), id+".json"), nil
}

func (s *Store) StdoutPath(id string) (string, error) {
	if err := ValidateJobID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.logsDir(), id+".jsonl"), nil
}

func (s *Store) StderrPath(id string) (string, error) {
	if err := ValidateJobID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.logsDir(), id+".stderr.log"), nil
}

func (s *Store) lockPath(id string) (string, error) {
	if err := ValidateJobID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.locksDir(), id+".lock"), nil
}

func (s *Store) metadataLockPath(id string) (string, error) {
	if err := ValidateJobID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.locksDir(), id+".meta.lock"), nil
}

func (s *Store) Create(job Job) error {
	if err := ValidateJobID(job.ID); err != nil {
		return err
	}
	if job.ParentJobID != "" {
		if err := ValidateJobID(job.ParentJobID); err != nil {
			return fmt.Errorf("invalid parent background job ID: %w", err)
		}
		if job.ParentJobID == job.ID {
			return fmt.Errorf("background job cannot be its own parent")
		}
	}
	if job.State != StateStarting {
		return fmt.Errorf("new background job must start in %q state", StateStarting)
	}
	if job.StartedAt.IsZero() {
		job.StartedAt = time.Now()
	}
	if strings.TrimSpace(job.WorkDir) == "" {
		return fmt.Errorf("background job work directory is empty")
	}
	path, _ := s.jobPath(job.ID)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("background job %q already exists", job.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensurePrivateDir(filepath.Join(s.inboxDir(), job.ID)); err != nil {
		return fmt.Errorf("prepare background job inbox: %w", err)
	}
	return s.write(job)
}

func validateControlMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("background control message is empty")
	}
	if len(message) > maxControlBytes {
		return fmt.Errorf("background control message exceeds %d KiB", maxControlBytes>>10)
	}
	if !utf8.ValidString(message) || strings.IndexByte(message, 0) >= 0 {
		return fmt.Errorf("background control message must be valid UTF-8 without NUL bytes")
	}
	return nil
}

func (s *Store) controlDir(jobID string) (string, error) {
	if err := ValidateJobID(jobID); err != nil {
		return "", err
	}
	return filepath.Join(s.inboxDir(), jobID), nil
}

func (s *Store) controlPath(jobID, controlID string) (string, error) {
	dir, err := s.controlDir(jobID)
	if err != nil {
		return "", err
	}
	if err := ValidateJobID(controlID); err != nil {
		return "", fmt.Errorf("invalid control ID: %w", err)
	}
	return filepath.Join(dir, controlID+".json"), nil
}

func (s *Store) EnqueueControl(jobID, message string) (Control, error) {
	if err := validateControlMessage(message); err != nil {
		return Control{}, err
	}
	lease, err := s.acquireMetadataLease(jobID)
	if err != nil {
		return Control{}, err
	}
	defer lease.Release()
	job, err := s.Load(jobID)
	if err != nil {
		return Control{}, err
	}
	if job.State != StateStarting && job.State != StateRunning {
		return Control{}, fmt.Errorf("background job %q is not accepting input in state %s", jobID, job.State)
	}
	controlDir, err := s.controlDir(jobID)
	if err != nil {
		return Control{}, err
	}
	if err := ensurePrivateDir(controlDir); err != nil {
		return Control{}, err
	}
	controls, err := s.loadControlsLocked(jobID)
	if err != nil {
		return Control{}, err
	}
	pending := 0
	for _, control := range controls {
		if control.State == ControlPending || control.State == ControlClaimed {
			pending++
		}
	}
	if pending >= maxControls {
		return Control{}, fmt.Errorf("background job inbox is full (%d messages)", maxControls)
	}
	control := Control{
		ID:        NewJobID(),
		JobID:     jobID,
		Message:   message,
		State:     ControlPending,
		CreatedAt: time.Now(),
	}
	if err := s.writeControl(control); err != nil {
		return Control{}, err
	}
	return control, nil
}

// ClaimNextControl claims the oldest pending input. A claimed record from a
// crashed worker is an ambiguity barrier: later messages must not overtake it
// and the possibly-delivered message must not be replayed automatically.
func (s *Store) ClaimNextControl(jobID string) (*Control, error) {
	lease, err := s.acquireMetadataLease(jobID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	controlDir, err := s.controlDir(jobID)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(controlDir); err != nil {
		return nil, err
	}
	controls, err := s.loadControlsLocked(jobID)
	if err != nil {
		return nil, err
	}
	for i := range controls {
		control := &controls[i]
		switch control.State {
		case ControlDelivered:
			continue
		case ControlClaimed:
			return nil, fmt.Errorf("%w: control %s", ErrAmbiguousControl, control.ID)
		case ControlPending:
			control.State = ControlClaimed
			control.ClaimedAt = time.Now()
			if err := s.writeControl(*control); err != nil {
				return nil, err
			}
			copy := *control
			return &copy, nil
		default:
			return nil, fmt.Errorf("background control %s has invalid state %q", control.ID, control.State)
		}
	}
	return nil, nil
}

// BeginFinishing atomically closes input admission only when the inbox is
// empty. A concurrent sender either commits before this lock (and returns
// false here) or observes StateFinishing and is rejected; no accepted message
// can be stranded behind worker exit.
func (s *Store) BeginFinishing(jobID string) (bool, error) {
	lease, err := s.acquireMetadataLease(jobID)
	if err != nil {
		return false, err
	}
	defer lease.Release()
	job, err := s.Load(jobID)
	if err != nil {
		return false, err
	}
	if job.State != StateRunning {
		return false, fmt.Errorf("background job %q cannot finish from state %s", jobID, job.State)
	}
	controls, err := s.loadControlsLocked(jobID)
	if err != nil {
		return false, err
	}
	for _, control := range controls {
		if control.State == ControlPending || control.State == ControlClaimed {
			return false, nil
		}
	}
	job.State = StateFinishing
	if err := s.write(job); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CompleteControl(control Control, outcome string) error {
	lease, err := s.acquireMetadataLease(control.JobID)
	if err != nil {
		return err
	}
	defer lease.Release()
	path, err := s.controlPath(control.JobID, control.ID)
	if err != nil {
		return err
	}
	current, err := s.loadControl(path, control.JobID, control.ID)
	if err != nil {
		return err
	}
	if current.State != ControlClaimed {
		return fmt.Errorf("background control %s is %s, want claimed", current.ID, current.State)
	}
	current.State = ControlDelivered
	current.Outcome = strings.TrimSpace(outcome)
	current.Message = ""
	current.DeliveredAt = time.Now()
	if err := s.writeControl(current); err != nil {
		return err
	}
	// The atomic delivered record is the commit point. Removing it afterward
	// keeps long-lived inboxes bounded and removes user text from disk; a
	// failed cleanup is harmless because delivered records are skipped.
	_ = os.Remove(path)
	return nil
}

func (s *Store) loadControlsLocked(jobID string) ([]Control, error) {
	dir, err := s.controlDir(jobID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxControls*4 {
		return nil, fmt.Errorf("background job inbox contains too many records")
	}
	controls := make([]Control, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		controlID := strings.TrimSuffix(entry.Name(), ".json")
		path, pathErr := s.controlPath(jobID, controlID)
		if pathErr != nil {
			return nil, pathErr
		}
		control, loadErr := s.loadControl(path, jobID, controlID)
		if loadErr != nil {
			return nil, loadErr
		}
		controls = append(controls, control)
	}
	sort.Slice(controls, func(i, j int) bool {
		if controls[i].CreatedAt.Equal(controls[j].CreatedAt) {
			return controls[i].ID < controls[j].ID
		}
		return controls[i].CreatedAt.Before(controls[j].CreatedAt)
	})
	return controls, nil
}

func (s *Store) loadControl(path, jobID, controlID string) (Control, error) {
	data, err := readPrivateRegular(path, maxControlBytes+4096)
	if err != nil {
		return Control{}, err
	}
	var control Control
	if err := json.Unmarshal(data, &control); err != nil {
		return Control{}, err
	}
	if control.ID != controlID || control.JobID != jobID {
		return Control{}, fmt.Errorf("background control identity mismatch")
	}
	return control, nil
}

func (s *Store) writeControl(control Control) error {
	path, err := s.controlPath(control.JobID, control.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(control, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}

func (s *Store) Load(id string) (Job, error) {
	path, err := s.jobPath(id)
	if err != nil {
		return Job{}, err
	}
	data, err := readPrivateRegular(path, maxJobFileBytes)
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, fmt.Errorf("decode background job %q: %w", id, err)
	}
	if job.ID != id {
		return Job{}, fmt.Errorf("background job identity mismatch: requested %q, file contains %q", id, job.ID)
	}
	return job, nil
}

// Resolve accepts a full UUID or an unambiguous UUID prefix. Short IDs keep
// management commands ergonomic without weakening file-path validation.
func (s *Store) Resolve(query string) (Job, error) {
	query = strings.TrimSpace(strings.ToLower(query))
	if parsed, err := uuid.Parse(query); err == nil && parsed.String() == query {
		return s.Load(query)
	}
	if len(query) < 6 || strings.IndexFunc(query, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-')
	}) >= 0 {
		return Job{}, fmt.Errorf("invalid background job ID or prefix %q", query)
	}
	entries, err := os.ReadDir(s.jobsDir())
	if err != nil {
		return Job{}, err
	}
	var matched string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.HasPrefix(id, query) {
			continue
		}
		if matched != "" {
			return Job{}, fmt.Errorf("background job prefix %q is ambiguous", query)
		}
		matched = id
	}
	if matched == "" {
		return Job{}, fmt.Errorf("background job %q was not found", query)
	}
	return s.Load(matched)
}

func (s *Store) write(job Job) error {
	path, err := s.jobPath(job.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}

func (s *Store) Update(id string, mutate func(*Job) error) (Job, error) {
	if mutate == nil {
		return Job{}, fmt.Errorf("background job mutation is required")
	}
	lease, err := s.acquireMetadataLease(id)
	if err != nil {
		return Job{}, err
	}
	defer lease.Release()
	job, err := s.Load(id)
	if err != nil {
		return Job{}, err
	}
	if err := mutate(&job); err != nil {
		return Job{}, err
	}
	if err := s.write(job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Store) MarkRunning(id string, pid int) (Job, error) {
	if pid <= 0 {
		return Job{}, fmt.Errorf("invalid background worker PID %d", pid)
	}
	return s.Update(id, func(job *Job) error {
		if job.Terminal() {
			return fmt.Errorf("background job %q is already terminal", id)
		}
		job.PID = pid
		job.State = StateRunning
		return nil
	})
}

func (s *Store) SetSessionID(id, sessionID string) (Job, error) {
	return s.Update(id, func(job *Job) error {
		job.SessionID = sessionID
		return nil
	})
}

func (s *Store) Finish(id, state string, exitCode int) (Job, error) {
	if state != StateSucceeded && state != StateFailed {
		return Job{}, fmt.Errorf("invalid worker terminal state %q", state)
	}
	return s.Update(id, func(job *Job) error {
		if job.State == StateStopping {
			job.State = StateStopped
		} else {
			job.State = state
		}
		job.ExitCode = exitCode
		job.EndedAt = time.Now()
		if err := s.setControlCountsLocked(job); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) MarkStopping(id string) (Job, error) {
	return s.Update(id, func(job *Job) error {
		if job.Terminal() {
			return fmt.Errorf("background job %q is already %s", id, job.State)
		}
		job.State = StateStopping
		return nil
	})
}

func (s *Store) List(workDir string, includeCompleted bool) ([]Job, error) {
	entries, err := os.ReadDir(s.jobsDir())
	if err != nil {
		return nil, err
	}
	if len(entries) > maxJobs {
		return nil, fmt.Errorf("background job directory contains more than %d entries", maxJobs)
	}
	cleanWorkDir := ""
	if workDir != "" {
		cleanWorkDir = filepath.Clean(workDir)
	}
	jobs := make([]Job, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		job, loadErr := s.Load(id)
		if loadErr != nil {
			continue
		}
		job, loadErr = s.Reconcile(job)
		if loadErr != nil {
			return nil, loadErr
		}
		job, loadErr = s.RefreshControlCounts(job)
		if loadErr != nil {
			return nil, loadErr
		}
		if cleanWorkDir != "" && filepath.Clean(job.WorkDir) != cleanWorkDir {
			continue
		}
		if !includeCompleted && job.Terminal() {
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].StartedAt.After(jobs[j].StartedAt)
	})
	return jobs, nil
}

func (s *Store) RefreshControlCounts(job Job) (Job, error) {
	return s.Update(job.ID, func(current *Job) error {
		return s.setControlCountsLocked(current)
	})
}

func (s *Store) setControlCountsLocked(job *Job) error {
	if job == nil {
		return fmt.Errorf("background job is nil")
	}
	dir, err := s.controlDir(job.ID)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	controls, err := s.loadControlsLocked(job.ID)
	if err != nil {
		return err
	}
	job.PendingInput = 0
	job.AmbiguousInput = 0
	for _, control := range controls {
		switch control.State {
		case ControlPending:
			job.PendingInput++
		case ControlClaimed:
			job.AmbiguousInput++
		}
	}
	return nil
}

func (s *Store) Reconcile(job Job) (Job, error) {
	if job.Terminal() {
		return job, nil
	}
	held, err := s.WorkerLeaseHeld(job.ID)
	if err != nil || held {
		return job, err
	}
	if job.State == StateStarting && time.Since(job.StartedAt) < startingGrace {
		return job, nil
	}
	terminal := StateInterrupted
	if job.State == StateStopping {
		terminal = StateStopped
	}
	return s.Update(job.ID, func(current *Job) error {
		if !current.Terminal() {
			current.State = terminal
			current.EndedAt = time.Now()
		}
		return nil
	})
}

func readPrivateRegular(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > limit {
		return nil, fmt.Errorf("background state path %q is not a bounded regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("background state file identity changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("background state file exceeds %d bytes", limit)
	}
	return data, nil
}
