package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPromptPatchesAreBoundedAndSessionOnly(t *testing.T) {
	workDir := t.TempDir()
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePrompt("After two identical failures, inspect the environment before retrying.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.RenderPrompt(), "inspect the environment") {
		t.Fatalf("rendered prompt = %q", store.RenderPrompt())
	}
	updated, err := store.UpdatePrompt(created.ID, "Do not repeat an identical failed command.")
	if err != nil || updated.ID != created.ID || !strings.Contains(store.RenderPrompt(), "Do not repeat") {
		t.Fatalf("updated=%+v err=%v prompt=%q", updated, err, store.RenderPrompt())
	}
	if _, err := store.CreatePrompt(strings.Repeat("x", MaxPromptPatchBytes+1)); err == nil {
		t.Fatal("oversized prompt patch accepted")
	}
	if err := store.DeletePrompt(created.ID); err != nil || store.RenderPrompt() != "" {
		t.Fatalf("delete prompt err=%v rendered=%q", err, store.RenderPrompt())
	}

	reopened, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.ListPrompts()) != 0 {
		t.Fatal("session prompt patches persisted to disk")
	}
}

func TestEpisodicMemoryPersistsPrivatelyAndRejectsCorruption(t *testing.T) {
	workDir := t.TempDir()
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutMemory("parser.retry-rule", "Use the generated parser for nested input."); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, ".gokin", "harness", "memory.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("memory mode = %04o", info.Mode().Perm())
	}
	reopened, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reopened.GetMemory("parser.retry-rule")
	if !ok || !strings.Contains(entry.Value, "generated parser") {
		t.Fatalf("reloaded entry = %+v, ok=%v", entry, ok)
	}
	if err := reopened.DeleteMemory("parser.retry-rule"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.GetMemory("parser.retry-rule"); ok {
		t.Fatal("memory delete did not update in-memory state")
	}
	if _, err := reopened.PutMemory("../escape", "x"); err == nil {
		t.Fatal("unsafe memory key accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"entries":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(workDir); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("corrupt/unsupported memory error = %v", err)
	}
}

func TestSkillProposalIsInertAndDeletionFailsClosed(t *testing.T) {
	workDir := t.TempDir()
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.ProposeSkill("nested-parser", "Parse nested legacy records", "def parse(value):\n    return value\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(proposal.Path, ".gokin/harness/proposals/skills/") {
		t.Fatalf("proposal path = %q", proposal.Path)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".gokin", "skills", "nested-parser")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proposal was activated or skills path unexpectedly exists: %v", err)
	}
	listed, err := store.ListSkills()
	if err != nil || len(listed) != 1 || listed[0].Name != "nested-parser" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	dir := filepath.Join(workDir, filepath.FromSlash(proposal.Path))
	if err := os.WriteFile(filepath.Join(dir, "unexpected.txt"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSkill("nested-parser"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("modified proposal delete error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "unexpected.txt")); err != nil {
		t.Fatalf("failed-closed delete removed unexpected data: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "unexpected.txt")); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSkill("nested-parser"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proposal directory remains: %v", err)
	}
}

func TestStoreConcurrentMemoryWritesRemainWhole(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			_, err := store.PutMemory("key-"+string(rune('a'+i)), strings.Repeat(string(rune('a'+i)), 256))
			errs <- err
		}(i)
	}
	for range 16 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := len(store.ListMemory()); got != 16 {
		t.Fatalf("memory entries = %d", got)
	}
}

func TestIndependentStoresMergeConcurrentMemoryWrites(t *testing.T) {
	workDir := t.TempDir()
	left, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		store := left
		if i%2 == 1 {
			store = right
		}
		go func(i int, store *Store) {
			key := "session-" + strconv.Itoa(i)
			_, err := store.PutMemoryContext(t.Context(), key, "value-"+key)
			errs <- err
		}(i, store)
	}
	for range 40 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	entries, err := left.ListMemoryFresh()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 40 {
		t.Fatalf("merged entries = %d, want 40: %+v", len(entries), entries)
	}
	reopened, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.ListMemory()); got != 40 {
		t.Fatalf("durable merged entries = %d, want 40", got)
	}
}

func TestMemoryLeaseHonorsCancellationAndRecovers(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.acquireMemoryLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err = store.PutMemoryContext(ctx, "blocked", "value")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended write error = %v", err)
	}
	lease.release()
	if _, err := store.PutMemoryContext(t.Context(), "recovered", "value"); err != nil {
		t.Fatalf("write after lease release: %v", err)
	}
	lockInfo, err := os.Stat(store.memoryLockPath())
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("memory lock mode = %04o", lockInfo.Mode().Perm())
	}
}

func TestFreshReadsObserveOtherStoreWrites(t *testing.T) {
	workDir := t.TempDir()
	reader, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.PutMemory("cross-session", "visible"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reader.GetMemory("cross-session"); ok {
		t.Fatal("test precondition failed: stale in-memory snapshot unexpectedly updated")
	}
	entry, ok, err := reader.GetMemoryFresh("cross-session")
	if err != nil || !ok || entry.Value != "visible" {
		t.Fatalf("fresh entry=%+v ok=%v err=%v", entry, ok, err)
	}
}
