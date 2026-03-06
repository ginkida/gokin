package agent

import (
	"testing"
	"time"
)

func TestSharedEntryIsExpired(t *testing.T) {
	// No TTL - never expires
	e := &SharedEntry{TTL: 0, Timestamp: time.Now().Add(-24 * time.Hour)}
	if e.IsExpired() {
		t.Error("zero TTL should never expire")
	}

	// Not expired yet
	e = &SharedEntry{TTL: time.Hour, Timestamp: time.Now()}
	if e.IsExpired() {
		t.Error("should not be expired yet")
	}

	// Expired
	e = &SharedEntry{TTL: time.Millisecond, Timestamp: time.Now().Add(-time.Second)}
	if !e.IsExpired() {
		t.Error("should be expired")
	}
}

func TestSharedMemoryWriteRead(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("key1", "value1", SharedEntryTypeFact, "agent-1")

	entry, ok := sm.Read("key1")
	if !ok {
		t.Fatal("should find entry")
	}
	if entry.Value != "value1" {
		t.Errorf("Value = %v", entry.Value)
	}
	if entry.Type != SharedEntryTypeFact {
		t.Errorf("Type = %v", entry.Type)
	}
	if entry.Source != "agent-1" {
		t.Errorf("Source = %q", entry.Source)
	}
	if entry.Version != 1 {
		t.Errorf("Version = %d", entry.Version)
	}

	// Not found
	_, ok = sm.Read("nonexistent")
	if ok {
		t.Error("should not find nonexistent key")
	}
}

func TestSharedMemoryWriteUpdate(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("key1", "v1", SharedEntryTypeFact, "agent-1")
	sm.Write("key1", "v2", SharedEntryTypeFact, "agent-2")

	entry, ok := sm.Read("key1")
	if !ok {
		t.Fatal("should find entry")
	}
	if entry.Value != "v2" {
		t.Errorf("Value = %v, want v2", entry.Value)
	}
	if entry.Version != 2 {
		t.Errorf("Version = %d, want 2", entry.Version)
	}
	if entry.Source != "agent-2" {
		t.Errorf("Source = %q, want agent-2", entry.Source)
	}
}

