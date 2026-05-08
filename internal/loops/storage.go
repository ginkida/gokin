package loops

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gokin/internal/fileutil"
)

// Storage is the persistence boundary for loops. File-based by default;
// the interface lets tests inject an in-memory store.
type Storage interface {
	// Load returns all loop state files currently on disk, in stable
	// (created_at, then ID) order. Corrupt files are skipped with the
	// error returned in the second slot — caller decides whether to
	// surface to the user.
	Load() ([]*Loop, []error)

	// Save persists one loop atomically. Caller is responsible for
	// holding any in-memory locks while calling.
	Save(l *Loop) error

	// Delete removes a loop's state file. Idempotent — missing file is
	// not an error (the user may have rm'd it manually).
	Delete(id string) error
}

// FileStorage stores loops as JSON files in <root>/loops/<id>.json.
// Default root is ~/.gokin (resolved by NewDefaultFileStorage).
type FileStorage struct {
	dir string
}

// NewFileStorage creates a Storage backed by the given directory. Creates
// the directory on first Save — empty dir is OK at construction time.
func NewFileStorage(dir string) *FileStorage {
	return &FileStorage{dir: dir}
}

// NewDefaultFileStorage returns a FileStorage rooted at ~/.gokin/loops.
// Errors only on home-dir resolution failure (rare).
func NewDefaultFileStorage() (*FileStorage, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("loops: resolve home dir: %w", err)
	}
	return NewFileStorage(filepath.Join(home, ".gokin", "loops")), nil
}

// path computes the on-disk path for a given loop ID. Centralized so
// the convention can change in one place.
func (s *FileStorage) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Load reads every *.json file in the directory and parses it. Files
// that fail to parse are skipped and their errors collected — callers
// (typically the Manager constructor) log them so users can find and
// fix or delete bad state files without losing the rest of their loops.
func (s *FileStorage) Load() ([]*Loop, []error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // first run — no loops yet
		}
		return nil, []error{fmt.Errorf("loops: read dir %s: %w", s.dir, err)}
	}

	var loops []*Loop
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		fullPath := filepath.Join(s.dir, name)
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("loops: read %s: %w", name, readErr))
			continue
		}
		l, parseErr := Unmarshal(data)
		if parseErr != nil {
			errs = append(errs, fmt.Errorf("loops: parse %s: %w", name, parseErr))
			continue
		}
		loops = append(loops, l)
	}

	// Stable sort: oldest first, then by ID for determinism.
	sort.Slice(loops, func(i, j int) bool {
		if !loops[i].CreatedAt.Equal(loops[j].CreatedAt) {
			return loops[i].CreatedAt.Before(loops[j].CreatedAt)
		}
		return loops[i].ID < loops[j].ID
	})

	return loops, errs
}

// Save writes a single loop atomically. Uses fileutil.AtomicWrite (the
// same temp+rename+sync helper used by session save) so a power loss
// between write and rename can't leave a half-empty file.
func (s *FileStorage) Save(l *Loop) error {
	if err := l.Validate(); err != nil {
		return fmt.Errorf("loops: refuse to save invalid loop: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("loops: create dir: %w", err)
	}
	data, err := l.Marshal()
	if err != nil {
		return fmt.Errorf("loops: marshal: %w", err)
	}
	return fileutil.AtomicWrite(s.path(l.ID), data, 0600)
}

// Delete removes a loop's state file. Missing file is not an error —
// idempotent so the manager can call it without first checking existence.
func (s *FileStorage) Delete(id string) error {
	err := os.Remove(s.path(id))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loops: delete %s: %w", id, err)
	}
	return nil
}

// NewID generates a short hex identifier suitable for filenames and
// /loop subcommand args. 8 hex chars (32 bits) is plenty for a per-user
// list that typically has 0-10 active loops.
func NewID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Crypto rand failure is exotic enough that panic is fine — same
		// pattern as session ID generation in chat/session.go.
		panic(fmt.Sprintf("loops: generate id: %v", err))
	}
	return "loop-" + hex.EncodeToString(buf[:])
}
