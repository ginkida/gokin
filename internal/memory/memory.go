package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// MemoryType represents the scope of the memory.
type MemoryType string

const (
	MemorySession MemoryType = "session" // Deleted after session
	MemoryProject MemoryType = "project" // Specific to this project
	MemoryGlobal  MemoryType = "global"  // Available across all projects
)

// Entry represents a single memory entry.
type Entry struct {
	ID            string     `json:"id"`
	Key           string     `json:"key,omitempty"`
	Content       string     `json:"content"`
	Type          MemoryType `json:"type"` // session, project, global
	Tags          []string   `json:"tags,omitempty"`
	Timestamp     time.Time  `json:"timestamp"`
	Project       string     `json:"project,omitempty"`
	LastAccessed  time.Time  `json:"last_accessed,omitempty"`
	AccessCount   int        `json:"access_count,omitempty"`
	SuccessCount  int        `json:"success_count,omitempty"`
	FailureCount  int        `json:"failure_count,omitempty"`
	Reinforcement int        `json:"reinforcement,omitempty"` // Increased when near-duplicate facts are remembered again
	ExpiresAt     time.Time  `json:"expires_at,omitempty"`
	Archived      bool       `json:"archived,omitempty"`
	ArchiveReason string     `json:"archive_reason,omitempty"`
}

// NewEntry creates a new memory entry with auto-generated ID.
func NewEntry(content string, memType MemoryType) *Entry {
	return &Entry{
		ID:        generateID(content),
		Content:   content,
		Type:      memType,
		Timestamp: time.Now(),
	}
}

// WithKey sets the key for the entry.
func (e *Entry) WithKey(key string) *Entry {
	e.Key = key
	return e
}

// WithTags sets the tags for the entry.
func (e *Entry) WithTags(tags []string) *Entry {
	e.Tags = tags
	return e
}

// WithProject sets the project for the entry.
func (e *Entry) WithProject(project string) *Entry {
	e.Project = project
	return e
}

// WithTTL sets the expiration time for the entry.
func (e *Entry) WithTTL(ttl time.Duration) *Entry {
	if ttl > 0 {
		e.ExpiresAt = time.Now().Add(ttl)
	}
	return e
}

// HasTag returns true if the entry has the specified tag.
func (e *Entry) HasTag(tag string) bool {
	tag = strings.TrimSpace(strings.ToLower(tag))
	for _, t := range e.Tags {
		if strings.ToLower(strings.TrimSpace(t)) == tag {
			return true
		}
	}
	return false
}

// IsExpired returns true when the entry TTL has elapsed.
func (e *Entry) IsExpired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt)
}

// SuccessRate returns a smoothed success ratio for ranking.
func (e *Entry) SuccessRate() float64 {
	total := e.SuccessCount + e.FailureCount
	if total == 0 {
		return 0.5
	}
	return float64(e.SuccessCount+1) / float64(total+2) // Laplace smoothing
}

// RecordAccess updates retrieval usage counters.
func (e *Entry) RecordAccess() {
	e.AccessCount++
	e.LastAccessed = time.Now()
}

// RecordOutcome updates success/failure counters for retrieval quality feedback.
func (e *Entry) RecordOutcome(success bool) {
	if success {
		e.SuccessCount++
	} else {
		e.FailureCount++
	}
	e.LastAccessed = time.Now()
}

// generateID generates a unique ID for the entry based on content and timestamp.
func generateID(content string) string {
	data := content + time.Now().String()
	hash := sha256.Sum256([]byte(data))
	return "mem_" + hex.EncodeToString(hash[:8])
}

// SearchQuery represents a query for searching memories.
type SearchQuery struct {
	Key             string   // Exact key match
	Query           string   // Content search (substring)
	Tags            []string // All tags must match
	ProjectOnly     bool     // Only match entries for current project
	Project         string   // Project to match (set by store)
	Limit           int      // Max results (0 = unlimited)
	IncludeArchived bool     // Include archived entries in search
}

// Matches returns true if the entry matches the search query.
func (e *Entry) Matches(q SearchQuery) bool {
	if !q.IncludeArchived && e.Archived {
		return false
	}

	// Key match (exact)
	if q.Key != "" && e.Key != q.Key {
		return false
	}

	// Project filter
	if q.ProjectOnly && q.Project != "" && e.Project != q.Project {
		return false
	}

	// Tag filter (all tags must match)
	for _, tag := range q.Tags {
		if !e.HasTag(tag) {
			return false
		}
	}

	// Content search is handled separately for flexibility

	return true
}
