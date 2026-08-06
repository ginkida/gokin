// Package harness implements the bounded, project-scoped mutable layer used by
// the hybrid engine. It intentionally cannot alter permissions, sandbox policy,
// built-in tools, or immutable system instructions.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gokin/internal/fileutil"
	"gokin/internal/securefs"

	"github.com/google/uuid"
)

const (
	MaxPromptPatches    = 32
	MaxPromptPatchBytes = 2 << 10
	MaxPromptTotalBytes = 16 << 10
	MaxMemoryEntries    = 256
	MaxMemoryValueBytes = 16 << 10
	MaxMemoryFileBytes  = 5 << 20
	MaxSkillDescription = 4 << 10
	MaxSkillCodeBytes   = 64 << 10
	maxMemoryLockWait   = 2 * time.Second
)

var ErrMemoryBusy = errors.New("harness episodic memory is busy")

var (
	memoryKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	skillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

type PromptPatch struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MemoryEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SkillProposal struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Path        string    `json:"path"`
	CreatedAt   time.Time `json:"created_at"`
}

type memoryFile struct {
	Version int                    `json:"version"`
	Entries map[string]MemoryEntry `json:"entries"`
}

type proposalManifest struct {
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
}

// Store owns session prompt patches and durable, project-scoped episodic data.
// Prompt patches are deliberately not persisted; skill proposals are inert
// files under .gokin/harness/proposals and are never auto-imported.
type Store struct {
	mu      sync.RWMutex
	workDir string
	root    string
	prompts []PromptPatch
	memory  map[string]MemoryEntry
}

