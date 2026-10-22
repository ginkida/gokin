// Package rewind persists prompt checkpoints independently of the rolling undo
// stack. File snapshots are content addressed; only the last 100 turns retain
// conversation snapshots and edit timelines.
package rewind

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gokin/internal/chat"
	"gokin/internal/fileutil"
	"gokin/internal/undo"
)

const MaxPoints = 100
const maxBlobBytes = 64 << 20
const maxIndexBytes = 4 << 20
const maxEvents = 10000

type Point struct {
	ID       string    `json:"id"`
	Prompt   string    `json:"prompt"`
	Created  time.Time `json:"created"`
	Snapshot string    `json:"snapshot"`
	Events   []string  `json:"events,omitempty"`
	Error    string    `json:"error,omitempty"`
}
type Snapshot struct {
	Prompt string             `json:"prompt"`
	State  *chat.SessionState `json:"state"`
}
type index struct {
	Version   int          `json:"version"`
	SessionID string       `json:"session_id"`
	WorkDir   string       `json:"work_dir"`
	Points    []Point      `json:"points"`
	Summaries []SummaryRef `json:"summaries,omitempty"`
}
type Store struct {
	mu      sync.Mutex
	dir     string
	doc     index
	failure error
}

// Open reads an existing journal without creating a store. The application's
// existing session writer lease also protects this journal from other writers.
func Open(root, sessionID, workDir string) (*Store, error) {
	if err := chat.ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: filepath.Join(root, sessionID), doc: index{Version: 1, SessionID: sessionID, WorkDir: resolved}}
	for _, path := range []string{root, s.dir} {
		if info, err := os.Lstat(path); err == nil && !info.IsDir() {
			return nil, fmt.Errorf("checkpoint directory is not a real directory")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	data, err := fileutil.ReadPrivateFile(filepath.Join(s.dir, "index.json"), maxIndexBytes)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var doc index
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Version != 1 || doc.SessionID != sessionID || doc.WorkDir != resolved || len(doc.Points) > MaxPoints {
		return nil, fmt.Errorf("checkpoint journal identity or version mismatch")
	}
	seen := map[string]bool{}
	for _, p := range doc.Points {
		if !digest(p.Snapshot) || len(p.ID) != 32 || seen[p.ID] || len(p.Events) > maxEvents {
			return nil, fmt.Errorf("invalid checkpoint journal")
		}
		if _, err := hex.DecodeString(p.ID); err != nil {
			return nil, fmt.Errorf("invalid checkpoint ID")
		}
		seen[p.ID] = true
		for _, hash := range p.Events {
			if !digest(hash) {
				return nil, fmt.Errorf("invalid checkpoint event")
			}
		}
	}
	s.doc = doc
	if len(doc.Summaries) > MaxSummaries {
		return nil, fmt.Errorf("too many summary archives")
	}
	for _, ref := range doc.Summaries {
		if len(ref.ID) != 32 || !digest(ref.Blob) || !digest(ref.Marker) {
			return nil, fmt.Errorf("invalid summary archive reference")
		}
		if _, err := hex.DecodeString(ref.ID); err != nil {
			return nil, err
		}
		if seen[ref.ID] {
			return nil, fmt.Errorf("duplicate summary archive identity")
		}
		seen[ref.ID] = true
	}
	return s, nil
}
func digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func (s *Store) save() error {
	data, err := json.Marshal(s.doc)
	if err != nil {
		return err
	}
	if len(data) > maxIndexBytes {
		return fmt.Errorf("checkpoint journal is too large")
	}
	if err := fileutil.EnsurePrivateDir(filepath.Dir(s.dir)); err != nil {
		return err
	}
	if err := fileutil.EnsurePrivateDir(s.dir); err != nil {
		return err
	}
	if err := fileutil.SecurePrivateFile(filepath.Join(s.dir, "index.json")); err != nil {
		return err
	}
	return fileutil.AtomicWrite(filepath.Join(s.dir, "index.json"), data, 0600)
}
func (s *Store) writeBlob(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > maxBlobBytes {
		return "", fmt.Errorf("checkpoint snapshot exceeds %d bytes", maxBlobBytes)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := fileutil.EnsurePrivateDir(filepath.Dir(s.dir)); err != nil {
		return "", err
	}
	if err := fileutil.EnsurePrivateDir(s.dir); err != nil {
		return "", err
	}
	if err := fileutil.AtomicWrite(filepath.Join(s.dir, hash+".json"), data, 0600); err != nil {
		return "", err
	}
	return hash, nil
}
func (s *Store) readBlob(hash string, value any) error {
	if !digest(hash) {
		return fmt.Errorf("invalid checkpoint snapshot reference")
	}
	for _, path := range []string{filepath.Dir(s.dir), s.dir} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkpoint storage is not a real directory")
		}
	}
	data, err := fileutil.ReadPrivateFile(filepath.Join(s.dir, hash+".json"), maxBlobBytes)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
		return fmt.Errorf("checkpoint snapshot checksum mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func (s *Store) Begin(prompt string, state *chat.SessionState) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == nil || state.ID != s.doc.SessionID {
		return "", fmt.Errorf("checkpoint session mismatch")
	}
	if err := s.importHistoryWindows(state.HistoryWindows); err != nil {
		return "", err
	}
	hash, err := s.writeBlob(Snapshot{Prompt: prompt, State: state})
	if err != nil {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	preview := []rune(prompt)
	if len(preview) > 160 {
		preview = preview[:160]
	}
	before := s.doc.Points
	s.doc.Points = append(append([]Point(nil), before...), Point{ID: id, Prompt: string(preview), Created: time.Now(), Snapshot: hash})
	if len(s.doc.Points) > MaxPoints {
		s.doc.Points = s.doc.Points[len(s.doc.Points)-MaxPoints:]
	}
	if err := s.save(); err != nil {
		s.doc.Points = before
		return "", err
	}
	s.failure = nil
	s.collect()
	return id, nil
}

// Record receives committed tool and undo/redo transactions synchronously.
// A failed journal write poisons code restoration instead of offering a
// seemingly complete restore that would silently leave some edits behind.
func (s *Store) Record(operations []undo.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.doc.Points) == 0 || len(operations) == 0 {
		return nil
	}
	point := &s.doc.Points[len(s.doc.Points)-1]
	for _, operation := range operations {
		if operation.Missing != "" {
			point.Error = "A file change was not captured: " + operation.Missing
			return s.save()
		}
	}
	if s.failure != nil {
		return s.failure
	}
	hash, err := s.writeBlob(operations)
	if err == nil && len(point.Events) >= maxEvents {
		err = fmt.Errorf("checkpoint has too many file transactions")
	}
	if err == nil {
		point.Events = append(point.Events, hash)
		err = s.save()
	}
	if err != nil {
		point.Error = "File snapshot persistence failed"
		s.failure = err
		_ = s.save()
	}
	return err
}
func (s *Store) Points() []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Point, len(s.doc.Points))
	copy(out, s.doc.Points)
	for i := range out {
		out[i].Events = append([]string(nil), out[i].Events...)
	}
	return out
}

