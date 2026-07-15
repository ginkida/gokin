package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"gokin/internal/fileutil"
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
	byKey         map[string]string // scope+Key -> ID; scopes never overwrite each other
	// writeBlockedPaths guards a scope whose unreadable/corrupt source could not
	// be backed up. Saving an empty in-memory map over that file would destroy
	// the only recoverable bytes. Populated only during construction, then read
	// immutably by saveFile.
	writeBlockedPaths map[string]error

	// Debounced write support
	dirty     bool        // Whether there are unsaved changes
	saveTimer *time.Timer // Timer for debounced save
	saveMu    sync.Mutex  // Protects saveTimer

	// ioMu serializes the actual disk-write phase of a debounced save against
	// Flush(). Timer.Stop() cannot cancel a callback that has already started
	// running — the debounce callback clears `dirty` BEFORE doing its disk
	// I/O (see scheduleSave), so without this, Flush() can acquire s.mu right
	// after the callback releases it, observe dirty==false, and return "nothing
	// to do" while the callback's AtomicWrite calls are still in flight. A
	// caller treating a nil-error Flush() as "safe to exit" (e.g. graceful
	// shutdown) could then terminate before that write completes — silent
	// data loss in exactly the window Flush() exists to close.
	ioMu sync.Mutex

	// GetForContext cache: invalidated on any mutation
	contextCache map[string]contextCacheEntry // keyed by "project"/"all"
	cacheVersion uint64                       // incremented on every mutation

	mu sync.RWMutex
}

type contextCacheEntry struct {
	content    string
	validUntil time.Time
}

// A memory JSON file is user-controlled persistent input. Bound it before
// decoding so a truncated/corrupt file cannot force an unbounded allocation at
// startup. Oversized files are quarantined intact rather than read into RAM.
const maxMemoryStoreFileBytes int64 = 64 << 20

// NewStore creates a new memory store.
func NewStore(configDir, projectPath string, maxEntries int) (*Store, error) {
	// Keyed memory may contain credentials, private preferences, and excerpts
	// copied from source code. Keep its namespace owner-only and repair older
	// installs that created this directory as 0755.
	memDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create memory directory: %w", err)
	}
	if err := os.Chmod(memDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to secure memory directory: %w", err)
	}

	// Generate project hash
	projectHash := hashPath(projectPath)

	store := &Store{
		configDir:         configDir,
		projectPath:       projectPath,
		projectHash:       projectHash,
		maxEntries:        maxEntries,
		entries:           make(map[string]*Entry),
		globalEntries:     make(map[string]*Entry),
		archived:          make(map[string]*Entry),
		globalArchive:     make(map[string]*Entry),
		byKey:             make(map[string]string),
		writeBlockedPaths: make(map[string]error),
	}
	// The 0700 parent already blocks traversal, but also repair the known JSON
	// files themselves for defense in depth (and for copies/backups that lose
	// the parent directory mode). Missing files are created owner-only on save.
	for _, path := range []string{
		store.storagePath(),
		store.globalStoragePath(),
		store.archiveStoragePath(),
		store.globalArchivePath(),
	} {
		if err := os.Chmod(path, 0600); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to secure memory file %s: %w", path, err)
		}
	}

	// Load existing entries
	if err := store.load(); err != nil {
		// Each scope is loaded independently. Never discard a valid global store
		// merely because this project's JSON is corrupt (or vice versa).
		logging.Warn("memory store loaded with partial failures", "error", err)
	}

	return store, nil
}

// markDirty marks the store as dirty and invalidates the GetForContext cache.
// Must be called under s.mu.Lock().
func (s *Store) markDirty() {
	s.dirty = true
	s.invalidateContextLocked()
}

