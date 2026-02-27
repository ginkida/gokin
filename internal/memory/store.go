package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"gokin/internal/logging"
)

// Store manages persistent memory storage.
type Store struct {
	configDir   string
	projectPath string
	projectHash string
	maxEntries  int

	entries       map[string]*Entry // ID -> Entry (Project & Session)
	globalEntries map[string]*Entry // ID -> Global Entry
	archived      map[string]*Entry // ID -> Archived project/session entries
	globalArchive map[string]*Entry // ID -> Archived global entries
	byKey         map[string]string // Key -> ID (All types)

	// Debounced write support
	dirty     bool        // Whether there are unsaved changes
	saveTimer *time.Timer // Timer for debounced save
	saveMu    sync.Mutex  // Protects saveTimer

	// GetForContext cache: invalidated on any mutation
	contextCache map[string]string // keyed by "project"/"all"
	cacheVersion uint64            // incremented on every mutation

	mu sync.RWMutex
}

// NewStore creates a new memory store.
func NewStore(configDir, projectPath string, maxEntries int) (*Store, error) {
	// Create memory directory
	memDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create memory directory: %w", err)
	}

	// Generate project hash
	projectHash := hashPath(projectPath)

	store := &Store{
		configDir:     configDir,
		projectPath:   projectPath,
		projectHash:   projectHash,
		maxEntries:    maxEntries,
		entries:       make(map[string]*Entry),
		globalEntries: make(map[string]*Entry),
		archived:      make(map[string]*Entry),
		globalArchive: make(map[string]*Entry),
		byKey:         make(map[string]string),
	}

	// Load existing entries
	if err := store.load(); err != nil {
		// Non-fatal - start fresh if load fails
		store.entries = make(map[string]*Entry)
		store.globalEntries = make(map[string]*Entry)
		store.archived = make(map[string]*Entry)
		store.globalArchive = make(map[string]*Entry)
		store.byKey = make(map[string]string)
	}

	return store, nil
}

// markDirty marks the store as dirty and invalidates the GetForContext cache.
// Must be called under s.mu.Lock().
func (s *Store) markDirty() {
	s.dirty = true
	s.cacheVersion++
	s.contextCache = nil
}

// Auto-tagging regex patterns.
var (
	reFilePath    = regexp.MustCompile(`(?:^|\s)(/[a-zA-Z0-9_.\-/]+)`)
	reFuncName    = regexp.MustCompile(`(?:func|function)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	rePackageName = regexp.MustCompile(`package\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
)

// extractContentTags extracts key concepts from content and returns them as tags.
func extractContentTags(content string) []string {
	seen := make(map[string]bool)
	var tags []string

	addTag := func(tag string) {
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}

	// Extract file paths
	for _, match := range reFilePath.FindAllStringSubmatch(content, -1) {
		addTag(match[1])
	}

	// Extract function names
	for _, match := range reFuncName.FindAllStringSubmatch(content, -1) {
		addTag(match[1])
	}

	// Extract package names
	for _, match := range rePackageName.FindAllStringSubmatch(content, -1) {
		addTag(match[1])
	}

	return tags
}

// autoTag merges extracted tags into the entry, deduplicating with existing tags.
func autoTag(entry *Entry) {
	extracted := extractContentTags(entry.Content)
	if len(extracted) == 0 {
		return
	}

	seen := make(map[string]bool)
	for _, t := range entry.Tags {
		seen[t] = true
	}
	for _, t := range extracted {
		if !seen[t] {
			entry.Tags = append(entry.Tags, t)
			seen[t] = true
		}
	}
}

const (
	staleArchiveWindow = 90 * 24 * time.Hour
	maxContextMemories = 12
)

func normalizeMemoryText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func tokenSetFromText(s string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, w := range strings.Fields(normalizeMemoryText(s)) {
		if len(w) < 3 {
			continue
		}
		set[w] = struct{}{}
	}
	return set
}

func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	intersection := 0
	union := len(a)
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		if _, ok := a[k]; ok {
			intersection++
		}
		if _, ok := seen[k]; !ok {
			union++
			seen[k] = struct{}{}
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func mergeTags(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, t := range existing {
		normalized := strings.ToLower(strings.TrimSpace(t))
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, t)
	}
	for _, t := range incoming {
		normalized := strings.ToLower(strings.TrimSpace(t))
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, t)
	}
	return out
}

