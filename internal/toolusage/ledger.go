// Package toolusage keeps a lifetime count of how often each tool is actually
// invoked, so "is this subsystem earning its place?" can be answered with data
// instead of intuition.
//
// The existing ToolMetrics collector already counts calls, but it lives in
// memory and is reset by /clear, which makes it a within-session instrument.
// The question it cannot answer is the one that decides whether a feature ships
// or gets a tombstone: has anyone reached for this at all, over weeks. Without
// retention that decision defaults to "leave it", which is how a subsystem
// quietly accumulates maintenance cost it never repays.
//
// Deliberately global rather than per-workspace: the question is about the tool
// surface, not about one repository. Nothing here is ever resumed or executed,
// so the workspace-ownership rule that governs sessions, checkpoints and loops
// does not apply. Only tool names and counts are stored — never arguments.
package toolusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gokin/internal/fileutil"
)

// flushEveryNRecords bounds how much is lost to a crash without putting a write
// on every tool call. The file is a few kilobytes, and a tool execution costs
// milliseconds at best, so one small atomic write per this many calls is noise.
const flushEveryNRecords = 25

// Ledger is a lifetime per-tool invocation counter backed by a JSON file.
// The zero value is unusable; construct with NewLedger. A nil *Ledger is a safe
// no-op on every method so callers never need to branch.
type Ledger struct {
	mu            sync.Mutex
	path          string
	counts        map[string]int64
	sinceFlush    int
	loadFailed    bool
	pendingWrites bool
}

type ledgerFile struct {
	Counts map[string]int64 `json:"counts"`
}

// NewLedger loads the ledger at path. A missing file is an empty ledger; a
// corrupt one starts empty and is overwritten on the next flush rather than
// failing construction, because losing usage history must never be able to stop
// the app from starting.
func NewLedger(path string) *Ledger {
	l := &Ledger{path: path, counts: map[string]int64{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	var parsed ledgerFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		l.loadFailed = true
		return l
	}
	for name, count := range parsed.Counts {
		if name != "" && count > 0 {
			l.counts[name] = count
		}
	}
	return l
}

// Record counts one invocation. Errors from the periodic flush are swallowed on
// purpose: telemetry must never interfere with the tool call that produced it.
func (l *Ledger) Record(name string) {
	if l == nil || name == "" {
		return
	}
	l.mu.Lock()
	l.counts[name]++
	l.pendingWrites = true
	l.sinceFlush++
	due := l.sinceFlush >= flushEveryNRecords
	if due {
		l.sinceFlush = 0
	}
	l.mu.Unlock()
	if due {
		_ = l.Flush()
	}
}

// Snapshot returns a copy of the lifetime counts.
func (l *Ledger) Snapshot() map[string]int64 {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int64, len(l.counts))
	for name, count := range l.counts {
		out[name] = count
	}
	return out
}

// NeverUsed returns the members of known that carry no recorded invocation,
// sorted. It is the actionable half of the ledger: a name here has never been
// chosen by any model in any session on this machine.
//
// The caller supplies the known set because the ledger deliberately does not
// import the registry — this stays a leaf package.
func (l *Ledger) NeverUsed(known []string) []string {
	counts := l.Snapshot()
	var unused []string
	for _, name := range known {
		if counts[name] == 0 {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	return unused
}

// Flush writes the ledger if anything changed since the last write. Callers on
// the shutdown path should invoke it so the final calls of a session are not
// lost between periodic flushes.
func (l *Ledger) Flush() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if !l.pendingWrites || l.path == "" {
		l.mu.Unlock()
		return nil
	}
	data, err := json.MarshalIndent(ledgerFile{Counts: l.counts}, "", "  ")
	if err != nil {
		l.mu.Unlock()
		return err
	}
	l.pendingWrites = false
	path := l.path
	l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		l.markDirty()
		return err
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		l.markDirty()
		return err
	}
	return nil
}

// markDirty restores the pending flag after a failed write so the next flush
// retries instead of silently dropping the counts it was carrying.
func (l *Ledger) markDirty() {
	l.mu.Lock()
	l.pendingWrites = true
	l.mu.Unlock()
}