func TestSharedMemoryWriteWithTTL(t *testing.T) {
	sm := NewSharedMemory()

	sm.WriteWithTTL("temp", "value", SharedEntryTypeFact, "agent-1", time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	_, ok := sm.Read("temp")
	if ok {
		t.Error("expired entry should not be readable")
	}
}

func TestSharedMemoryReadByType(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("fact1", "f1", SharedEntryTypeFact, "a1")
	sm.Write("fact2", "f2", SharedEntryTypeFact, "a1")
	sm.Write("insight1", "i1", SharedEntryTypeInsight, "a1")

	facts := sm.ReadByType(SharedEntryTypeFact)
	if len(facts) != 2 {
		t.Errorf("facts count = %d, want 2", len(facts))
	}

	insights := sm.ReadByType(SharedEntryTypeInsight)
	if len(insights) != 1 {
		t.Errorf("insights count = %d, want 1", len(insights))
	}

	decisions := sm.ReadByType(SharedEntryTypeDecision)
	if len(decisions) != 0 {
		t.Errorf("decisions count = %d, want 0", len(decisions))
	}
}

func TestSharedMemoryReadAll(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("a", 1, SharedEntryTypeFact, "a1")
	sm.Write("b", 2, SharedEntryTypeInsight, "a1")

	all := sm.ReadAll()
	if len(all) != 2 {
		t.Errorf("ReadAll count = %d, want 2", len(all))
	}
}

func TestSharedMemoryDelete(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("key1", "v1", SharedEntryTypeFact, "a1")

	if !sm.Delete("key1") {
		t.Error("Delete should return true for existing key")
	}
	if sm.Delete("key1") {
		t.Error("Delete should return false for already deleted key")
	}
	if sm.Delete("nonexistent") {
		t.Error("Delete should return false for nonexistent key")
	}

	_, ok := sm.Read("key1")
	if ok {
		t.Error("deleted key should not be readable")
	}
}

func TestSharedMemoryClear(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("a", 1, SharedEntryTypeFact, "a1")
	sm.Write("b", 2, SharedEntryTypeFact, "a1")
	sm.Clear()

	all := sm.ReadAll()
	if len(all) != 0 {
		t.Errorf("ReadAll after Clear = %d", len(all))
	}
}

func TestSharedMemoryStats(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("f1", 1, SharedEntryTypeFact, "a1")
	sm.Write("f2", 2, SharedEntryTypeFact, "a1")
	sm.Write("i1", 3, SharedEntryTypeInsight, "a1")

	stats := sm.Stats()
	if stats.TotalEntries != 3 {
		t.Errorf("TotalEntries = %d, want 3", stats.TotalEntries)
	}
	if stats.ByType[SharedEntryTypeFact] != 2 {
		t.Errorf("facts = %d, want 2", stats.ByType[SharedEntryTypeFact])
	}
	if stats.ByType[SharedEntryTypeInsight] != 1 {
		t.Errorf("insights = %d, want 1", stats.ByType[SharedEntryTypeInsight])
	}
}

func TestSharedMemorySubscribeUnsubscribe(t *testing.T) {
	sm := NewSharedMemory()

	ch := sm.Subscribe("agent-1")
	if ch == nil {
		t.Fatal("Subscribe should return non-nil channel")
	}

	// Write should notify subscriber
	sm.Write("key1", "val", SharedEntryTypeFact, "agent-2")

	select {
	case entry := <-ch:
		if entry.Key != "key1" {
			t.Errorf("Key = %q", entry.Key)
		}
	case <-time.After(time.Second):
		t.Fatal("should receive notification")
	}

	// Unsubscribe
	sm.Unsubscribe("agent-1")

	stats := sm.Stats()
	if stats.Subscribers != 0 {
		t.Errorf("Subscribers = %d after unsubscribe", stats.Subscribers)
	}
}

func TestSharedMemoryCleanupExpired(t *testing.T) {
	sm := NewSharedMemory()

	sm.WriteWithTTL("temp1", "v1", SharedEntryTypeFact, "a1", time.Millisecond)
	sm.WriteWithTTL("temp2", "v2", SharedEntryTypeFact, "a1", time.Millisecond)
	sm.Write("perm", "v3", SharedEntryTypeFact, "a1")

	time.Sleep(5 * time.Millisecond)

	removed := sm.CleanupExpired()
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	stats := sm.Stats()
	if stats.TotalEntries != 1 {
		t.Errorf("TotalEntries after cleanup = %d, want 1", stats.TotalEntries)
	}
}

func TestSharedMemoryCapacityLimit(t *testing.T) {
	sm := NewSharedMemory()

	// Fill to capacity
	for i := 0; i < MaxSharedEntries+10; i++ {
		sm.Write(
			"key-"+string(rune('A'+i%26))+"-"+time.Now().String(),
			i,
			SharedEntryTypeFact,
			"a1",
		)
	}

	stats := sm.Stats()
	if stats.TotalEntries > MaxSharedEntries {
		t.Errorf("TotalEntries = %d, should not exceed %d", stats.TotalEntries, MaxSharedEntries)
	}
}

func TestSharedMemoryGetForContext(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("fact1", "important fact", SharedEntryTypeFact, "agent-1")
	sm.Write("fact2", "another fact", SharedEntryTypeFact, "agent-2")

	// Agent should not see own entries
	ctx := sm.GetForContext("agent-1", 10)
	if ctx == "" {
		t.Error("should have context from other agent")
	}

	// Empty if no entries from others
	sm.Clear()
	sm.Write("self", "my fact", SharedEntryTypeFact, "agent-1")
	ctx = sm.GetForContext("agent-1", 10)
	if ctx != "" {
		t.Error("should be empty when only self entries")
	}
}

func TestSharedMemoryTypeChange(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("key1", "v1", SharedEntryTypeFact, "a1")
	sm.Write("key1", "v2", SharedEntryTypeDecision, "a1")

	facts := sm.ReadByType(SharedEntryTypeFact)
	if len(facts) != 0 {
		t.Error("old type should be removed from index")
	}

	decisions := sm.ReadByType(SharedEntryTypeDecision)
	if len(decisions) != 1 {
		t.Error("new type should be in index")
	}
}

// --- ContextSnapshot tests ---

func TestNewContextSnapshot(t *testing.T) {
	cs := NewContextSnapshot()
	if cs.KeyFiles == nil || cs.Discoveries == nil || cs.ErrorPatterns == nil ||
		cs.CriticalResults == nil || cs.Requirements == nil || cs.Decisions == nil {
		t.Error("all fields should be initialized")
	}
}

func TestContextSnapshotAddMethods(t *testing.T) {
	cs := NewContextSnapshot()

	cs.AddKeyFile("/src/main.go", "entry point")
	if cs.KeyFiles["/src/main.go"] != "entry point" {
		t.Error("AddKeyFile failed")
	}

	cs.AddDiscovery("uses PostgreSQL")
	if len(cs.Discoveries) != 1 {
		t.Error("AddDiscovery failed")
	}

	cs.AddRequirement("must support auth")
	if len(cs.Requirements) != 1 {
		t.Error("AddRequirement failed")
	}

	cs.AddDecision("use JWT tokens")
	if len(cs.Decisions) != 1 {
		t.Error("AddDecision failed")
	}

	cs.AddCriticalResult("grep", "found pattern", "in 5 files")
	if len(cs.CriticalResults) != 1 {
		t.Error("AddCriticalResult failed")
	}
	if cs.CriticalResults[0].ToolName != "grep" {
		t.Errorf("ToolName = %q", cs.CriticalResults[0].ToolName)
	}

	cs.AddErrorPattern("nil pointer", "check for nil")
	if cs.ErrorPatterns["nil pointer"] != "check for nil" {
		t.Error("AddErrorPattern failed")
	}
}

func TestSaveAndGetContextSnapshot(t *testing.T) {
	sm := NewSharedMemory()

	cs := NewContextSnapshot()
	cs.AddKeyFile("/main.go", "entry point")
	cs.AddDiscovery("uses gRPC")

	sm.SaveContextSnapshot(cs, "planner-1")

	got := sm.GetContextSnapshot()
	if got == nil {
		t.Fatal("should retrieve snapshot")
	}
	if got.Source != "planner-1" {
		t.Errorf("Source = %q", got.Source)
	}
	if got.KeyFiles["/main.go"] != "entry point" {
		t.Error("KeyFiles not preserved")
	}
	if len(got.Discoveries) != 1 {
		t.Error("Discoveries not preserved")
	}
}

func TestGetContextSnapshotForPrompt(t *testing.T) {
	sm := NewSharedMemory()

	// Empty
	prompt := sm.GetContextSnapshotForPrompt()
	if prompt != "" {
		t.Error("should be empty with no snapshot")
	}

	cs := NewContextSnapshot()
	cs.AddKeyFile("/main.go", "entry")
	cs.AddDiscovery("uses REST API")
	cs.AddRequirement("must handle errors")
	cs.AddDecision("use middleware pattern")
	cs.AddCriticalResult("grep", "found 10 endpoints", "short details")
	cs.AddErrorPattern("timeout", "increase deadline")

	sm.SaveContextSnapshot(cs, "agent-1")

	prompt = sm.GetContextSnapshotForPrompt()
	if prompt == "" {
		t.Fatal("should have prompt content")
	}

	for _, expected := range []string{
		"CONTEXT FROM PLANNING PHASE",
		"/main.go",
		"uses REST API",
		"must handle errors",
		"middleware pattern",
		"found 10 endpoints",
		"timeout",
	} {
		if !containsStr(prompt, expected) {
			t.Errorf("prompt missing %q", expected)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