// Restore restores before the selected prompt. apply is nil for code-only
// rewind; otherwise it publishes conversation state and returns a rollback.
// The caller must own the foreground slot and prevent background file edits.
func (s *Store) Restore(id string, code, conversation bool, apply func(*Snapshot) (func() error, error)) (int, error) {
	return s.restore(id, code, conversation, apply, undo.ApplyOperations)
}

func (s *Store) restore(id string, code, conversation bool, apply func(*Snapshot) (func() error, error), applyOperations func([]undo.Operation) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := -1
	for i, p := range s.doc.Points {
		if p.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return 0, fmt.Errorf("checkpoint not found")
	}
	if !code && !conversation {
		return 0, fmt.Errorf("select code, conversation or both")
	}
	var snapshot Snapshot
	if err := s.readBlob(s.doc.Points[at].Snapshot, &snapshot); err != nil {
		return 0, err
	}
	if snapshot.State == nil || snapshot.State.ID != s.doc.SessionID {
		return 0, fmt.Errorf("checkpoint snapshot identity mismatch")
	}
	var events []undo.Operation
	var snapshotBytes int
	if code {
		if s.failure != nil {
			return 0, s.failure
		}
		for _, p := range s.doc.Points[at:] {
			if p.Error != "" {
				return 0, fmt.Errorf("checkpoint cannot restore code: %s", p.Error)
			}
			for _, hash := range p.Events {
				var ops []undo.Operation
				if err := s.readBlob(hash, &ops); err != nil {
					return 0, err
				}
				for _, operation := range ops {
					snapshotBytes += len(operation.Change.OldContent) + len(operation.Change.NewContent)
				}
				if snapshotBytes > 256<<20 || len(events)+len(ops) > 100000 {
					return 0, fmt.Errorf("checkpoint restore exceeds the bounded transaction size")
				}
				events = append(events, ops...)
			}
		}
	}
	inverse := undo.ReverseOperations(events)
	if err := applyOperations(inverse); err != nil {
		if errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
			s.failure = err
		}
		return 0, err
	}
	var rollback func() error
	fail := func(err error) (int, error) {
		if errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
			s.failure = err
			return 0, err
		}
		if rollback != nil {
			if e := rollback(); e != nil {
				return 0, fmt.Errorf("%w; conversation rollback failed: %v", err, e)
			}
		}
		if e := applyOperations(events); e != nil {
			failure := fmt.Errorf("%w; code rollback failed: %w", err, e)
			if errors.Is(e, fileutil.ErrAtomicWriteCommitUncertain) {
				s.failure = failure
			}
			return 0, failure
		}
		return 0, err
	}
	if conversation && apply == nil {
		return fail(fmt.Errorf("conversation restore is unavailable"))
	}
	if apply != nil {
		var err error
		var selected *Snapshot
		if conversation {
			selected = &snapshot
		}
		rollback, err = apply(selected)
		if err != nil {
			return fail(err)
		}
	}
	old, _ := json.Marshal(s.doc)
	if conversation {
		var retained []string
		var retainedError string
		if !code {
			for _, p := range s.doc.Points[at:] {
				retained = append(retained, p.Events...)
				if p.Error != "" {
					retainedError = p.Error
				}
			}
		}
		s.doc.Points = s.doc.Points[:at+1]
		s.doc.Points[at].Events = retained
		s.doc.Points[at].Error = retainedError
	} else if len(inverse) > 0 {
		hash, err := s.writeBlob(inverse)
		if err != nil {
			return fail(err)
		}
		last := len(s.doc.Points) - 1
		s.doc.Points[last].Events = append(s.doc.Points[last].Events, hash)
	}
	if err := s.save(); err != nil {
		_ = json.Unmarshal(old, &s.doc)
		return fail(err)
	}
	s.collect()
	return len(events), nil
}

