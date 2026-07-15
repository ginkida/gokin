package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// MaxInvocationSourceBytes bounds persisted provenance labels. Built-in
	// sources are short (for example "project" and "claude-global").
	MaxInvocationSourceBytes = 256
	// MaxInvocationPathBytes is above normal platform path limits while keeping
	// an untrusted restored snapshot from retaining unbounded metadata.
	MaxInvocationPathBytes = 4096
	// MaxInvocationRestoreEntries bounds validation work for an untrusted
	// persisted payload while leaving room for historical duplicate snapshots.
	MaxInvocationRestoreEntries = MaxSkillCount * 8
)

// Invocation is the immutable latest rendered snapshot for one normalized
// skill invocation key. RenderHash is the lowercase hexadecimal SHA-256 of
// Rendered.
type Invocation struct {
	Name       string `json:"name"`
	Rendered   string `json:"rendered"`
	RenderHash string `json:"render_hash"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	Sequence   uint64 `json:"sequence"`
}

// InvocationLedger records the latest distinct rendered skill payload per
// invocation name. Its zero value is ready for use.
type InvocationLedger struct {
	mu           sync.RWMutex
	latest       map[string]Invocation
	nextSequence uint64
}

// NewInvocationLedger returns an empty invocation ledger.
func NewInvocationLedger() *InvocationLedger {
	return &InvocationLedger{
		latest:       make(map[string]Invocation),
		nextSequence: 1,
	}
}

// Record stores rendered as the latest immutable snapshot for name. Sequence
// advances only for a new name or a changed render hash. Re-recording identical
// rendered content returns the existing snapshot and changed=false; provenance
// changes alone do not rewrite that immutable snapshot.
func (l *InvocationLedger) Record(name, rendered, source, path string) (Invocation, bool, error) {
	normalized, err := validateInvocationFields(name, rendered, source, path)
	if err != nil {
		return Invocation{}, false, err
	}
	hash := renderHash(rendered)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureInitializedLocked()

	if existing, ok := l.latest[normalized]; ok && existing.RenderHash == hash {
		return cloneInvocation(existing), false, nil
	}
	if _, exists := l.latest[normalized]; !exists && len(l.latest) >= MaxSkillCount {
		l.evictOldestLocked()
	}
	sequence := l.allocateSequenceLocked()
	invocation := Invocation{
		Name:       normalized,
		Rendered:   strings.Clone(rendered),
		RenderHash: hash,
		Source:     strings.Clone(source),
		Path:       strings.Clone(path),
		Sequence:   sequence,
	}
	l.latest[normalized] = invocation
	return cloneInvocation(invocation), true, nil
}

// SnapshotNewestFirst returns a deep copy sorted by descending sequence. Name
// is the deterministic ascending tie-breaker for restored corrupt histories
// that reuse a sequence.
func (l *InvocationLedger) SnapshotNewestFirst() []Invocation {
	l.mu.RLock()
	result := make([]Invocation, 0, len(l.latest))
	for _, invocation := range l.latest {
		result = append(result, cloneInvocation(invocation))
	}
	l.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Sequence != result[j].Sequence {
			return result[i].Sequence > result[j].Sequence
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// Restore atomically replaces the ledger from untrusted persisted entries.
// Invalid entries are skipped and returned as an aggregate error; valid entries
// are normalized and restored. For each normalized name, the highest-sequence
// valid render wins, with deterministic hash/provenance tie-breaking. If the
// persisted maximum sequence cannot be incremented safely, active entries are
// compactly resequenced while preserving their deterministic order.
func (l *InvocationLedger) Restore(entries []Invocation) error {
	if len(entries) > MaxInvocationRestoreEntries {
		return fmt.Errorf("invocation restore has %d entries; limit is %d", len(entries), MaxInvocationRestoreEntries)
	}

	next := make(map[string]Invocation)
	var validationErrors []error
	for i, candidate := range entries {
		normalized, err := validateInvocationFields(candidate.Name, candidate.Rendered, candidate.Source, candidate.Path)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("invocation[%d]: %w", i, err))
			continue
		}
		if candidate.Sequence == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("invocation[%d]: sequence must be greater than zero", i))
			continue
		}
		expectedHash := renderHash(candidate.Rendered)
		if len(candidate.RenderHash) != sha256.Size*2 {
			validationErrors = append(validationErrors, fmt.Errorf("invocation[%d]: render_hash must be a 64-character SHA-256", i))
			continue
		}
		decodedHash, err := hex.DecodeString(candidate.RenderHash)
		if err != nil || len(decodedHash) != sha256.Size || !strings.EqualFold(candidate.RenderHash, expectedHash) {
			validationErrors = append(validationErrors, fmt.Errorf("invocation[%d]: render_hash does not match rendered content", i))
			continue
		}
		candidate.Name = normalized
		candidate.RenderHash = expectedHash
		candidate = cloneInvocation(candidate)
		if existing, ok := next[normalized]; !ok || invocationIsNewer(candidate, existing) {
			next[normalized] = candidate
		}
	}

	if len(next) > MaxSkillCount {
		ordered := invocationValuesNewestFirst(next)
		bounded := make(map[string]Invocation, MaxSkillCount)
		for _, invocation := range ordered[:MaxSkillCount] {
			bounded[invocation.Name] = invocation
		}
		next = bounded
	}

	nextSequence := uint64(1)
	var maxSequence uint64
	for _, invocation := range next {
		if invocation.Sequence > maxSequence {
			maxSequence = invocation.Sequence
		}
	}
	if len(next) > 0 {
		if maxSequence == math.MaxUint64 {
			nextSequence = resequenceInvocations(next)
		} else {
			nextSequence = maxSequence + 1
		}
	}

	l.mu.Lock()
	l.latest = next
	l.nextSequence = nextSequence
	l.mu.Unlock()
	return errors.Join(validationErrors...)
}

// Clear removes all invocation snapshots and resets sequence allocation.
func (l *InvocationLedger) Clear() {
	l.mu.Lock()
	l.latest = make(map[string]Invocation)
	l.nextSequence = 1
	l.mu.Unlock()
}

func validateInvocationFields(name, rendered, source, path string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if !validSkillName.MatchString(normalized) {
		return "", fmt.Errorf("invalid invocation name %q", name)
	}
	if strings.TrimSpace(rendered) == "" {
		return "", fmt.Errorf("rendered content is empty")
	}
	if !utf8.ValidString(rendered) {
		return "", fmt.Errorf("rendered content must be valid UTF-8")
	}
	if len(rendered) > MaxRenderedSkillBytes {
		return "", fmt.Errorf("rendered content exceeds %d bytes", MaxRenderedSkillBytes)
	}
	if !utf8.ValidString(source) || len(source) > MaxInvocationSourceBytes {
		return "", fmt.Errorf("source must be valid UTF-8 and at most %d bytes", MaxInvocationSourceBytes)
	}
	if !utf8.ValidString(path) || len(path) > MaxInvocationPathBytes {
		return "", fmt.Errorf("path must be valid UTF-8 and at most %d bytes", MaxInvocationPathBytes)
	}
	return normalized, nil
}

func renderHash(rendered string) string {
	sum := sha256.Sum256([]byte(rendered))
	return hex.EncodeToString(sum[:])
}

func cloneInvocation(invocation Invocation) Invocation {
	invocation.Name = strings.Clone(invocation.Name)
	invocation.Rendered = strings.Clone(invocation.Rendered)
	invocation.RenderHash = strings.Clone(invocation.RenderHash)
	invocation.Source = strings.Clone(invocation.Source)
	invocation.Path = strings.Clone(invocation.Path)
	return invocation
}

func (l *InvocationLedger) ensureInitializedLocked() {
	if l.latest == nil {
		l.latest = make(map[string]Invocation)
	}
	if l.nextSequence == 0 {
		l.nextSequence = 1
	}
}

func (l *InvocationLedger) allocateSequenceLocked() uint64 {
	// MaxUint64 cannot be followed by another monotonic uint64 value. Rebase an
	// impossible-in-practice or maliciously restored near-overflow history before
	// allocating it, preserving relative order and never emitting zero.
	if l.nextSequence == math.MaxUint64 {
		l.nextSequence = resequenceInvocations(l.latest)
	}
	sequence := l.nextSequence
	l.nextSequence++
	return sequence
}

func (l *InvocationLedger) evictOldestLocked() {
	oldestName := ""
	var oldestSequence uint64
	for name, invocation := range l.latest {
		if oldestName == "" || invocation.Sequence < oldestSequence ||
			(invocation.Sequence == oldestSequence && name > oldestName) {
			oldestName = name
			oldestSequence = invocation.Sequence
		}
	}
	if oldestName != "" {
		delete(l.latest, oldestName)
	}
}

func invocationIsNewer(candidate, existing Invocation) bool {
	if candidate.Sequence != existing.Sequence {
		return candidate.Sequence > existing.Sequence
	}
	if candidate.RenderHash != existing.RenderHash {
		return candidate.RenderHash > existing.RenderHash
	}
	if candidate.Source != existing.Source {
		return candidate.Source > existing.Source
	}
	return candidate.Path > existing.Path
}

func invocationValuesNewestFirst(values map[string]Invocation) []Invocation {
	result := make([]Invocation, 0, len(values))
	for _, invocation := range values {
		result = append(result, invocation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Sequence != result[j].Sequence {
			return result[i].Sequence > result[j].Sequence
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func resequenceInvocations(values map[string]Invocation) uint64 {
	ordered := make([]Invocation, 0, len(values))
	for _, invocation := range values {
		ordered = append(ordered, invocation)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Sequence != ordered[j].Sequence {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		// Snapshot order treats the lexicographically smaller name as newer for a
		// corrupt sequence tie, so walk names in reverse while assigning old-to-new
		// compact sequences and preserve that same deterministic order.
		return ordered[i].Name > ordered[j].Name
	})
	for i, invocation := range ordered {
		invocation.Sequence = uint64(i + 1)
		values[invocation.Name] = invocation
	}
	return uint64(len(ordered)) + 1
}
