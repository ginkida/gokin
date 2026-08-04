package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuxiliaryStoresRejectOversizedFilesAndSkipNullEntries(t *testing.T) {
	examples, err := NewExampleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(examples.storagePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAuxiliaryStoreFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if err := examples.load(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized examples load error = %v", err)
	}
	if err := os.WriteFile(examples.storagePath(), []byte(`{"nil":null,"ok":{"id":"ok","task_type":"test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := examples.load(); err != nil || len(examples.examples) != 1 {
		t.Fatalf("null-safe examples load = %d, %v", len(examples.examples), err)
	}

	errorsStore, err := NewErrorStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errorsStore.storagePath(), []byte(`[null,{"id":"ok","error_type":"test"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := errorsStore.load(); err != nil || errorsStore.Count() != 1 {
		t.Fatalf("null-safe errors load = %d, %v", errorsStore.Count(), err)
	}
}

func TestErrorStoreLoadKeepsMostRecentEntriesWithinLimit(t *testing.T) {
	store, err := NewErrorStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	entries := make([]*ErrorEntry, maxLearnedErrorEntries+1)
	for i := range entries {
		entries[i] = &ErrorEntry{
			ID:       fmt.Sprintf("error-%d", i),
			LastUsed: time.Unix(int64(i), 0),
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.storagePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.load(); err != nil {
		t.Fatal(err)
	}
	if got := store.Count(); got != maxLearnedErrorEntries {
		t.Fatalf("entry count = %d, want %d", got, maxLearnedErrorEntries)
	}
	if _, found := store.entries["error-0"]; found {
		t.Fatal("oldest entry was not evicted")
	}
	if _, found := store.entries[fmt.Sprintf("error-%d", maxLearnedErrorEntries)]; !found {
		t.Fatal("newest entry was evicted")
	}
}