// Add adds a new entry to the store.
func (s *Store) Add(entry *Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Housekeeping keeps hot set small and removes expired entries.
	s.archiveExpiredLocked(now)
	s.archiveStaleLowValueLocked(now)
	s.cleanupArchivedLocked(now)

	if entry.Timestamp.IsZero() {
		entry.Timestamp = now
	}

	// Auto-tag: extract key concepts from content
	autoTag(entry)

	// Semantic dedup: reinforce existing memories instead of creating near-duplicates.
	if existing, ok := s.findSemanticDuplicateLocked(entry); ok {
		existing.Reinforcement++
		existing.Timestamp = now
		existing.Tags = mergeTags(existing.Tags, entry.Tags)
		if entry.Key != "" {
			if oldID, exists := s.byKey[entry.Key]; exists && oldID != existing.ID {
				delete(s.entries, oldID)
				delete(s.globalEntries, oldID)
			}
			existing.Key = entry.Key
			s.byKey[entry.Key] = existing.ID
		}
		existing.Archived = false
		existing.ArchiveReason = ""
		s.markDirty()
		s.scheduleSave()
		return nil
	}

	// If key exists, remove old ID from both stores and index
	if entry.Key != "" {
		if oldID, ok := s.byKey[entry.Key]; ok {
			delete(s.entries, oldID)
			delete(s.globalEntries, oldID)
			delete(s.archived, oldID)
			delete(s.globalArchive, oldID)
		}
		s.byKey[entry.Key] = entry.ID
	}

	// Determine storage
	if entry.Type == MemoryGlobal {
		s.globalEntries[entry.ID] = entry
	} else {
		// Set project if not already set (for project/session)
		if entry.Project == "" {
			entry.Project = s.projectHash
		}
		s.entries[entry.ID] = entry
	}

	// Enforce max entries limit (total across project and global)
	if s.maxEntries > 0 && (len(s.entries)+len(s.globalEntries)) > s.maxEntries {
		s.pruneOldest()
	}

	// Mark dirty and schedule debounced save
	s.markDirty()
	s.scheduleSave()
	return nil
}

// Get retrieves an entry by key.
func (s *Store) Get(key string) (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()

	id, ok := s.byKey[key]
	if !ok {
		return nil, false
	}

	if entry, ok := s.entries[id]; ok {
		if entry.IsExpired(now) {
			return nil, false
		}
		return entry, true
	}
	entry, ok := s.globalEntries[id]
	if ok && entry.IsExpired(now) {
		return nil, false
	}
	return entry, ok
}

// GetByID retrieves an entry by ID.
func (s *Store) GetByID(id string) (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()

	if entry, ok := s.entries[id]; ok {
		if entry.IsExpired(now) {
			return nil, false
		}
		return entry, true
	}
	entry, ok := s.globalEntries[id]
	if ok && entry.IsExpired(now) {
		return nil, false
	}
	return entry, ok
}

// RecordAccess marks an entry as accessed to improve ranking based on usage.
func (s *Store) RecordAccess(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		entry, ok = s.globalEntries[id]
	}
	if !ok {
		return
	}
	entry.RecordAccess()
	s.markDirty()
	s.scheduleSave()
}

// RecordFeedback updates retrieval success/failure metrics for an entry.
func (s *Store) RecordFeedback(id string, success bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		entry, ok = s.globalEntries[id]
	}
	if !ok {
		return false
	}

	entry.RecordOutcome(success)
	s.markDirty()
	s.scheduleSave()
	return true
}

// Edit updates the content of an existing entry by ID, re-runs auto-tagging,
// and marks the store dirty.
func (s *Store) Edit(id string, newContent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		entry, ok = s.globalEntries[id]
	}
	if !ok {
		return fmt.Errorf("entry not found: %s", id)
	}

	entry.Content = newContent
	// Reset tags and re-run auto-tagging
	entry.Tags = nil
	autoTag(entry)

	s.markDirty()
	s.scheduleSave()
	return nil
}

// scoredEntry holds an entry and its relevance score for search ranking.
type scoredEntry struct {
	entry *Entry
	score float64
}

// contextScoredEntry represents a memory item ranked for prompt injection.
type contextScoredEntry struct {
	entry *Entry
	score float64
}

