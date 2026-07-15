package skills

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func persistedInvocation(name, rendered string, sequence uint64) Invocation {
	return Invocation{
		Name:       name,
		Rendered:   rendered,
		RenderHash: renderHash(rendered),
		Source:     "project",
		Path:       "/project/skills/" + strings.TrimSpace(name) + "/SKILL.md",
		Sequence:   sequence,
	}
}

func TestInvocationLedgerRecordDeduplicatesAndSequencesChanges(t *testing.T) {
	ledger := NewInvocationLedger()
	first, changed, err := ledger.Record(" Verify ", "render one", "project", "/one")
	if err != nil || !changed {
		t.Fatalf("first Record = %#v changed=%v err=%v", first, changed, err)
	}
	if first.Name != "verify" || first.Sequence != 1 || first.RenderHash != renderHash("render one") {
		t.Fatalf("first = %#v", first)
	}

	duplicate, changed, err := ledger.Record("VERIFY", "render one", "different-source", "/different")
	if err != nil || changed || duplicate != first {
		t.Fatalf("duplicate = %#v changed=%v err=%v, want existing %#v", duplicate, changed, err, first)
	}
	second, changed, err := ledger.Record("verify", "render two", "project", "/two")
	if err != nil || !changed || second.Sequence != 2 || second.RenderHash == first.RenderHash {
		t.Fatalf("changed render = %#v changed=%v err=%v", second, changed, err)
	}

	snapshot := ledger.SnapshotNewestFirst()
	if len(snapshot) != 1 || snapshot[0] != second {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	snapshot[0].Rendered = "mutated"
	if got := ledger.SnapshotNewestFirst()[0].Rendered; got != "render two" {
		t.Fatalf("snapshot was not independent: %q", got)
	}
}

func TestInvocationJSONTags(t *testing.T) {
	invocation := persistedInvocation("verify", "rendered", 7)
	data, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"name"`, `"rendered"`, `"render_hash"`, `"source"`, `"path"`, `"sequence"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("JSON %s lacks %s", data, key)
		}
	}
}

func TestInvocationLedgerRecordValidationTable(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		rendered string
		source   string
		path     string
	}{
		{name: "empty name", key: " ", rendered: "x"},
		{name: "invalid name", key: "bad/name", rendered: "x"},
		{name: "empty render", key: "valid", rendered: " \n\t"},
		{name: "invalid render utf8", key: "valid", rendered: string([]byte{0xff})},
		{name: "oversized render", key: "valid", rendered: strings.Repeat("x", MaxRenderedSkillBytes+1)},
		{name: "oversized source", key: "valid", rendered: "x", source: strings.Repeat("s", MaxInvocationSourceBytes+1)},
		{name: "oversized path", key: "valid", rendered: "x", path: strings.Repeat("p", MaxInvocationPathBytes+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := NewInvocationLedger()
			if _, _, err := ledger.Record(tc.key, tc.rendered, tc.source, tc.path); err == nil {
				t.Fatal("Record accepted invalid input")
			}
			if got := ledger.SnapshotNewestFirst(); len(got) != 0 {
				t.Fatalf("invalid Record mutated ledger: %#v", got)
			}
		})
	}

	ledger := NewInvocationLedger()
	if _, changed, err := ledger.Record("valid", strings.Repeat("x", MaxRenderedSkillBytes), strings.Repeat("s", MaxInvocationSourceBytes), strings.Repeat("p", MaxInvocationPathBytes)); err != nil || !changed {
		t.Fatalf("exact bounds rejected: changed=%v err=%v", changed, err)
	}
}

func TestInvocationLedgerRestoreValidatesDeduplicatesAndRepairsNext(t *testing.T) {
	fooOld := persistedInvocation("foo", "old", 2)
	fooDuplicate := persistedInvocation(" FOO ", "old", 4)
	fooDuplicate.RenderHash = strings.ToUpper(fooDuplicate.RenderHash)
	fooNew := persistedInvocation("foo", "new", 5)
	bar := persistedInvocation("bar", "bar", 3)
	badHash := persistedInvocation("corrupt", "payload", 100)
	badHash.RenderHash = strings.Repeat("0", 64)
	zeroSequence := persistedInvocation("zero", "payload", 0)
	badName := persistedInvocation("bad/name", "payload", 9)
	oversized := persistedInvocation("oversized", strings.Repeat("x", MaxRenderedSkillBytes+1), 10)

	ledger := NewInvocationLedger()
	err := ledger.Restore([]Invocation{fooOld, fooDuplicate, fooNew, bar, badHash, zeroSequence, badName, oversized})
	if err == nil {
		t.Fatal("Restore did not report corrupt entries")
	}
	snapshot := ledger.SnapshotNewestFirst()
	if len(snapshot) != 2 || snapshot[0].Name != "foo" || snapshot[0].Rendered != "new" || snapshot[0].Sequence != 5 || snapshot[1].Name != "bar" {
		t.Fatalf("restored snapshot = %#v", snapshot)
	}
	next, changed, err := ledger.Record("next", "next render", "project", "/next")
	if err != nil || !changed || next.Sequence != 6 {
		t.Fatalf("next Record = %#v changed=%v err=%v", next, changed, err)
	}
}