// invalidateContextLocked advances the observable memory revision without
// necessarily scheduling another write. Housekeeping performed by Flush or a
// debounce callback already belongs to an in-flight write, but it still changes
// what prompt builders may safely serve from cache.
func (s *Store) invalidateContextLocked() {
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

func cloneEntry(entry *Entry) *Entry {
	if entry == nil {
		return nil
	}
	copyEntry := *entry
	copyEntry.Tags = append([]string(nil), entry.Tags...)
	return &copyEntry
}

// memoryKeyIndex keeps session/project/global namespaces independent. A
// temporary session note with key "verification" may shadow project/global
// lookup for the current conversation, but must never delete either durable
// entry from memory or disk.
func memoryKeyIndex(memType MemoryType, key string) string {
	return string(memType) + "\x00" + key
}

func (s *Store) indexEntryKeyLocked(entry *Entry) {
	if entry != nil && entry.Key != "" {
		s.byKey[memoryKeyIndex(entry.Type, entry.Key)] = entry.ID
	}
}

func (s *Store) removeEntryKeyLocked(entry *Entry) {
	if entry == nil || entry.Key == "" {
		return
	}
	index := memoryKeyIndex(entry.Type, entry.Key)
	if s.byKey[index] == entry.ID {
		delete(s.byKey, index)
	}
	// Compatibility for tests/embedders that populated the pre-namespaced
	// in-memory index directly. byKey is never persisted, so new stores only
	// write the scoped form above.
	if s.byKey[entry.Key] == entry.ID {
		delete(s.byKey, entry.Key)
	}
}

// resolveMemoryKeyLocked applies intentional shadowing without destructive
// replacement: current-session knowledge wins, then project, then global.
func (s *Store) resolveMemoryKeyLocked(key string) (string, bool) {
	for _, memType := range []MemoryType{MemorySession, MemoryProject, MemoryGlobal} {
		if id, ok := s.byKey[memoryKeyIndex(memType, key)]; ok {
			return id, true
		}
	}
	return "", false
}

// resolveActiveMemoryKeyLocked is the read-side variant of
// resolveMemoryKeyLocked. An expired temporary override must not make a
// durable project/global value disappear until the next housekeeping pass.
func (s *Store) resolveActiveMemoryKeyLocked(key string, now time.Time) (string, bool) {
	for _, memType := range []MemoryType{MemorySession, MemoryProject, MemoryGlobal} {
		id, ok := s.byKey[memoryKeyIndex(memType, key)]
		if !ok {
			continue
		}
		entry, ok := s.entries[id]
		if !ok {
			entry, ok = s.globalEntries[id]
		}
		if !ok || entry == nil || entry.Archived || entry.IsExpired(now) {
			continue
		}
		return id, true
	}
	return "", false
}

const (
	staleArchiveWindow = 90 * 24 * time.Hour
	maxContextMemories = 12
	// Per-turn retrieval is intentionally smaller than the always-on hot set:
	// it travels with the current user message and should add signal, not another
	// copy of the whole memory database.
	maxRelevantContextMemories = 6
	maxMemoryQueryRunes        = 2400
)

// memoryRetrievalStopWords removes conversational glue from automatic recall.
// Without this filter, a request such as "please continue the work" could rank
// an unrelated note merely because it also contains "work". Explicit keys and
// tags still match even when they are short (for example "go" or "db").
var memoryRetrievalStopWords = map[string]struct{}{
	"and": {}, "are": {}, "can": {}, "could": {}, "for": {}, "from": {},
	"continue": {}, "how": {}, "please": {}, "that": {}, "the": {}, "this": {},
	"what": {}, "when": {}, "where": {}, "why": {}, "with": {}, "work": {}, "would": {},
	"была": {}, "было": {}, "все": {}, "где": {}, "его": {}, "для": {},
	"или": {}, "как": {}, "когда": {}, "можешь": {}, "надо": {}, "нужно": {},
	"она": {}, "они": {}, "почему": {}, "при": {}, "продолжи": {}, "само": {},
	"сделай": {}, "теперь": {}, "что": {}, "чтобы": {}, "это": {},
}

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

func boundMemoryQuery(query string) string {
	runes := []rune(strings.TrimSpace(query))
	if len(runes) <= maxMemoryQueryRunes {
		return string(runes)
	}
	// User intent normally appears before an attachment or at the end after a
	// pasted excerpt. Retain both ends while preventing a 1M-context payload
	// from being tokenized once per stored memory.
	const tailRunes = 800
	headRunes := maxMemoryQueryRunes - tailRunes
	return string(runes[:headRunes]) + "\n…\n" + string(runes[len(runes)-tailRunes:])
}

func tokenSetFromText(s string) map[string]struct{} {
	set := make(map[string]struct{})
	for w := range strings.FieldsSeq(normalizeMemoryText(s)) {
		// Count characters, not UTF-8 bytes: two-letter Russian glue words are
		// otherwise treated as 4-byte "meaningful" terms and cause noisy recall.
		if len([]rune(w)) < 3 {
			continue
		}
		set[w] = struct{}{}
	}
	return set
}

func retrievalTokenSetFromText(s string) map[string]struct{} {
	set := tokenSetFromText(s)
	for word := range set {
		if _, stop := memoryRetrievalStopWords[word]; stop {
			delete(set, word)
			continue
		}
		// Keep the full path/key token, but also index its components so a
		// query for "deploy" can match a key such as "deploy-order".
		for _, part := range strings.FieldsFunc(word, func(r rune) bool {
			return r == '/' || r == '_' || r == '-' || r == '.'
		}) {
			if len([]rune(part)) < 3 {
				continue
			}
			if _, stop := memoryRetrievalStopWords[part]; !stop {
				set[part] = struct{}{}
			}
		}
	}
	return set
}

func retrievalTokensMatch(queryToken, candidateToken string) bool {
	if queryToken == candidateToken {
		return true
	}
	// A conservative prefix match covers common morphology and concise keys
	// (deploy/deploys, auth/authentication) without making tiny fragments match.
	if len([]rune(queryToken)) < 4 || len([]rune(candidateToken)) < 4 {
		return false
	}
	return strings.HasPrefix(queryToken, candidateToken) || strings.HasPrefix(candidateToken, queryToken)
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
	_, err := s.AddResolved(entry)
	return err
}

// AddResolved adds or reinforces a memory and returns the canonical stored
// entry. The distinction matters for semantic deduplication: callers must not
// expose the incoming entry's new ID when an older entry was reinforced instead.
func (s *Store) AddResolved(entry *Entry) (*Entry, error) {
	if entry == nil {
		return nil, fmt.Errorf("cannot add nil memory entry")
	}
	// Store owns every pointer reachable from its maps. Keeping the caller's
	// Entry (or Tags backing array) lets an innocent post-Add mutation race
	// persistence, retrieval, and feedback updates.
	entry = cloneEntry(entry)
	switch entry.Type {
	case MemorySession, MemoryProject, MemoryGlobal:
	default:
		return nil, fmt.Errorf("cannot add memory entry with unknown type %q", entry.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.activeMutationBlockedLocked(entry.Type); err != nil {
		return nil, err
	}

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
			if existing.Key != "" && existing.Key != entry.Key {
				s.removeEntryKeyLocked(existing)
			}
			index := memoryKeyIndex(entry.Type, entry.Key)
			if oldID, exists := s.byKey[index]; exists && oldID != existing.ID {
				delete(s.entries, oldID)
				delete(s.globalEntries, oldID)
			}
			existing.Key = entry.Key
			s.indexEntryKeyLocked(existing)
		}
		existing.Archived = false
		existing.ArchiveReason = ""
		s.markDirty()
		s.scheduleSave()
		return cloneEntry(existing), nil
	}

	// If key exists, remove old ID from both stores and index
	if entry.Key != "" {
		index := memoryKeyIndex(entry.Type, entry.Key)
		if oldID, ok := s.byKey[index]; ok {
			delete(s.entries, oldID)
			delete(s.globalEntries, oldID)
			delete(s.archived, oldID)
			delete(s.globalArchive, oldID)
		}
		s.byKey[index] = entry.ID
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

	// Each trust/lifecycle scope has its own quota. A project mutation must
	// never evict cross-project global knowledge, and ephemeral session notes
	// must never evict durable project facts.
	if s.maxEntries > 0 {
		s.pruneOldest()
	}

	// Mark dirty and schedule debounced save
	s.markDirty()
	s.scheduleSave()
	return cloneEntry(entry), nil
}

// Get retrieves an entry by key.
func (s *Store) Get(key string) (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()

	id, ok := s.resolveActiveMemoryKeyLocked(key, now)
	if !ok {
		return nil, false
	}

	if entry, ok := s.entries[id]; ok {
		if entry.IsExpired(now) {
			return nil, false
		}
		return cloneEntry(entry), true
	}
	entry, ok := s.globalEntries[id]
	if ok && entry.IsExpired(now) {
		return nil, false
	}
	return cloneEntry(entry), ok
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
		return cloneEntry(entry), true
	}
	entry, ok := s.globalEntries[id]
	if ok && entry.IsExpired(now) {
		return nil, false
	}
	return cloneEntry(entry), ok
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
	if err := s.activeMutationBlockedLocked(entry.Type); err != nil {
		logging.Warn("memory access metadata update blocked", "id", id, "error", err)
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
	if s.activeMutationBlockedLocked(entry.Type) != nil {
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
	if err := s.activeMutationBlockedLocked(entry.Type); err != nil {
		return err
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

	queryText := strings.TrimSpace(query.Query)
	if queryText == "" {
		score += 1.0
	} else {
		score += memoryQueryRelevance(entry, queryText)
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

// memoryQueryRelevance returns lexical relevance without freshness or usage
// boosts. A zero score means the entry has no meaningful relationship to the
// query and must not be returned merely because it is recent or frequently used.
func memoryQueryRelevance(entry *Entry, query string) float64 {
	if entry == nil {
		return 0
	}

	queryNorm := normalizeMemoryText(query)
	if queryNorm == "" {
		return 0
	}

	keyNorm := normalizeMemoryText(entry.Key)
	tagsNorm := normalizeMemoryText(strings.Join(entry.Tags, " "))
	contentNorm := normalizeMemoryText(entry.Content)
	queryRunes := []rune(queryNorm)
	score := 0.0

	if keyNorm != "" {
		switch {
		case keyNorm == queryNorm:
			score += 12.0
		case len(queryRunes) >= 3 && (strings.Contains(keyNorm, queryNorm) || strings.Contains(queryNorm, keyNorm)):
			score += 6.0
		}
	}
	for _, tag := range entry.Tags {
		tagNorm := normalizeMemoryText(tag)
		if tagNorm == queryNorm {
			score += 5.0
		} else if tagNorm != "" && len(queryRunes) >= 3 && strings.Contains(tagNorm, queryNorm) {
			score += 2.5
		}
	}

	queryTokens := retrievalTokenSetFromText(queryNorm)
	// Substring matching is useful for paths and precise phrases, but only for
	// meaningful queries; short/common glue words otherwise produce noisy hits.
	if len(queryTokens) > 0 && len(queryRunes) >= 3 && strings.Contains(contentNorm, queryNorm) {
		score += 5.0
	}

	if len(queryTokens) == 0 {
		return score
	}

	contentTokens := retrievalTokenSetFromText(contentNorm)
	keyTokens := retrievalTokenSetFromText(keyNorm)
	tagTokens := retrievalTokenSetFromText(tagsNorm)
	overlap := func(candidate map[string]struct{}) int {
		n := 0
		for queryToken := range queryTokens {
			matched := false
			for candidateToken := range candidate {
				if retrievalTokensMatch(queryToken, candidateToken) {
					matched = true
					break
				}
			}
			if matched {
				n++
			}
		}
		return n
	}

	contentOverlap := overlap(contentTokens)
	keyOverlap := overlap(keyTokens)
	tagOverlap := overlap(tagTokens)
	score += 6.0 * float64(contentOverlap) / float64(len(queryTokens))
	score += 4.0 * float64(keyOverlap) / float64(len(queryTokens))
	score += 3.0 * float64(tagOverlap) / float64(len(queryTokens))
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

// Search finds entries matching the query. A non-empty text query requires at
// least one meaningful key/tag/content match; recency and usage only rank real
// matches and can never pull unrelated memories into recall. Results are then
// sorted by score, recency, and stable ID.
func (s *Store) Search(query SearchQuery) []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Set current project for filtering
	query.Project = s.projectHash
	query.Query = boundMemoryQuery(query.Query)
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
		if strings.TrimSpace(query.Query) != "" && memoryQueryRelevance(entry, query.Query) == 0 {
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
			if strings.TrimSpace(query.Query) != "" && memoryQueryRelevance(entry, query.Query) == 0 {
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
			if strings.TrimSpace(query.Query) != "" && memoryQueryRelevance(entry, query.Query) == 0 {
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
				if strings.TrimSpace(query.Query) != "" && memoryQueryRelevance(entry, query.Query) == 0 {
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
		if !scored[i].entry.Timestamp.Equal(scored[j].entry.Timestamp) {
			return scored[i].entry.Timestamp.After(scored[j].entry.Timestamp)
		}
		return scored[i].entry.ID < scored[j].entry.ID
	})

	// Extract entries from scored results
	results := make([]*Entry, len(scored))
	for i, se := range scored {
		results[i] = cloneEntry(se.entry)
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
					if id, ok := s.resolveMemoryKeyLocked(idOrKey); ok {
						actualID = id
					} else {
						return false
					}
				}
			}
			if id, ok := s.resolveMemoryKeyLocked(idOrKey); ok {
				actualID = id
			}
		}
	}

	if entry, ok := s.entries[actualID]; ok {
		if s.activeMutationBlockedLocked(entry.Type) != nil {
			return false
		}
		delete(s.entries, actualID)
		s.removeEntryKeyLocked(entry)
		s.markDirty()
		s.scheduleSave()
		return true
	}
	if entry, ok := s.globalEntries[actualID]; ok {
		if s.activeMutationBlockedLocked(entry.Type) != nil {
			return false
		}
		delete(s.globalEntries, actualID)
		s.removeEntryKeyLocked(entry)
		s.markDirty()
		s.scheduleSave()
		return true
	}
	if entry, ok := s.archived[actualID]; ok {
		if s.archiveMutationBlockedLocked(entry.Type) != nil {
			return false
		}
		delete(s.archived, actualID)
		s.removeEntryKeyLocked(entry)
		s.markDirty()
		s.scheduleSave()
		return true
	}
	if entry, ok := s.globalArchive[actualID]; ok {
		if s.archiveMutationBlockedLocked(entry.Type) != nil {
			return false
		}
		delete(s.globalArchive, actualID)
		s.removeEntryKeyLocked(entry)
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
		results = append(results, cloneEntry(entry))
	}
	for _, entry := range s.globalEntries {
		if entry.IsExpired(now) {
			continue
		}
		results = append(results, cloneEntry(entry))
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.After(results[j].Timestamp)
	})

	return results
}

// GetReport returns a human-readable summary of stored memories.
func (s *Store) GetReport() string {
	entries := s.ListAll()
	if len(entries) == 0 {
		return "No memories stored. Use the `memory` tool to save and recall information."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "**Stored Memories** (%d total)\n\n", len(entries))

	// Group by scope
	var project, global, session []*Entry
	for _, e := range entries {
		switch e.Type {
		case MemoryProject:
			project = append(project, e)
		case MemoryGlobal:
			global = append(global, e)
		case MemorySession:
			session = append(session, e)
		}
	}

	writeGroup := func(title string, items []*Entry) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&sb, "### %s (%d)\n", title, len(items))
		for _, e := range items {
			age := time.Since(e.Timestamp)
			ageStr := formatAge(age)
			content := e.Content
			if runes := []rune(content); len(runes) > 80 {
				content = string(runes[:77]) + "..."
			}
			if e.Key != "" {
				fmt.Fprintf(&sb, "- **%s**: %s (%s)\n", e.Key, content, ageStr)
			} else {
				fmt.Fprintf(&sb, "- %s (%s)\n", content, ageStr)
			}
		}
		sb.WriteString("\n")
	}

	writeGroup("Project", project)
	writeGroup("Global", global)
	writeGroup("Session", session)

	return sb.String()
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
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
	for i, entry := range entries {
		if entry == nil {
			return fmt.Errorf("invalid import entry %d: entry is null", i)
		}
		if strings.TrimSpace(entry.ID) == "" {
			return fmt.Errorf("invalid import entry %d: id is required", i)
		}
		switch entry.Type {
		case MemorySession, MemoryProject, MemoryGlobal:
		default:
			return fmt.Errorf("invalid import entry %d: unknown memory type %q", i, entry.Type)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, entry := range entries {
		if err := s.activeMutationBlockedLocked(entry.Type); err != nil {
			return err
		}
	}

	for _, entry := range entries {
		// Skip duplicates by ID
		if _, ok := s.entries[entry.ID]; ok {
			continue
		}
		if _, ok := s.globalEntries[entry.ID]; ok {
			continue
		}

		if entry.Type == MemoryGlobal {
			entry.Project = ""
			s.globalEntries[entry.ID] = entry
		} else {
			// Export/import moves project knowledge into the destination
			// workspace. Retaining the source hash either made the entry
			// invisible to project-only recall or leaked foreign project data
			// when callers requested project+global context.
			entry.Project = s.projectHash
			s.entries[entry.ID] = entry
		}

		s.indexEntryKeyLocked(entry)
	}

	s.markDirty()
	s.scheduleSave()
	return nil
}

// GetForContext returns a formatted string of memories for injection into prompts.
// Results are cached and invalidated when the store is mutated.
func (s *Store) GetForContext(projectOnly bool) string {
	content, _, _ := s.GetForContextSnapshot(projectOnly)
	return content
}

// GetForContextSnapshot returns content together with the exact mutation
// revision and earliest TTL deadline used to produce it. Prompt-level caches
// use this atomic snapshot contract so they cannot label old content with a
// newer revision captured after a concurrent mutation.
func (s *Store) GetForContextSnapshot(projectOnly bool) (string, uint64, time.Time) {
	cacheKey := "all"
	if projectOnly {
		cacheKey = "project"
	}

	// Check the cache and, on a miss, capture owned entries and their state in
	// one read-side critical section.
	s.mu.RLock()
	now := time.Now()
	ver := s.cacheVersion
	if s.contextCache != nil {
		if cached, ok := s.contextCache[cacheKey]; ok {
			if cached.validUntil.IsZero() || now.Before(cached.validUntil) {
				s.mu.RUnlock()
				return cached.content, ver, cached.validUntil
			}
		}
	}
	entries := make([]*Entry, 0, len(s.entries)+len(s.globalEntries))
	var validUntil time.Time
	s.forEachVisibleContextEntryLocked(projectOnly, now, func(entry *Entry) {
		entries = append(entries, cloneEntry(entry))
		if !entry.ExpiresAt.IsZero() && (validUntil.IsZero() || entry.ExpiresAt.Before(validUntil)) {
			validUntil = entry.ExpiresAt
		}
	})
	s.mu.RUnlock()

	if len(entries) == 0 {
		return "", ver, validUntil
	}

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
		if !scored[i].entry.Timestamp.Equal(scored[j].entry.Timestamp) {
			return scored[i].entry.Timestamp.After(scored[j].entry.Timestamp)
		}
		return scored[i].entry.ID < scored[j].entry.ID
	})
	if len(scored) > maxContextMemories {
		scored = scored[:maxContextMemories]
	}

	selected := make([]*Entry, 0, len(scored))
	for _, item := range scored {
		selected = append(selected, item.entry)
	}

	result := formatMemoryContext("Memory", selected)

	// Store in cache only if no mutations happened during computation
	s.mu.Lock()
	if s.cacheVersion == ver {
		if s.contextCache == nil {
			s.contextCache = make(map[string]contextCacheEntry, 2)
		}
		s.contextCache[cacheKey] = contextCacheEntry{content: result, validUntil: validUntil}
	}
	s.mu.Unlock()

	return result, ver, validUntil
}

// MemoryContextState is the cheap compare side of GetForContextSnapshot. Its
// revision changes on every Store mutation; validUntil represents the next
// time passage alone can change visible context (for example when a session
// override expires and reveals the project value beneath it).
func (s *Store) MemoryContextState(projectOnly bool) (uint64, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	var validUntil time.Time
	s.forEachVisibleContextEntryLocked(projectOnly, now, func(entry *Entry) {
		if !entry.ExpiresAt.IsZero() && (validUntil.IsZero() || entry.ExpiresAt.Before(validUntil)) {
			validUntil = entry.ExpiresAt
		}
	})
	return s.cacheVersion, validUntil
}

// forEachVisibleContextEntryLocked applies the same non-destructive shadowing
// contract as Get: session wins over project, which wins over global. Only the
// winning value for a key is exposed to the model, avoiding contradictory
// instructions while retaining lower scopes for when an override is cleared.
// Caller must hold s.mu for reading.
func (s *Store) forEachVisibleContextEntryLocked(projectOnly bool, now time.Time, visit func(*Entry)) {
	seenKeys := make(map[string]struct{})
	visitScope := func(memType MemoryType, source map[string]*Entry) {
		for _, entry := range source {
			if entry == nil || entry.Type != memType || entry.Archived || entry.IsExpired(now) {
				continue
			}
			if memType != MemoryGlobal && entry.Project != "" && entry.Project != s.projectHash {
				continue
			}
			if entry.Key != "" {
				// Ignore a stale same-scope record when the canonical key index
				// points elsewhere (possible after importing legacy data).
				if id, ok := s.byKey[memoryKeyIndex(memType, entry.Key)]; ok && id != entry.ID {
					continue
				}
				if _, shadowed := seenKeys[entry.Key]; shadowed {
					continue
				}
				seenKeys[entry.Key] = struct{}{}
			}
			visit(entry)
		}
	}

	visitScope(MemorySession, s.entries)
	visitScope(MemoryProject, s.entries)
	if !projectOnly {
		visitScope(MemoryGlobal, s.globalEntries)
	}
}

// GetRelevantForContext returns a small query-aware memory block for the
// current turn. Unlike GetForContext's generic hot set, this method excludes
// entries with zero lexical relevance; freshness and prior success may order
// genuine matches but can never make an unrelated note appear relevant.
//
// The formatted snapshot is built while the read lock is held. Store entries
// are mutable pointers, so formatting Search() results after its lock is
// released would otherwise race a concurrent feedback/edit operation.
func (s *Store) GetRelevantForContext(query string, projectOnly bool, limit int) string {
	query = boundMemoryQuery(query)
	if query == "" {
		return ""
	}
	if limit <= 0 || limit > maxRelevantContextMemories {
		limit = maxRelevantContextMemories
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	scored := make([]contextScoredEntry, 0, limit)
	consider := func(entry *Entry) {
		relevance := memoryQueryRelevance(entry, query)
		if relevance <= 0 {
			return
		}
		// Relevance dominates; the hot-set score is only a quality/freshness
		// tiebreaker among memories that actually match this request.
		scored = append(scored, contextScoredEntry{
			entry: entry,
			score: relevance*10 + scoreEntryForContext(entry, now),
		})
	}

	s.forEachVisibleContextEntryLocked(projectOnly, now, consider)

	if len(scored) == 0 {
		return ""
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if !scored[i].entry.Timestamp.Equal(scored[j].entry.Timestamp) {
			return scored[i].entry.Timestamp.After(scored[j].entry.Timestamp)
		}
		return scored[i].entry.ID < scored[j].entry.ID
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}

	selected := make([]*Entry, 0, len(scored))
	for _, item := range scored {
		selected = append(selected, item.entry)
	}
	return formatMemoryContext("Relevant Memory for This Turn", selected)
}

func formatMemoryContext(title string, entries []*Entry) string {
	if len(entries) == 0 {
		return ""
	}

	byType := make(map[MemoryType][]*Entry)
	for _, entry := range entries {
		if entry != nil {
			byType[entry.Type] = append(byType[entry.Type], entry)
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "## %s\n\n", title)
	types := []struct {
		t     MemoryType
		label string
	}{
		{MemorySession, "Current Session"},
		{MemoryProject, "Project Knowledge"},
		{MemoryGlobal, "User Preferences"},
	}
	for _, tc := range types {
		items := byType[tc.t]
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "### %s\n", tc.label)
		for _, entry := range items {
			content := strings.TrimSpace(entry.Content)
			if runes := []rune(content); len(runes) > 220 {
				content = string(runes[:220]) + "..."
			}
			if entry.Key != "" {
				fmt.Fprintf(&builder, "- **%s**: %s\n", entry.Key, content)
			} else {
				fmt.Fprintf(&builder, "- %s\n", content)
			}
		}
		builder.WriteString("\n")
	}
	return builder.String()
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
	for _, path := range []string{
		s.storagePath(),
		s.globalStoragePath(),
		s.archiveStoragePath(),
		s.globalArchivePath(),
	} {
		if err := s.mutationBlockedForPathLocked(path); err != nil {
			return err
		}
	}

	s.entries = make(map[string]*Entry)
	s.globalEntries = make(map[string]*Entry)
	s.archived = make(map[string]*Entry)
	s.globalArchive = make(map[string]*Entry)
	s.byKey = make(map[string]string)
	s.markDirty()
	s.scheduleSave()
	return nil
}

// ClearSession removes every session-scoped entry while preserving project and
// user knowledge. It is the keyed-memory half of a conversation clean slate;
// session entries are intentionally not persisted, but without this method they
// would still survive /clear for the rest of the process.
func (s *Store) ClearSession() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	clearFrom := func(entries map[string]*Entry) {
		for id, entry := range entries {
			if entry == nil || entry.Type != MemorySession {
				continue
			}
			delete(entries, id)
			s.removeEntryKeyLocked(entry)
			removed++
		}
	}
	clearFrom(s.entries)
	clearFrom(s.globalEntries)
	clearFrom(s.archived)
	clearFrom(s.globalArchive)
	if removed > 0 {
		s.markDirty()
		s.scheduleSave()
	}
	return removed
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

func (s *Store) activeStoragePathForType(memType MemoryType) string {
	switch memType {
	case MemoryProject:
		return s.storagePath()
	case MemoryGlobal:
		return s.globalStoragePath()
	default:
		// Session memory is intentionally process-local and has no file whose
		// failed load could be overwritten.
		return ""
	}
}

func (s *Store) archiveStoragePathForType(memType MemoryType) string {
	if memType == MemoryGlobal {
		return s.globalArchivePath()
	}
	if memType == MemoryProject {
		return s.archiveStoragePath()
	}
	return ""
}

func (s *Store) mutationBlockedForPathLocked(path string) error {
	if path == "" {
		return nil
	}
	if loadErr, blocked := s.writeBlockedPaths[path]; blocked {
		return fmt.Errorf("refusing to mutate memory scope whose source could not be loaded or backed up: %w", loadErr)
	}
	return nil
}

func (s *Store) activeMutationBlockedLocked(memType MemoryType) error {
	return s.mutationBlockedForPathLocked(s.activeStoragePathForType(memType))
}

func (s *Store) archiveMutationBlockedLocked(memType MemoryType) error {
	return s.mutationBlockedForPathLocked(s.archiveStoragePathForType(memType))
}

// load loads entries from disk.
func (s *Store) load() error {
	loads := []struct {
		name         string
		path         string
		target       map[string]*Entry
		expectedType MemoryType
		archived     bool
	}{
		{name: "project", path: s.storagePath(), target: s.entries, expectedType: MemoryProject},
		{name: "global", path: s.globalStoragePath(), target: s.globalEntries, expectedType: MemoryGlobal},
		{name: "project archive", path: s.archiveStoragePath(), target: s.archived, expectedType: MemoryProject, archived: true},
		{name: "global archive", path: s.globalArchivePath(), target: s.globalArchive, expectedType: MemoryGlobal, archived: true},
	}
	var failures []string
	for _, load := range loads {
		if err := s.loadFile(load.path, load.target, load.expectedType, load.archived); err != nil {
			failures = append(failures, fmt.Sprintf("%s memory (%s): %v", load.name, load.path, err))
		}
	}

	// Update byKey index
	for _, entry := range s.entries {
		s.indexEntryKeyLocked(entry)
	}
	for _, entry := range s.globalEntries {
		s.indexEntryKeyLocked(entry)
	}

	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func (s *Store) loadFile(path string, target map[string]*Entry, expectedType MemoryType, archived bool) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// No bytes were available to back up. Block this exact scope from writes
		// rather than treating it as empty and destroying an unreadable source.
		s.writeBlockedPaths[path] = err
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		s.writeBlockedPaths[path] = err
		return err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		err := fmt.Errorf("memory store path is not a regular file (mode %s)", info.Mode())
		s.writeBlockedPaths[path] = err
		return err
	}
	if info.Size() > maxMemoryStoreFileBytes {
		if err := file.Close(); err != nil {
			s.writeBlockedPaths[path] = err
			return fmt.Errorf("close oversized memory file: %w", err)
		}
		return s.quarantineOversizedMemoryFile(path, info.Size())
	}

	// The Stat check avoids allocating for already-large regular files. The
	// limit additionally closes the race where a file grows after Stat.
	data, readErr := io.ReadAll(io.LimitReader(file, maxMemoryStoreFileBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		s.writeBlockedPaths[path] = readErr
		return fmt.Errorf("read memory file: %w", readErr)
	}
	if closeErr != nil {
		s.writeBlockedPaths[path] = closeErr
		return fmt.Errorf("close memory file: %w", closeErr)
	}
	if int64(len(data)) > maxMemoryStoreFileBytes {
		return s.quarantineOversizedMemoryFile(path, int64(len(data)))
	}

	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return s.preserveInvalidMemoryFile(path, data, err)
	}

	// Validate into a temporary map first. A mixed valid/invalid JSON array must
	// not partially populate memory; otherwise a later Flush would serialize an
	// incomplete scope and silently discard the rejected tail.
	loaded := make(map[string]*Entry, len(entries))
	keys := make(map[string]struct{})
	for i, entry := range entries {
		if entry == nil {
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d is null", i))
		}
		if strings.TrimSpace(entry.ID) == "" {
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d has empty id", i))
		}
		switch entry.Type {
		case MemorySession, MemoryProject, MemoryGlobal:
		default:
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d (%s) has unknown type %q", i, entry.ID, entry.Type))
		}
		if entry.Type != expectedType {
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d (%s) has type %q, want %q for this file", i, entry.ID, entry.Type, expectedType))
		}
		if entry.Archived != archived {
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d (%s) archived=%v, want %v for this file", i, entry.ID, entry.Archived, archived))
		}
		if expectedType == MemoryProject {
			if entry.Project == "" {
				entry.Project = s.projectHash
			} else if entry.Project != s.projectHash {
				return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d (%s) belongs to another project", i, entry.ID))
			}
		} else if entry.Project != "" {
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d (%s) is global but has project binding", i, entry.ID))
		}
		if _, duplicate := loaded[entry.ID]; duplicate {
			return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d repeats id %q", i, entry.ID))
		}
		if entry.Key != "" {
			keyIndex := memoryKeyIndex(entry.Type, entry.Key)
			if _, duplicate := keys[keyIndex]; duplicate {
				return s.preserveInvalidMemoryFile(path, data, fmt.Errorf("entry %d repeats key %q", i, entry.Key))
			}
			keys[keyIndex] = struct{}{}
		}
		loaded[entry.ID] = entry
	}
	for id, entry := range loaded {
		target[id] = entry
	}
	return nil
}

func (s *Store) quarantineOversizedMemoryFile(path string, observedSize int64) error {
	cause := fmt.Errorf("memory file is too large: %d bytes exceeds %d-byte limit", observedSize, maxMemoryStoreFileBytes)
	// NewStore already repairs known files to 0600, but enforce it here too so
	// the quarantine remains private if loadFile is reused independently.
	if err := os.Chmod(path, 0600); err != nil {
		s.writeBlockedPaths[path] = fmt.Errorf("%v; secure quarantine source: %w", cause, err)
		return s.writeBlockedPaths[path]
	}
	backupPath := fmt.Sprintf("%s.oversized-%d-%d.bak", path, observedSize, time.Now().UnixNano())
	if err := os.Rename(path, backupPath); err != nil {
		s.writeBlockedPaths[path] = fmt.Errorf("%v; quarantine failed: %w", cause, err)
		return s.writeBlockedPaths[path]
	}
	return fmt.Errorf("%w (original file quarantined intact at %s)", cause, backupPath)
}

func corruptMemoryBackupPath(path string, data []byte) string {
	digest := sha256.Sum256(data)
	return path + ".corrupt-" + hex.EncodeToString(digest[:8]) + ".bak"
}

// preserveInvalidMemoryFile creates an owner-only, content-addressed backup
// before this scope is ever allowed to be rewritten from its empty in-memory
// representation. If backup fails, saveFile blocks only this exact scope while
// healthy scopes remain usable.
func (s *Store) preserveInvalidMemoryFile(path string, data []byte, cause error) error {
	backupPath := corruptMemoryBackupPath(path, data)
	if err := fileutil.AtomicWrite(backupPath, data, 0600); err != nil {
		s.writeBlockedPaths[path] = fmt.Errorf("%v; backup failed: %w", cause, err)
		return fmt.Errorf("%v; refusing future writes because backup failed: %w", cause, err)
	}
	return fmt.Errorf("%w (original bytes preserved at %s)", cause, backupPath)
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
		// Session notes and durable project knowledge share s.entries for
		// lookup, but they must never share a deduplication identity. If a
		// project fact reinforces an equal session note, the canonical entry
		// remains MemorySession and is deliberately omitted from save(), so
		// the user's attempt to make that knowledge durable is lost at exit.
		if existing.Type != entry.Type {
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
	s.removeEntryKeyLocked(entry)
	entry.Archived = true
	entry.ArchiveReason = reason
	s.invalidateContextLocked()
	if entry.Type == MemoryGlobal {
		s.globalArchive[entry.ID] = entry
	} else {
		s.archived[entry.ID] = entry
	}
}

func (s *Store) archiveExpiredLocked(now time.Time) {
	for id, entry := range s.entries {
		if entry == nil || !entry.IsExpired(now) {
			continue
		}
		if entry.Type == MemorySession {
			// Session memory is deliberately process-local. Archiving it made
			// expired session notes durable and caused the strict project archive
			// loader to quarantine a file written by Store itself.
			delete(s.entries, id)
			s.removeEntryKeyLocked(entry)
			s.invalidateContextLocked()
			continue
		}
		if s.activeMutationBlockedLocked(entry.Type) != nil || s.archiveMutationBlockedLocked(entry.Type) != nil {
			continue
		}
		delete(s.entries, id)
		s.archiveEntryLocked(entry, "ttl_expired")
	}
	for id, entry := range s.globalEntries {
		if entry == nil || !entry.IsExpired(now) {
			continue
		}
		if s.activeMutationBlockedLocked(entry.Type) != nil || s.archiveMutationBlockedLocked(entry.Type) != nil {
			continue
		}
		delete(s.globalEntries, id)
		s.archiveEntryLocked(entry, "ttl_expired")
	}
}

func (s *Store) archiveStaleLowValueLocked(now time.Time) {
	archiveIfStale := func(id string, entry *Entry, from map[string]*Entry) {
		if entry == nil || entry.Type == MemorySession {
			return
		}
		if s.activeMutationBlockedLocked(entry.Type) != nil || s.archiveMutationBlockedLocked(entry.Type) != nil {
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
	cleanup := func(store map[string]*Entry, memType MemoryType) {
		if s.archiveMutationBlockedLocked(memType) != nil {
			return
		}
		for id, entry := range store {
			if entry == nil {
				delete(store, id)
				continue
			}
			if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt.Add(30*24*time.Hour)) {
				s.removeEntryKeyLocked(entry)
				delete(store, id)
				continue
			}
			if now.Sub(entry.Timestamp) > 365*24*time.Hour {
				s.removeEntryKeyLocked(entry)
				delete(store, id)
			}
		}
	}
	cleanup(s.archived, MemoryProject)
	cleanup(s.globalArchive, MemoryGlobal)
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
		if entry != nil && entry.Type == MemoryProject {
			projectArchive = append(projectArchive, entry)
		}
	}
	if err := s.saveFile(s.archiveStoragePath(), projectArchive); err != nil {
		return err
	}

	globalArchive := make([]*Entry, 0, len(s.globalArchive))
	for _, entry := range s.globalArchive {
		if entry != nil && entry.Type == MemoryGlobal {
			globalArchive = append(globalArchive, entry)
		}
	}
	return s.saveFile(s.globalArchivePath(), globalArchive)
}

func (s *Store) saveFile(path string, entries []*Entry) error {
	if _, blocked := s.writeBlockedPaths[path]; blocked {
		// Mutating APIs reject changes to this exact scope synchronously. A
		// healthy project/global sibling must still be flushable, so leave the
		// blocked source untouched instead of failing an unrelated save.
		return nil
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0600)
}

// pruneOldest enforces maxEntries independently for each lifecycle/trust
// scope. Project, session, and global memories must never evict one another.
func (s *Store) pruneOldest() {
	if s.maxEntries <= 0 {
		return
	}
	s.pruneOldestFrom(s.entries, MemoryProject)
	s.pruneOldestFrom(s.entries, MemorySession)
	s.pruneOldestFrom(s.globalEntries, MemoryGlobal)
}

func (s *Store) pruneOldestFrom(entries map[string]*Entry, memType MemoryType) {
	candidates := make([]*Entry, 0, len(entries))
	for _, entry := range entries {
		if entry != nil && entry.Type == memType {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) <= s.maxEntries {
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].Timestamp.Equal(candidates[j].Timestamp) {
			return candidates[i].Timestamp.Before(candidates[j].Timestamp)
		}
		return candidates[i].ID < candidates[j].ID
	})

	toRemove := len(candidates) - s.maxEntries
	for i := range toRemove {
		entry := candidates[i]
		delete(entries, entry.ID)
		s.removeEntryKeyLocked(entry)
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

// saveDebounceInterval is the debounce window before scheduleSave's timer
// fires. A package var (not a const) so tests can shorten it to drive the
// debounced-save path deterministically instead of waiting out the real
// 2s window.
var saveDebounceInterval = 2 * time.Second

// saveIOHookForTest, when non-nil, is invoked at the start of the debounced
// save's disk-write phase — after ioMu is acquired, before any file write.
// Test-only seam for deterministically widening the write-phase race window
// (see store_flush_race_test.go). The seam lock prevents an old timer callback
// from racing a test cleanup that restores these package-level values.
var (
	saveTestSeamMu    sync.RWMutex
	saveIOHookForTest func()
)

// scheduleSave schedules a debounced save operation.
// Multiple calls within saveDebounceInterval will be coalesced into a single save.
func (s *Store) scheduleSave() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	// Cancel existing timer if any
	if s.saveTimer != nil {
		s.saveTimer.Stop()
	}

	// Schedule new save after the debounce window. Defer-recover guards
	// against a panic in JSON marshal / file write — the timer fires on its
	// own goroutine, so an unrecovered panic would crash the whole process.
	saveTestSeamMu.RLock()
	debounceInterval := saveDebounceInterval
	saveTestSeamMu.RUnlock()
	s.saveTimer = time.AfterFunc(debounceInterval, func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("memory store save timer panicked", "panic", r)
			}
		}()
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
				projectEntries = append(projectEntries, cloneEntry(entry))
			}
		}
		globalEntries := make([]*Entry, 0, len(s.globalEntries))
		for _, entry := range s.globalEntries {
			if entry != nil && entry.Type == MemoryGlobal {
				globalEntries = append(globalEntries, cloneEntry(entry))
			}
		}
		projectArchive := make([]*Entry, 0, len(s.archived))
		for _, entry := range s.archived {
			if entry != nil && entry.Type == MemoryProject {
				projectArchive = append(projectArchive, cloneEntry(entry))
			}
		}
		globalArchive := make([]*Entry, 0, len(s.globalArchive))
		for _, entry := range s.globalArchive {
			if entry != nil && entry.Type == MemoryGlobal {
				globalArchive = append(globalArchive, cloneEntry(entry))
			}
		}
		s.dirty = false
		s.mu.Unlock()

		// Serialize the disk-write phase against Flush() — see the ioMu field
		// doc. Held across the writes AND the error-handling re-dirty below,
		// so Flush() (which also acquires ioMu before checking s.dirty) can
		// never observe a transient dirty==false while these writes are
		// still in flight or about to be retried.
		s.ioMu.Lock()
		defer s.ioMu.Unlock()
		saveTestSeamMu.RLock()
		saveHook := saveIOHookForTest
		saveTestSeamMu.RUnlock()
		if saveHook != nil {
			saveHook()
		}

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

	// Wait for any in-flight debounced save's disk-write phase to finish
	// before checking dirty. Timer.Stop() above cannot cancel a callback
	// that has already started running, and that callback clears `dirty`
	// BEFORE it finishes writing to disk (see scheduleSave) — without this,
	// Flush() could acquire s.mu right after the callback releases it,
	// observe dirty==false, and return "nothing to do" while the callback's
	// AtomicWrite calls are still in flight.
	s.ioMu.Lock()
	defer s.ioMu.Unlock()

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