// scoreEntry calculates retrieval score from relevance + freshness + prior success.
func scoreEntry(entry *Entry, query SearchQuery, now time.Time) float64 {
	score := 0.0

	queryText := strings.TrimSpace(strings.ToLower(query.Query))
	if queryText == "" {
		score += 1.0
	}

	if queryText != "" {
		if strings.EqualFold(entry.Key, query.Query) {
			score += 12.0
		} else if entry.Key != "" && strings.Contains(strings.ToLower(entry.Key), queryText) {
			score += 5.0
		}

		for _, tag := range entry.Tags {
			if strings.EqualFold(tag, query.Query) {
				score += 3.5
			}
		}

		contentNorm := normalizeMemoryText(entry.Content)
		queryNorm := normalizeMemoryText(query.Query)
		if strings.Contains(contentNorm, queryNorm) {
			score += 4.0
		}

		queryTokens := tokenSetFromText(query.Query)
		contentTokens := tokenSetFromText(entry.Content)
		tagTokens := tokenSetFromText(strings.Join(entry.Tags, " "))

		score += 6.0 * jaccardSimilarity(queryTokens, contentTokens)
		score += 2.0 * jaccardSimilarity(queryTokens, tagTokens)
	}

	ageHours := now.Sub(entry.Timestamp).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	// Half-life style freshness decay (recent memories get a small boost).
	freshness := math.Exp(-ageHours / (24 * 14))
	score += 2.5 * freshness

	// Retrieval quality from prior outcomes.
	score += 3.0 * entry.SuccessRate()
	score += 0.35 * math.Log1p(float64(entry.Reinforcement))
	score += 0.25 * math.Log1p(float64(entry.AccessCount))

	return score
}

// scoreEntryForContext ranks memories for prompt injection.
// It prioritizes recency + demonstrated usefulness + reinforced facts.
func scoreEntryForContext(entry *Entry, now time.Time) float64 {
	if entry == nil {
		return 0
	}

	ageHours := now.Sub(entry.Timestamp).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	freshness := math.Exp(-ageHours / (24 * 21))

	accessHours := ageHours
	if !entry.LastAccessed.IsZero() {
		accessHours = now.Sub(entry.LastAccessed).Hours()
		if accessHours < 0 {
			accessHours = 0
		}
	}
	accessFreshness := math.Exp(-accessHours / (24 * 30))

	score := 0.0
	score += 4.0 * freshness
	score += 3.0 * entry.SuccessRate()
	score += 0.40 * math.Log1p(float64(entry.Reinforcement))
	score += 0.30 * math.Log1p(float64(entry.AccessCount))
	score += 1.0 * accessFreshness

	if strings.TrimSpace(entry.Key) != "" {
		score += 0.6
	}
	if len(entry.Tags) > 0 {
		score += 0.2 * math.Log1p(float64(len(entry.Tags)))
	}

	return score
}

// Search finds entries matching the query.
// Results are scored by relevance: exact key match = 10, tag match = 5,
// content substring = 1. Results are sorted by score descending, then by
// timestamp descending as a tiebreaker.
func (s *Store) Search(query SearchQuery) []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Set current project for filtering
	query.Project = s.projectHash
	now := time.Now()

	var scored []scoredEntry

	// Search project and session entries
	for _, entry := range s.entries {
		if entry.IsExpired(now) {
			continue
		}
		if !entry.Matches(query) {
			continue
		}
		sc := scoreEntry(entry, query, now)
		if sc > 0 {
			scored = append(scored, scoredEntry{entry: entry, score: sc})
		}
	}

	// Search global entries (unless ProjectOnly is specified)
	if !query.ProjectOnly {
		for _, entry := range s.globalEntries {
			if entry.IsExpired(now) {
				continue
			}
			if !entry.Matches(query) {
				continue
			}
			sc := scoreEntry(entry, query, now)
			if sc > 0 {
				scored = append(scored, scoredEntry{entry: entry, score: sc})
			}
		}
	}

	if query.IncludeArchived {
		for _, entry := range s.archived {
			if !entry.Matches(query) {
				continue
			}
			sc := scoreEntry(entry, query, now) * 0.65 // Archived entries are lower priority.
			if sc > 0 {
				scored = append(scored, scoredEntry{entry: entry, score: sc})
			}
		}
		if !query.ProjectOnly {
			for _, entry := range s.globalArchive {
				if !entry.Matches(query) {
					continue
				}
				sc := scoreEntry(entry, query, now) * 0.65
				if sc > 0 {
					scored = append(scored, scoredEntry{entry: entry, score: sc})
				}
			}
		}
	}

	// Sort by score descending, then by timestamp descending
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].entry.Timestamp.After(scored[j].entry.Timestamp)
	})

	// Extract entries from scored results
	results := make([]*Entry, len(scored))
	for i, se := range scored {
		results[i] = se.entry
	}

	// Apply limit
	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}

	return results
}