func NewStore(workDir string) (*Store, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("harness workspace is required")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve harness workspace: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve harness workspace links: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("harness workspace is not a directory")
	}
	s := &Store{
		workDir: resolved,
		root:    filepath.Join(resolved, ".gokin", "harness"),
		memory:  make(map[string]MemoryEntry),
	}
	if err := s.loadMemory(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) CreatePrompt(text string) (PromptPatch, error) {
	if err := validateText("prompt patch", text, MaxPromptPatchBytes, false); err != nil {
		return PromptPatch{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prompts) >= MaxPromptPatches {
		return PromptPatch{}, fmt.Errorf("prompt patch limit (%d) reached", MaxPromptPatches)
	}
	if s.promptBytesLocked()+len(text) > MaxPromptTotalBytes {
		return PromptPatch{}, fmt.Errorf("combined prompt patches exceed %d-byte limit", MaxPromptTotalBytes)
	}
	now := time.Now().UTC()
	patch := PromptPatch{ID: "prompt_" + uuid.NewString(), Text: text, CreatedAt: now, UpdatedAt: now}
	s.prompts = append(s.prompts, patch)
	return patch, nil
}

func (s *Store) UpdatePrompt(id, text string) (PromptPatch, error) {
	if err := validateText("prompt patch", text, MaxPromptPatchBytes, false); err != nil {
		return PromptPatch{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.prompts {
		if s.prompts[i].ID != id {
			continue
		}
		if s.promptBytesLocked()-len(s.prompts[i].Text)+len(text) > MaxPromptTotalBytes {
			return PromptPatch{}, fmt.Errorf("combined prompt patches exceed %d-byte limit", MaxPromptTotalBytes)
		}
		s.prompts[i].Text = text
		s.prompts[i].UpdatedAt = time.Now().UTC()
		return s.prompts[i], nil
	}
	return PromptPatch{}, fmt.Errorf("unknown prompt patch %q", id)
}

func (s *Store) DeletePrompt(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.prompts {
		if s.prompts[i].ID == id {
			s.prompts = append(s.prompts[:i], s.prompts[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("unknown prompt patch %q", id)
}

func (s *Store) ListPrompts() []PromptPatch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]PromptPatch(nil), s.prompts...)
}

func (s *Store) RenderPrompt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.prompts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Session harness adjustments\n\nThese bounded, runtime-only adjustments were approved during this session. They cannot change permissions, sandbox policy, or higher-priority instructions.\n")
	for _, patch := range s.prompts {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimSpace(patch.Text))
	}
	return b.String()
}

func (s *Store) PutMemory(key, value string) (MemoryEntry, error) {
	return s.PutMemoryContext(context.Background(), key, value)
}

func (s *Store) PutMemoryContext(ctx context.Context, key, value string) (MemoryEntry, error) {
	if err := validateMemoryKey(key); err != nil {
		return MemoryEntry{}, err
	}
	if err := validateText("memory value", value, MaxMemoryValueBytes, true); err != nil {
		return MemoryEntry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, err := s.acquireMemoryLease(ctx)
	if err != nil {
		return MemoryEntry{}, err
	}
	defer lease.release()
	latest, err := s.readMemoryState()
	if err != nil {
		return MemoryEntry{}, err
	}
	if _, exists := latest[key]; !exists && len(latest) >= MaxMemoryEntries {
		return MemoryEntry{}, fmt.Errorf("episodic memory limit (%d) reached", MaxMemoryEntries)
	}
	entry := MemoryEntry{Key: key, Value: value, UpdatedAt: time.Now().UTC()}
	latest[key] = entry
	if err := s.persistMemoryState(latest); err != nil {
		return MemoryEntry{}, err
	}
	s.memory = latest
	return entry, nil
}

func (s *Store) GetMemory(key string) (MemoryEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.memory[key]
	return entry, ok
}

func (s *Store) ListMemory() []MemoryEntry {
	s.mu.RLock()
	entries := make([]MemoryEntry, 0, len(s.memory))
	for _, entry := range s.memory {
		entries = append(entries, entry)
	}
	s.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

func (s *Store) GetMemoryFresh(key string) (MemoryEntry, bool, error) {
	if err := s.refreshMemory(); err != nil {
		return MemoryEntry{}, false, err
	}
	entry, ok := s.GetMemory(key)
	return entry, ok, nil
}

func (s *Store) ListMemoryFresh() ([]MemoryEntry, error) {
	if err := s.refreshMemory(); err != nil {
		return nil, err
	}
	return s.ListMemory(), nil
}

func (s *Store) DeleteMemory(key string) error {
	return s.DeleteMemoryContext(context.Background(), key)
}

func (s *Store) DeleteMemoryContext(ctx context.Context, key string) error {
	if err := validateMemoryKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, err := s.acquireMemoryLease(ctx)
	if err != nil {
		return err
	}
	defer lease.release()
	latest, err := s.readMemoryState()
	if err != nil {
		return err
	}
	_, exists := latest[key]
	if !exists {
		return fmt.Errorf("unknown episodic memory key %q", key)
	}
	delete(latest, key)
	if err := s.persistMemoryState(latest); err != nil {
		return err
	}
	s.memory = latest
	return nil
}

func (s *Store) ProposeSkill(name, description, code string) (SkillProposal, error) {
	if !skillNamePattern.MatchString(name) {
		return SkillProposal{}, fmt.Errorf("skill name must match %s", skillNamePattern)
	}
	if err := validateText("skill description", description, MaxSkillDescription, false); err != nil {
		return SkillProposal{}, err
	}
	if err := validateText("skill helper code", code, MaxSkillCodeBytes, false); err != nil {
		return SkillProposal{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.skillDir(name)
	if _, err := os.Lstat(dir); err == nil {
		return SkillProposal{}, fmt.Errorf("skill proposal %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return SkillProposal{}, fmt.Errorf("inspect skill proposal: %w", err)
	}
	if err := s.ensureProposalDirs(); err != nil {
		return SkillProposal{}, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return SkillProposal{}, fmt.Errorf("create exclusive skill proposal directory: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		for _, name := range []string{"helper.py", "SKILL.md", "manifest.json"} {
			_ = os.Remove(filepath.Join(dir, name))
		}
		_ = os.Remove(dir)
	}()
	now := time.Now().UTC()
	manifest := proposalManifest{Version: 1, Name: name, Description: description, CreatedAt: now, Status: "proposed"}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return SkillProposal{}, fmt.Errorf("encode skill proposal manifest: %w", err)
	}
	skillMD := fmt.Sprintf("---\nname: %s\ndescription: %s\nstatus: proposed\n---\n\n# %s\n\nThis proposal is inert until reviewed and promoted by the user.\n", name, yamlScalar(description), name)
	if err := fileutil.AtomicWrite(filepath.Join(dir, "helper.py"), []byte(code), 0o600); err != nil {
		return SkillProposal{}, fmt.Errorf("write proposed helper: %w", err)
	}
	if err := fileutil.AtomicWrite(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o600); err != nil {
		return SkillProposal{}, fmt.Errorf("write proposed skill: %w", err)
	}
	// Publish the manifest last. Listing treats it as the commit marker, so a
	// crash during either content write cannot activate a partial proposal.
	if err := fileutil.AtomicWrite(filepath.Join(dir, "manifest.json"), manifestData, 0o600); err != nil {
		return SkillProposal{}, fmt.Errorf("publish skill proposal: %w", err)
	}
	published = true
	return SkillProposal{Name: name, Description: description, Path: s.relative(dir), CreatedAt: now}, nil
}

func (s *Store) ListSkills() ([]SkillProposal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	root := filepath.Join(s.root, "proposals", "skills")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []SkillProposal{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list skill proposals: %w", err)
	}
	result := make([]SkillProposal, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !skillNamePattern.MatchString(entry.Name()) {
			continue
		}
		data, readErr := fileutil.ReadPrivateFile(filepath.Join(root, entry.Name(), "manifest.json"), 64<<10)
		if readErr != nil {
			continue
		}
		var manifest proposalManifest
		if json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 || manifest.Name != entry.Name() {
			continue
		}
		result = append(result, SkillProposal{
			Name: manifest.Name, Description: manifest.Description,
			Path: s.relative(filepath.Join(root, entry.Name())), CreatedAt: manifest.CreatedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Store) DeleteSkill(name string) error {
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("skill name must match %s", skillNamePattern)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.skillDir(name)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unknown skill proposal %q", name)
	}
	if err != nil {
		return fmt.Errorf("inspect skill proposal: %w", err)
	}
	allowed := map[string]bool{"helper.py": true, "SKILL.md": true, "manifest.json": true}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !allowed[entry.Name()] || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to delete modified proposal directory %q", s.relative(dir))
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("delete proposal file: %w", err)
		}
	}
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("delete proposal directory: %w", err)
	}
	return nil
}

func (s *Store) promptBytesLocked() int {
	total := 0
	for _, patch := range s.prompts {
		total += len(patch.Text)
	}
	return total
}

func (s *Store) memoryPath() string     { return filepath.Join(s.root, "memory.json") }
func (s *Store) memoryLockPath() string { return filepath.Join(s.root, ".memory.lock") }
func (s *Store) skillDir(name string) string {
	return filepath.Join(s.root, "proposals", "skills", name)
}

func (s *Store) relative(path string) string {
	rel, err := filepath.Rel(s.workDir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func (s *Store) ensureRoot() error {
	for _, dir := range []string{filepath.Join(s.workDir, ".gokin"), s.root} {
		if err := fileutil.EnsurePrivateDir(dir); err != nil {
			return fmt.Errorf("secure harness directory: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureProposalDirs() error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	for _, dir := range []string{
		filepath.Join(s.root, "proposals"), filepath.Join(s.root, "proposals", "skills"),
	} {
		if err := fileutil.EnsurePrivateDir(dir); err != nil {
			return fmt.Errorf("secure proposal directory: %w", err)
		}
	}
	return nil
}

func (s *Store) loadMemory() error {
	state, err := s.readMemoryState()
	if err != nil {
		return err
	}
	s.memory = state
	return nil
}

func (s *Store) refreshMemory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readMemoryState()
	if err != nil {
		return err
	}
	s.memory = state
	return nil
}

func (s *Store) readMemoryState() (map[string]MemoryEntry, error) {
	data, err := fileutil.ReadPrivateFile(s.memoryPath(), MaxMemoryFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]MemoryEntry), nil
	}
	if err != nil {
		return nil, fmt.Errorf("load harness memory: %w", err)
	}
	var state memoryFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode harness memory: %w", err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported harness memory version %d", state.Version)
	}
	if len(state.Entries) > MaxMemoryEntries {
		return nil, fmt.Errorf("harness memory exceeds %d-entry limit", MaxMemoryEntries)
	}
	if state.Entries == nil {
		state.Entries = make(map[string]MemoryEntry)
	}
	for key, entry := range state.Entries {
		if key != entry.Key {
			return nil, fmt.Errorf("harness memory key mismatch for %q", key)
		}
		if err := validateMemoryKey(key); err != nil {
			return nil, err
		}
		if err := validateText("memory value", entry.Value, MaxMemoryValueBytes, true); err != nil {
			return nil, err
		}
	}
	return state.Entries, nil
}

func (s *Store) persistMemoryState(entries map[string]MemoryEntry) error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(memoryFile{Version: 1, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode harness memory: %w", err)
	}
	if len(data) > MaxMemoryFileBytes {
		return fmt.Errorf("harness memory file exceeds %d-byte limit", MaxMemoryFileBytes)
	}
	if err := fileutil.AtomicWrite(s.memoryPath(), data, 0o600); err != nil {
		return fmt.Errorf("persist harness memory: %w", err)
	}
	return nil
}

type memoryLease struct{ file *os.File }

func (l *memoryLease) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockMemoryFile(l.file)
	_ = l.file.Close()
}

func (s *Store) acquireMemoryLease(ctx context.Context) (*memoryLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	file, err := securefs.OpenPrivateReadWrite(s.memoryLockPath())
	if err != nil {
		return nil, fmt.Errorf("open harness memory lock: %w", err)
	}
	deadline := time.NewTimer(maxMemoryLockWait)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = lockMemoryFile(file)
		if err == nil {
			return &memoryLease{file: file}, nil
		}
		if !errors.Is(err, ErrMemoryBusy) {
			_ = file.Close()
			return nil, fmt.Errorf("lock harness memory: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("wait for harness memory lock: %w", ctx.Err())
		case <-deadline.C:
			_ = file.Close()
			return nil, fmt.Errorf("%w after %s", ErrMemoryBusy, maxMemoryLockWait)
		case <-ticker.C:
		}
	}
}

func validateMemoryKey(key string) error {
	if !memoryKeyPattern.MatchString(key) || strings.Contains(key, "..") {
		return fmt.Errorf("memory key must match %s and must not contain '..'", memoryKeyPattern)
	}
	return nil
}

func validateText(label, value string, limit int, allowEmpty bool) error {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s must be valid UTF-8 without NUL bytes", label)
	}
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d-byte limit", label, limit)
	}
	return nil
}

func yamlScalar(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
