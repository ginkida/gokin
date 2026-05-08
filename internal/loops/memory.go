package loops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gokin/internal/fileutil"
)

// MemoryWriter persists loop state to human-readable markdown files
// inside the project's `.gokin/loops/` directory. One file per loop,
// regenerated from the in-memory state on every iteration.
//
// The file is intended for the user to grep / open in their editor
// when they want to know what a long-running loop has been doing
// without invoking `/loop status` in the TUI. It is NOT auto-loaded
// into the agent's system prompt — that decision is left to the user
// (they can `cat .gokin/loops/<id>.md >> .gokin/project-memory.md`
// if they want loop context in the main agent's working memory).
//
// Why a per-loop file rather than appending to project-memory.md:
//
//  1. Isolation — a runaway loop can't pollute the main project
//     memory, which is loaded into every prompt.
//  2. Cleanup — deleting a loop is one rm; finding and surgically
//     removing per-loop entries from a shared file is fragile.
//  3. Human-friendly — `ls .gokin/loops/` shows what's running and
//     for how long without parsing.
type MemoryWriter struct {
	workDir string
}

// NewMemoryWriter creates a writer rooted at <workDir>/.gokin/loops/.
// Returns nil when workDir is empty — callers that don't have a stable
// project directory just skip the memory write entirely.
func NewMemoryWriter(workDir string) *MemoryWriter {
	if strings.TrimSpace(workDir) == "" {
		return nil
	}
	return &MemoryWriter{workDir: workDir}
}

// WriteLoop renders the loop's full state as markdown and atomically
// writes it to <workDir>/.gokin/loops/<loop_id>.md. Idempotent — safe
// to call repeatedly with the same loop (each call regenerates the
// whole file from current state).
//
// The directory is created on demand. Errors are returned but typically
// non-fatal at the call site — the loop iteration result is already
// safely persisted in the manager's JSON state file; the markdown is a
// human-readable convenience, not the source of truth.
//
// File permissions match the JSON storage (0700 dir, 0600 file): the
// markdown can contain agent outputs, task descriptions, and
// fragments of files the agent read — same sensitivity class as the
// underlying state, so don't make it world-readable.
func (w *MemoryWriter) WriteLoop(l *Loop) error {
	if w == nil || l == nil {
		return nil
	}
	dir := filepath.Join(w.workDir, ".gokin", "loops")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("loops memory: create dir: %w", err)
	}
	path := filepath.Join(dir, l.ID+".md")
	content := RenderLoopMarkdown(l)
	return fileutil.AtomicWrite(path, []byte(content), 0600)
}

// DeleteLoop removes the loop's markdown file. Called when a loop is
// removed via /loop remove so we don't leak stale files. Idempotent —
// missing file is not an error.
func (w *MemoryWriter) DeleteLoop(loopID string) error {
	if w == nil || loopID == "" {
		return nil
	}
	path := filepath.Join(w.workDir, ".gokin", "loops", loopID+".md")
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loops memory: delete %s: %w", loopID, err)
	}
	return nil
}

// RenderLoopMarkdown formats a loop as human-readable markdown.
// Exported so tests can pin the format and the future TUI panel can
// reuse the same renderer for in-app display.
//
// Format (structured for both human reading and basic grep-ability):
//
//	# Loop <id>
//	**Task:** <task>
//	**Mode:** <mode>
//	**Status:** <status>
//	...
//	## Recent iterations
//	### #N — <timestamp> (<duration>) <ok-marker>
//	<summary>
//	...
func RenderLoopMarkdown(l *Loop) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Loop %s\n\n", l.ID)
	fmt.Fprintf(&sb, "**Task:** %s\n\n", l.Task)
	fmt.Fprintf(&sb, "**Mode:** %s\n", renderMode(l))
	fmt.Fprintf(&sb, "**Status:** %s\n", l.Status)
	fmt.Fprintf(&sb, "**Created:** %s\n", l.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	if !l.LastRunAt.IsZero() {
		fmt.Fprintf(&sb, "**Last run:** %s\n", l.LastRunAt.Format("2006-01-02 15:04:05 MST"))
	}
	if l.IsActive() && !l.NextRunAt.IsZero() {
		fmt.Fprintf(&sb, "**Next run:** %s\n", l.NextRunAt.Format("2006-01-02 15:04:05 MST"))
	}
	fmt.Fprintf(&sb, "**Iterations:** %d", l.IterationCount)
	if l.MaxIterations > 0 {
		fmt.Fprintf(&sb, " / %d", l.MaxIterations)
	}
	if l.IterationCount > 0 {
		fmt.Fprintf(&sb, " (%d ✓ / %d ✗)", l.SuccessCount, l.FailureCount)
	}
	sb.WriteString("\n\n")

	if len(l.Iterations) == 0 {
		sb.WriteString("_No iterations yet._\n")
		return sb.String()
	}

	sb.WriteString("## Recent iterations (newest first)\n\n")
	// Iterate in reverse — readers usually want the latest first.
	for i := len(l.Iterations) - 1; i >= 0; i-- {
		it := l.Iterations[i]
		marker := "✓"
		if !it.OK {
			marker = "✗"
		}
		fmt.Fprintf(&sb, "### #%d — %s (%s) %s\n",
			it.N,
			it.StartedAt.Format("2006-01-02 15:04:05 MST"),
			renderDuration(it.Duration.Seconds()),
			marker)
		summary := strings.TrimSpace(it.Summary)
		if summary == "" {
			summary = "_(no summary)_"
		}
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// renderMode formats the mode for the markdown header. Includes the
// interval for interval mode so the file is self-describing without
// needing to cross-reference the JSON state.
func renderMode(l *Loop) string {
	if l.Mode == ModeInterval {
		return fmt.Sprintf("interval (every %s)", renderDuration(float64(l.IntervalSeconds)))
	}
	if l.MinDelaySeconds > 0 && l.MinDelaySeconds != DefaultMinDelaySeconds {
		return fmt.Sprintf("self-paced (min %s between iterations)", renderDuration(float64(l.MinDelaySeconds)))
	}
	return "self-paced"
}

// renderDuration formats a seconds count as a short human label like
// "5m" / "1h30m" / "2d". Mirrors the formatDurationShort helper in
// the /loop command but operates on float seconds for arithmetic
// convenience.
func renderDuration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", int(seconds))
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", int(seconds/60))
	}
	if seconds < 86400 {
		h := int(seconds / 3600)
		m := int(seconds/60) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	d := int(seconds / 86400)
	h := int(seconds/3600) - d*24
	if h == 0 {
		return fmt.Sprintf("%dd", d)
	}
	return fmt.Sprintf("%dd%dh", d, h)
}