// Remove removes an entry by ID or key.
func (s *Store) Remove(idOrKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Resolve key to ID if needed (avoids recursive Lock which would deadlock)
	actualID := idOrKey
	if _, ok := s.entries[idOrKey]; !ok {
		if _, ok := s.globalEntries[idOrKey]; !ok {
			if _, ok := s.archived[idOrKey]; !ok {
				if _, ok := s.globalArchive[idOrKey]; !ok {
					if id, ok := s.byKey[idOrKey]; ok {
						actualID = id
					} else {
						return false
					}
				}
			}
			if id, ok := s.byKey[idOrKey]; ok {
				actualID = id
			}
		}
	}

	if entry, ok := s.entries[actualID]; ok {
		delete(s.entries, actualID)
		if entry.Key != "" {
			delete(s.byKey, entry.Key)
		}
		s.markDirty()
		s.scheduleSave()
		return true
	}
	if entry, ok := s.globalEntries[actualID]; ok {
		delete(s.globalEntries, actualID)
		if entry.Key != "" {
			delete(s.byKey, entry.Key)
		}
		s.markDirty()
		s.scheduleSave()
		return true
	}
	if entry, ok := s.archived[actualID]; ok {
		delete(s.archived, actualID)
		if entry.Key != "" {
			delete(s.byKey, entry.Key)
		}
		s.markDirty()
		s.scheduleSave()
		return true
	}
	if entry, ok := s.globalArchive[actualID]; ok {
		delete(s.globalArchive, actualID)
		if entry.Key != "" {
			delete(s.byKey, entry.Key)
		}
		s.markDirty()
		s.scheduleSave()
		return true
	}

	return false
}

// List returns entries for the current project (optionally project-only).
func (s *Store) List(projectOnly bool) []*Entry {
	return s.Search(SearchQuery{
		ProjectOnly: projectOnly,
		Project:     s.projectHash,
	})
}

// ListAll returns all entries (project + global) sorted by timestamp, newest first.
func (s *Store) ListAll() []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()

	results := make([]*Entry, 0, len(s.entries)+len(s.globalEntries))
	for _, entry := range s.entries {
		if entry.IsExpired(now) {
			continue
		}
		results = append(results, entry)
	}
	for _, entry := range s.globalEntries {
		if entry.IsExpired(now) {
			continue
		}
		results = append(results, entry)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.After(results[j].Timestamp)
	})

	return results
}

// Export serializes all entries (project + global) to JSON.
func (s *Store) Export() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make([]*Entry, 0, len(s.entries)+len(s.globalEntries))
	for _, entry := range s.entries {
		all = append(all, entry)
	}
	for _, entry := range s.globalEntries {
		all = append(all, entry)
	}

	return json.MarshalIndent(all, "", "  ")
}

// Import deserializes entries from JSON and merges them into the store.
// Duplicate entries (by ID) are skipped.
func (s *Store) Import(data []byte) error {
	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("failed to unmarshal import data: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, entry := range entries {
		// Skip duplicates by ID
		if _, ok := s.entries[entry.ID]; ok {
			continue
		}
		if _, ok := s.globalEntries[entry.ID]; ok {
			continue
		}

		if entry.Type == MemoryGlobal {
			s.globalEntries[entry.ID] = entry
		} else {
			s.entries[entry.ID] = entry
		}

		if entry.Key != "" {
			s.byKey[entry.Key] = entry.ID
		}
	}

	s.markDirty()
	s.scheduleSave()
	return nil
}