func TestInvocationLedgerRestoreRepairsSequenceOverflow(t *testing.T) {
	ledger := NewInvocationLedger()
	if err := ledger.Restore([]Invocation{
		persistedInvocation("older", "older", math.MaxUint64-1),
		persistedInvocation("newer", "newer", math.MaxUint64),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := ledger.SnapshotNewestFirst()
	if len(snapshot) != 2 || snapshot[0].Name != "newer" || snapshot[0].Sequence != 2 || snapshot[1].Sequence != 1 {
		t.Fatalf("overflow repair = %#v", snapshot)
	}
	next, changed, err := ledger.Record("after", "after", "project", "/after")
	if err != nil || !changed || next.Sequence != 3 {
		t.Fatalf("post-repair Record = %#v changed=%v err=%v", next, changed, err)
	}
}

func TestInvocationLedgerDeterministicallyBoundsAndEvictsOldest(t *testing.T) {
	ledger := NewInvocationLedger()
	for i := 0; i < MaxSkillCount+2; i++ {
		name := fmt.Sprintf("skill-%03d", i)
		if _, _, err := ledger.Record(name, "render "+name, "project", "/"+name); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := ledger.SnapshotNewestFirst()
	if len(snapshot) != MaxSkillCount || snapshot[0].Sequence != MaxSkillCount+2 || snapshot[len(snapshot)-1].Sequence != 3 {
		t.Fatalf("bounded snapshot len=%d first=%#v last=%#v", len(snapshot), snapshot[0], snapshot[len(snapshot)-1])
	}
	for _, invocation := range snapshot {
		if invocation.Name == "skill-000" || invocation.Name == "skill-001" {
			t.Fatalf("oldest invocation was not evicted: %#v", invocation)
		}
	}

	restored := make([]Invocation, 0, MaxSkillCount+2)
	for i := 1; i <= MaxSkillCount+2; i++ {
		name := fmt.Sprintf("restore-%03d", i)
		restored = append(restored, persistedInvocation(name, name, uint64(i)))
	}
	if err := ledger.Restore(restored); err != nil {
		t.Fatal(err)
	}
	snapshot = ledger.SnapshotNewestFirst()
	if len(snapshot) != MaxSkillCount || snapshot[len(snapshot)-1].Sequence != 3 {
		t.Fatalf("bounded restore = %#v", snapshot)
	}
}

func TestInvocationLedgerRestoreEntryBoundIsAtomic(t *testing.T) {
	ledger := NewInvocationLedger()
	if _, _, err := ledger.Record("existing", "existing", "project", "/existing"); err != nil {
		t.Fatal(err)
	}
	err := ledger.Restore(make([]Invocation, MaxInvocationRestoreEntries+1))
	if err == nil {
		t.Fatal("oversized restore was accepted")
	}
	if snapshot := ledger.SnapshotNewestFirst(); len(snapshot) != 1 || snapshot[0].Name != "existing" {
		t.Fatalf("oversized restore mutated ledger: %#v", snapshot)
	}
}

func TestInvocationLedgerConcurrentRecordAndSnapshot(t *testing.T) {
	ledger := NewInvocationLedger()
	var changedCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := ledger.Record("shared", "identical", "project", "/shared")
			if err != nil {
				t.Errorf("Record: %v", err)
				return
			}
			if changed {
				changedCount.Add(1)
			}
			_ = ledger.SnapshotNewestFirst()
		}()
	}
	wg.Wait()
	if changedCount.Load() != 1 {
		t.Fatalf("identical concurrent render changed %d times", changedCount.Load())
	}
	if snapshot := ledger.SnapshotNewestFirst(); len(snapshot) != 1 || snapshot[0].Sequence != 1 {
		t.Fatalf("concurrent snapshot = %#v", snapshot)
	}

	for worker := 0; worker < 16; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				name := fmt.Sprintf("worker-%02d", worker)
				rendered := fmt.Sprintf("render-%02d-%03d", worker, i)
				if _, _, err := ledger.Record(name, rendered, "project", "/"+name); err != nil {
					t.Errorf("Record: %v", err)
					return
				}
				if i%7 == 0 {
					_ = ledger.SnapshotNewestFirst()
				}
			}
		}()
	}
	wg.Wait()
	snapshot := ledger.SnapshotNewestFirst()
	seen := make(map[string]bool)
	for i, invocation := range snapshot {
		if seen[invocation.Name] || invocation.RenderHash != renderHash(invocation.Rendered) {
			t.Fatalf("invalid concurrent snapshot entry: %#v", invocation)
		}
		seen[invocation.Name] = true
		if i > 0 && snapshot[i-1].Sequence <= invocation.Sequence {
			t.Fatalf("snapshot is not newest-first at %d: %#v", i, snapshot)
		}
	}
}

func TestInvocationLedgerClear(t *testing.T) {
	var ledger InvocationLedger
	if first, changed, err := ledger.Record("first", "first", "", ""); err != nil || !changed || first.Sequence != 1 {
		t.Fatalf("zero-value Record = %#v changed=%v err=%v", first, changed, err)
	}
	ledger.Clear()
	if snapshot := ledger.SnapshotNewestFirst(); len(snapshot) != 0 {
		t.Fatalf("Clear left entries: %#v", snapshot)
	}
	if next, changed, err := ledger.Record("next", "next", "", ""); err != nil || !changed || next.Sequence != 1 {
		t.Fatalf("post-Clear Record = %#v changed=%v err=%v", next, changed, err)
	}
}