// collect drops unreferenced snapshots only after a new index is durable.
func (s *Store) collect() {
	live := map[string]bool{}
	for _, ref := range s.doc.Summaries {
		live[ref.Blob] = true
	}
	for _, p := range s.doc.Points {
		live[p.Snapshot] = true
		for _, hash := range p.Events {
			live[hash] = true
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) == 69 && filepath.Ext(name) == ".json" {
			hash := name[:64]
			if digest(hash) && !live[hash] {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
		}
	}
}

// Fork copies checkpoint ownership into a new session. Blobs remain immutable;
// conversation identities are rebound so either branch can rewind and prune
// its own journal without changing the other branch.
func (s *Store) Fork(newID string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := chat.ValidateSessionID(newID); err != nil {
		return nil, err
	}
	if newID == s.doc.SessionID {
		return nil, fmt.Errorf("fork checkpoint identity must be new")
	}
	if len(s.doc.Points) == 0 && len(s.doc.Summaries) == 0 {
		return func() {}, nil
	}
	destination := filepath.Join(filepath.Dir(s.dir), newID)
	if err := os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(destination) }
	target := &Store{dir: destination, doc: index{Version: 1, SessionID: newID, WorkDir: s.doc.WorkDir}}
	for _, ref := range s.doc.Summaries {
		var archive SummaryArchive
		if err := s.readBlob(ref.Blob, &archive); err != nil {
			cleanup()
			return nil, err
		}
		if _, err := target.writeBlob(archive); err != nil {
			cleanup()
			return nil, err
		}
		target.doc.Summaries = append(target.doc.Summaries, ref)
	}
	for _, point := range s.doc.Points {
		var snapshot Snapshot
		if err := s.readBlob(point.Snapshot, &snapshot); err != nil {
			cleanup()
			return nil, err
		}
		if snapshot.State == nil {
			cleanup()
			return nil, fmt.Errorf("invalid fork checkpoint")
		}
		snapshot.State.ID = newID
		hash, err := target.writeBlob(snapshot)
		if err != nil {
			cleanup()
			return nil, err
		}
		point.Snapshot = hash
		for _, event := range point.Events {
			var operations []undo.Operation
			if err := s.readBlob(event, &operations); err != nil {
				cleanup()
				return nil, err
			}
			if _, err := target.writeBlob(operations); err != nil {
				cleanup()
				return nil, err
			}
		}
		target.doc.Points = append(target.doc.Points, point)
	}
	if err := target.save(); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