// GetForContext returns a formatted string of memories for injection into prompts.
// Results are cached and invalidated when the store is mutated.
func (s *Store) GetForContext(projectOnly bool) string {
	cacheKey := "all"
	if projectOnly {
		cacheKey = "project"
	}

	// Check cache under read lock
	s.mu.RLock()
	ver := s.cacheVersion
	if s.contextCache != nil {
		if cached, ok := s.contextCache[cacheKey]; ok {
			s.mu.RUnlock()
			return cached
		}
	}
	s.mu.RUnlock()

	// Cache miss — compute (List/Search acquires its own RLock)
	entries := s.List(projectOnly)
	if len(entries) == 0 {
		return ""
	}

	now := time.Now()
	scored := make([]contextScoredEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		scored = append(scored, contextScoredEntry{
			entry: entry,
			score: scoreEntryForContext(entry, now),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].entry.Timestamp.After(scored[j].entry.Timestamp)
	})
	if len(scored) > maxContextMemories {
		scored = scored[:maxContextMemories]
	}

	selected := make([]*Entry, 0, len(scored))
	for _, item := range scored {
		selected = append(selected, item.entry)
	}

	// Group by type
	byType := make(map[MemoryType][]*Entry)
	for _, entry := range selected {
		byType[entry.Type] = append(byType[entry.Type], entry)
	}

	var builder strings.Builder
	builder.WriteString("## Memory\n\n")

	types := []struct {
		t     MemoryType
		label string
	}{
		{MemorySession, "Current Session"},
		{MemoryProject, "Project Knowledge"},
		{MemoryGlobal, "User Preferences"},
	}

	for _, tc := range types {
		if items, ok := byType[tc.t]; ok && len(items) > 0 {
			builder.WriteString(fmt.Sprintf("### %s\n", tc.label))
			for _, entry := range items {
				content := strings.TrimSpace(entry.Content)
				if len(content) > 220 {
					content = content[:220] + "..."
				}
				if entry.Key != "" {
					builder.WriteString(fmt.Sprintf("- **%s**: %s\n", entry.Key, content))
				} else {
					builder.WriteString(fmt.Sprintf("- %s\n", content))
				}
			}
			builder.WriteString("\n")
		}
	}

	result := builder.String()

	// Store in cache only if no mutations happened during computation
	s.mu.Lock()
	if s.cacheVersion == ver {
		if s.contextCache == nil {
			s.contextCache = make(map[string]string, 2)
		}
		s.contextCache[cacheKey] = result
	}
	s.mu.Unlock()

	return result
}

// Count returns the number of entries.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries) + len(s.globalEntries)
}

// Clear removes all entries.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = make(map[string]*Entry)
	s.globalEntries = make(map[string]*Entry)
	s.archived = make(map[string]*Entry)
	s.globalArchive = make(map[string]*Entry)
	s.byKey = make(map[string]string)
	s.markDirty()
	s.scheduleSave()
	return nil
}

// storagePath returns the path to the memory file for the current project.
func (s *Store) storagePath() string {
	return filepath.Join(s.configDir, "memory", s.projectHash+".json")
}

// globalStoragePath returns the path to the global memory file.
func (s *Store) globalStoragePath() string {
	return filepath.Join(s.configDir, "memory", "global.json")
}

// archiveStoragePath returns the path to archived memory entries for current project.
func (s *Store) archiveStoragePath() string {
	return filepath.Join(s.configDir, "memory", s.projectHash+".archive.json")
}

// globalArchivePath returns the path to archived global memory entries.
func (s *Store) globalArchivePath() string {
	return filepath.Join(s.configDir, "memory", "global.archive.json")
}

// load loads entries from disk.
func (s *Store) load() error {
	// Load project entries
	if err := s.loadFile(s.storagePath(), s.entries); err != nil {
		return err
	}
	// Load global entries
	if err := s.loadFile(s.globalStoragePath(), s.globalEntries); err != nil {
		return err
	}
	// Load archived entries
	if err := s.loadFile(s.archiveStoragePath(), s.archived); err != nil {
		return err
	}
	if err := s.loadFile(s.globalArchivePath(), s.globalArchive); err != nil {
		return err
	}

	// Update byKey index
	for _, entry := range s.entries {
		if entry.Key != "" {
			s.byKey[entry.Key] = entry.ID
		}
	}
	for _, entry := range s.globalEntries {
		if entry.Key != "" {
			s.byKey[entry.Key] = entry.ID
		}
	}

	return nil
}

func (s *Store) loadFile(path string, target map[string]*Entry) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}

	for _, entry := range entries {
		target[entry.ID] = entry
	}
	return nil
}

