package memory

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSearchQueryExcludesUnrelatedMemories(t *testing.T) {
	store := newTestStore(t)
	relevant := NewEntry("Authentication uses short-lived OAuth tokens", MemoryProject).
		WithKey("auth-token-policy").WithTags([]string{"oauth", "authentication"})
	unrelated := NewEntry("Frontend cards use twelve pixel rounded corners", MemoryProject).
		WithKey("card-style")
	unrelated.AccessCount = 500
	unrelated.SuccessCount = 500
	if err := store.Add(relevant); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(unrelated); err != nil {
		t.Fatal(err)
	}

	results := store.Search(SearchQuery{Query: "fix OAuth authentication", ProjectOnly: true})
	if len(results) != 1 || results[0].ID != relevant.ID {
		t.Fatalf("query returned unrelated memories: %#v", results)
	}
}

func TestGetRelevantForContextUsesCurrentQuery(t *testing.T) {
	store := newTestStore(t)
	auth := NewEntry("Run ./scripts/rotate-auth-keys before the authentication integration tests", MemoryProject).
		WithKey("auth-tests")
	ui := NewEntry("The settings panel uses Graphite borders", MemoryProject).
		WithKey("settings-theme")
	auth.Timestamp = time.Now().Add(-60 * 24 * time.Hour)
	ui.Timestamp = time.Now()
	if err := store.Add(auth); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(ui); err != nil {
		t.Fatal(err)
	}

	got := store.GetRelevantForContext("repair the authentication test", true, 0)
	if !strings.Contains(got, "rotate-auth-keys") {
		t.Fatalf("relevant older memory was not recalled:\n%s", got)
	}
	if strings.Contains(got, "Graphite") {
		t.Fatalf("fresh but unrelated memory leaked into query context:\n%s", got)
	}
	if !strings.Contains(got, "Relevant Memory for This Turn") {
		t.Fatalf("missing per-turn memory heading:\n%s", got)
	}
}

func TestGetRelevantForContextHonorsScopeAndNoiseFilter(t *testing.T) {
	store := newTestStore(t)
	if err := store.Add(NewEntry("Use the private global deploy cluster", MemoryGlobal).WithKey("deploy-cluster")); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(NewEntry("Project deploys through staging first", MemoryProject).WithKey("deploy-order")); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(NewEntry("Не удалять generated API clients вручную", MemoryProject).WithKey("generated-clients")); err != nil {
		t.Fatal(err)
	}

	project := store.GetRelevantForContext("continue the deploy work", true, 0)
	if strings.Contains(project, "private global") || !strings.Contains(project, "staging first") {
		t.Fatalf("project-only retrieval used the wrong scope:\n%s", project)
	}
	if got := store.GetRelevantForContext("please continue this work", true, 0); got != "" {
		t.Fatalf("conversational glue produced a false recall:\n%s", got)
	}
	if got := store.GetRelevantForContext("это не то что надо", true, 0); got != "" {
		t.Fatalf("short UTF-8 glue words produced a false recall:\n%s", got)
	}
}

func TestGetRelevantForContextConcurrentMutation(t *testing.T) {
	store := newTestStore(t)
	entry := NewEntry("Authentication tests use a local issuer", MemoryProject).WithKey("auth-issuer")
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = store.GetRelevantForContext("authentication issuer", true, 0)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 50 {
			store.RecordFeedback(entry.ID, i%2 == 0)
		}
	}()
	wg.Wait()
}

func TestGetForContextConcurrentMutation(t *testing.T) {
	store := newTestStore(t)
	entry := NewEntry("Use the local authentication issuer", MemoryProject).WithKey("issuer")
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = store.GetForContext(true)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 50 {
			store.RecordFeedback(entry.ID, i%2 == 0)
		}
	}()
	wg.Wait()
}