// findSemanticDuplicateLocked returns an existing active entry that is a semantic
// near-duplicate of the provided entry. Must be called with s.mu held.
func (s *Store) findSemanticDuplicateLocked(entry *Entry) (*Entry, bool) {
	if entry == nil || strings.TrimSpace(entry.Content) == "" {
		return nil, false
	}

	candidates := s.entries
	if entry.Type == MemoryGlobal {
		candidates = s.globalEntries
	}

	normalizedNew := normalizeMemoryText(entry.Content)
	newTokens := tokenSetFromText(entry.Content)

	for _, existing := range candidates {
		if existing == nil || existing.Archived {
			continue
		}
		if entry.Type != MemoryGlobal && existing.Project != "" && existing.Project != s.projectHash {
			continue
		}
		normalizedExisting := normalizeMemoryText(existing.Content)
		if normalizedExisting == normalizedNew {
			return existing, true
		}
		existingTokens := tokenSetFromText(existing.Content)
		if sim := jaccardSimilarity(newTokens, existingTokens); sim >= 0.90 {
			return existing, true
		}
	}

	return nil, false
}

func (s *Store) archiveEntryLocked(entry *Entry, reason string) {
	if entry == nil {
		return
	}
	if entry.Key != "" {
		delete(s.byKey, entry.Key)
	}
	entry.Archived = true
	entry.ArchiveReason = reason
	if entry.Type == MemoryGlobal {
		s.globalArchive[entry.ID] = entry
	} else {
		s.archived[entry.ID] = entry
	}
}

func (s *Store) archiveExpiredLocked(now time.Time) {
	for id, entry := range s.entries {
		if entry != nil && entry.IsExpired(now) {
			delete(s.entries, id)
			s.archiveEntryLocked(entry, "ttl_expired")
		}
	}
	for id, entry := range s.globalEntries {
		if entry != nil && entry.IsExpired(now) {
			delete(s.globalEntries, id)
			s.archiveEntryLocked(entry, "ttl_expired")
		}
	}
}

func (s *Store) archiveStaleLowValueLocked(now time.Time) {
	archiveIfStale := func(id string, entry *Entry, from map[string]*Entry) {
		if entry == nil || entry.Type == MemorySession {
			return
		}
		if now.Sub(entry.Timestamp) < staleArchiveWindow {
			return
		}
		if entry.Reinforcement > 0 || entry.AccessCount > 0 || entry.SuccessCount > 0 {
			return
		}
		delete(from, id)
		s.archiveEntryLocked(entry, "stale_low_value")
	}

	for id, entry := range s.entries {
		archiveIfStale(id, entry, s.entries)
	}
	for id, entry := range s.globalEntries {
		archiveIfStale(id, entry, s.globalEntries)
	}
}

func (s *Store) cleanupArchivedLocked(now time.Time) {
	cleanup := func(store map[string]*Entry) {
		for id, entry := range store {
			if entry == nil {
				delete(store, id)
				continue
			}
			if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt.Add(30*24*time.Hour)) {
				if entry.Key != "" {
					delete(s.byKey, entry.Key)
				}
				delete(store, id)
				continue
			}
			if now.Sub(entry.Timestamp) > 365*24*time.Hour {
				if entry.Key != "" {
					delete(s.byKey, entry.Key)
				}
				delete(store, id)
			}
		}
	}
	cleanup(s.archived)
	cleanup(s.globalArchive)
}

// save persists entries to disk.
func (s *Store) save() error {
	now := time.Now()
	s.archiveExpiredLocked(now)
	s.archiveStaleLowValueLocked(now)
	s.cleanupArchivedLocked(now)

	// Save project entries (filter out session entries)
	projectEntries := make([]*Entry, 0)
	for _, entry := range s.entries {
		if entry.Type == MemoryProject {
			projectEntries = append(projectEntries, entry)
		}
	}
	if err := s.saveFile(s.storagePath(), projectEntries); err != nil {
		return err
	}

	// Save global entries
	globalEntries := make([]*Entry, 0, len(s.globalEntries))
	for _, entry := range s.globalEntries {
		globalEntries = append(globalEntries, entry)
	}
	if err := s.saveFile(s.globalStoragePath(), globalEntries); err != nil {
		return err
	}

	projectArchive := make([]*Entry, 0, len(s.archived))
	for _, entry := range s.archived {
		projectArchive = append(projectArchive, entry)
	}
	if err := s.saveFile(s.archiveStoragePath(), projectArchive); err != nil {
		return err
	}

	globalArchive := make([]*Entry, 0, len(s.globalArchive))
	for _, entry := range s.globalArchive {
		globalArchive = append(globalArchive, entry)
	}
	return s.saveFile(s.globalArchivePath(), globalArchive)
}