func TestStoreReadAPIsReturnOwnedSnapshots(t *testing.T) {
	store := newTestStore(t)
	entry := NewEntry("original content", MemoryProject).WithKey("owned")
	entry.WithTags([]string{"original-tag"})
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}

	got, ok := store.Get("owned")
	if !ok {
		t.Fatal("Get did not find stored entry")
	}
	got.Content = "caller mutation"
	got.Tags[0] = "caller-tag"
	results := store.Search(SearchQuery{Query: "original content", ProjectOnly: true})
	if len(results) != 1 {
		t.Fatalf("Search returned %d entries, want 1", len(results))
	}
	results[0].Content = "search mutation"

	stored, ok := store.GetByID(entry.ID)
	if !ok {
		t.Fatal("GetByID did not find stored entry")
	}
	if stored.Content != "original content" || len(stored.Tags) != 1 || stored.Tags[0] != "original-tag" {
		t.Fatalf("caller mutated live store state: %#v", stored)
	}
}

func TestAddResolvedReturnsCanonicalEntryAfterDedup(t *testing.T) {
	store := newTestStore(t)
	first := NewEntry("Deploy keys rotate every ninety days", MemoryProject).WithKey("deploy-key")
	canonical, err := store.AddResolved(first)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := NewEntry("Deploy keys rotate every ninety days", MemoryProject).WithKey("deploy-key")
	resolved, err := store.AddResolved(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != canonical.ID || resolved.ID == duplicate.ID {
		t.Fatalf("dedup returned non-canonical ID: first=%q duplicate=%q resolved=%q", canonical.ID, duplicate.ID, resolved.ID)
	}
	if resolved.Reinforcement != 1 {
		t.Fatalf("canonical reinforcement = %d, want 1", resolved.Reinforcement)
	}
}

func TestAddResolvedDedupReplacesCanonicalKeyIndex(t *testing.T) {
	store := newTestStore(t)
	first := NewEntry("Integration tests require Redis", MemoryProject).WithKey("old-test-key")
	canonical, err := store.AddResolved(first)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.AddResolved(
		NewEntry("Integration tests require Redis", MemoryProject).WithKey("current-test-key"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != canonical.ID || resolved.Key != "current-test-key" {
		t.Fatalf("unexpected canonical entry after re-key: %#v", resolved)
	}
	if _, ok := store.Get("old-test-key"); ok {
		t.Fatal("stale key still resolves after canonical memory was re-keyed")
	}
	if got, ok := store.Get("current-test-key"); !ok || got.ID != canonical.ID {
		t.Fatalf("new key does not resolve canonical entry: got=%#v ok=%v", got, ok)
	}
}

func TestClearSessionPreservesDurableMemory(t *testing.T) {
	store := newTestStore(t)
	sessionEntry := NewEntry("temporary investigation state", MemorySession).WithKey("current-task")
	projectEntry := NewEntry("go test ./... is the project verification", MemoryProject).WithKey("verify")
	globalEntry := NewEntry("prefer concise status updates", MemoryGlobal).WithKey("preference")
	for _, entry := range []*Entry{sessionEntry, projectEntry, globalEntry} {
		if err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	if removed := store.ClearSession(); removed != 1 {
		t.Fatalf("ClearSession removed %d entries, want 1", removed)
	}
	if _, ok := store.Get("current-task"); ok {
		t.Fatal("session entry survived ClearSession")
	}
	if _, ok := store.Get("verify"); !ok {
		t.Fatal("project memory was removed by ClearSession")
	}
	if _, ok := store.Get("preference"); !ok {
		t.Fatal("global memory was removed by ClearSession")
	}
	if context := store.GetForContext(false); strings.Contains(context, "temporary investigation") {
		t.Fatalf("cleared session entry remained in cached context:\n%s", context)
	}
}

func TestBoundMemoryQueryKeepsIntentAtBothEnds(t *testing.T) {
	query := "HEAD-INTENT " + strings.Repeat("middle-payload ", 1000) + " TAIL-INTENT"
	bounded := boundMemoryQuery(query)
	if len([]rune(bounded)) > maxMemoryQueryRunes+3 {
		t.Fatalf("bounded query has %d runes", len([]rune(bounded)))
	}
	if !strings.Contains(bounded, "HEAD-INTENT") || !strings.Contains(bounded, "TAIL-INTENT") {
		t.Fatalf("bounded query lost user intent at an edge: %q", bounded)
	}
	if !strings.Contains(bounded, "…") {
		t.Fatal("bounded query does not mark the omitted middle")
	}
}