func (s *Store) saveFile(path string, entries []*Entry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// pruneOldest removes the oldest entries to stay within limit.
// Considers both project entries and global entries when pruning.
func (s *Store) pruneOldest() {
	totalCount := len(s.entries) + len(s.globalEntries)
	if s.maxEntries <= 0 || totalCount <= s.maxEntries {
		return
	}

	// Collect all entries from both stores
	all := make([]*Entry, 0, totalCount)
	for _, entry := range s.entries {
		all = append(all, entry)
	}
	for _, entry := range s.globalEntries {
		all = append(all, entry)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	// Remove oldest entries from the appropriate store
	toRemove := totalCount - s.maxEntries
	for i := 0; i < toRemove; i++ {
		entry := all[i]
		delete(s.entries, entry.ID)
		delete(s.globalEntries, entry.ID)
		if entry.Key != "" {
			delete(s.byKey, entry.Key)
		}
	}
}

// hashPath generates a deterministic hash for a file path.
func hashPath(path string) string {
	normalized := path
	if abs, err := filepath.Abs(path); err == nil {
		normalized = abs
	}
	if resolved, err := filepath.EvalSymlinks(normalized); err == nil {
		normalized = resolved
	}
	normalized = filepath.Clean(normalized)

	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:8])
}

// scheduleSave schedules a debounced save operation.
// Multiple calls within 2 seconds will be coalesced into a single save.
func (s *Store) scheduleSave() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	// Cancel existing timer if any
	if s.saveTimer != nil {
		s.saveTimer.Stop()
	}

	// Schedule new save after 2 seconds
	s.saveTimer = time.AfterFunc(2*time.Second, func() {
		s.mu.Lock()
		if !s.dirty {
			s.mu.Unlock()
			return
		}

		now := time.Now()
		s.archiveExpiredLocked(now)
		s.archiveStaleLowValueLocked(now)
		s.cleanupArchivedLocked(now)

		// Snapshot entry lists under lock
		projectEntries := make([]*Entry, 0)
		for _, entry := range s.entries {
			if entry.Type == MemoryProject {
				projectEntries = append(projectEntries, entry)
			}
		}
		globalEntries := make([]*Entry, 0, len(s.globalEntries))
		for _, entry := range s.globalEntries {
			globalEntries = append(globalEntries, entry)
		}
		projectArchive := make([]*Entry, 0, len(s.archived))
		for _, entry := range s.archived {
			projectArchive = append(projectArchive, entry)
		}
		globalArchive := make([]*Entry, 0, len(s.globalArchive))
		for _, entry := range s.globalArchive {
			globalArchive = append(globalArchive, entry)
		}
		s.dirty = false
		s.mu.Unlock()

		// Save outside lock — disk I/O no longer blocks readers/writers.
		projectErr := s.saveFile(s.storagePath(), projectEntries)
		globalErr := s.saveFile(s.globalStoragePath(), globalEntries)
		projectArchiveErr := s.saveFile(s.archiveStoragePath(), projectArchive)
		globalArchiveErr := s.saveFile(s.globalArchivePath(), globalArchive)
		if projectErr != nil || globalErr != nil || projectArchiveErr != nil || globalArchiveErr != nil {
			logging.Warn("failed to save memory store",
				"project_error", projectErr,
				"global_error", globalErr,
				"project_archive_error", projectArchiveErr,
				"global_archive_error", globalArchiveErr)
			s.mu.Lock()
			s.markDirty()
			s.mu.Unlock()
		}
	})
}

// Flush forces an immediate save of any pending changes.
// Should be called during shutdown to ensure data is persisted.
func (s *Store) Flush() error {
	s.saveMu.Lock()
	if s.saveTimer != nil {
		s.saveTimer.Stop()
		s.saveTimer = nil
	}
	s.saveMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}

	err := s.save()
	if err == nil {
		s.dirty = false
	}
	return err
}
