package ssh

import (
	"strings"
	"testing"
)

// TestCappedWriter_StopsGrowingAtLimit (round 4) pins the fix for unbounded
// SSH command-output buffering: SSHClient.Execute used to capture
// stdout/stderr into a plain strings.Builder with no size bound — the
// tools/ssh.go 30000-rune display truncation only ran AFTER the entire
// remote output had already been read into memory, so a remote command that
// streamed far more output than expected (a big log tail, a runaway process,
// a hostile/compromised SSH server) could grow this process's heap without
// bound before truncation ever applied. cappedWriter stops accumulating once
// its limit is reached, so the in-process buffer itself is bounded.
func TestCappedWriter_StopsGrowingAtLimit(t *testing.T) {
	w := &cappedWriter{limit: 100}

	n, err := w.Write([]byte(strings.Repeat("a", 60)))
	if err != nil || n != 60 {
		t.Fatalf("first write: n=%d err=%v, want n=60 err=nil", n, err)
	}
	if w.Len() != 60 {
		t.Fatalf("Len() = %d, want 60", w.Len())
	}

	// This write straddles the limit (60+60=120 > 100) — only the remaining
	// 40 bytes should be buffered, but the write must still report success
	// for its FULL length (60), not a short write, so the SSH library never
	// sees an error and aborts the remote command mid-stream.
	n, err = w.Write([]byte(strings.Repeat("b", 60)))
	if err != nil || n != 60 {
		t.Fatalf("straddling write: n=%d err=%v, want n=60 err=nil (must report success even though truncated)", n, err)
	}
	if w.Len() != 100 {
		t.Fatalf("Len() after straddling write = %d, want 100 (capped)", w.Len())
	}

	// Further writes past the cap are silently discarded — buffer size never
	// grows past the limit, however much more is written.
	n, err = w.Write([]byte(strings.Repeat("c", 1_000_000)))
	if err != nil || n != 1_000_000 {
		t.Fatalf("overflow write: n=%d err=%v, want n=1000000 err=nil (report success, discard silently)", n, err)
	}
	if w.Len() != 100 {
		t.Fatalf("Len() after overflow write = %d, want 100 (must stay capped, not grow with every write)", w.Len())
	}

	got := w.String()
	if len(got) != 100 {
		t.Fatalf("String() length = %d, want 100", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 60)) {
		t.Fatal("expected the first 60 bytes (the 'a' run) to be preserved verbatim")
	}
	if !strings.HasSuffix(got, strings.Repeat("b", 40)) {
		t.Fatal("expected exactly 40 of the straddling 'b' write's bytes to be kept (up to the cap)")
	}
}

// TestCappedWriter_UnderLimitBehavesLikePlainBuilder is the happy-path
// regression check — ordinary, well-under-cap output must round-trip exactly,
// matching the pre-fix strings.Builder behavior for normal command output.
func TestCappedWriter_UnderLimitBehavesLikePlainBuilder(t *testing.T) {
	w := &cappedWriter{limit: maxCapturedOutputBytes}
	msg := "total 0\ndrwxr-xr-x  2 user user 4096 Jan  1 00:00 .\n"
	n, err := w.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(msg) {
		t.Fatalf("n = %d, want %d", n, len(msg))
	}
	if w.String() != msg {
		t.Fatalf("String() = %q, want %q", w.String(), msg)
	}
}

// TestCappedWriter_MultipleSmallWritesAccumulateCorrectly exercises the
// common SSH streaming pattern (many small chunk writes) to confirm the cap
// still triggers correctly across many calls, not just a single big write.
func TestCappedWriter_MultipleSmallWritesAccumulateCorrectly(t *testing.T) {
	w := &cappedWriter{limit: 50}
	total := 0
	for i := 0; i < 20; i++ {
		chunk := strings.Repeat("x", 10)
		n, err := w.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		total += n
	}
	if total != 200 {
		t.Fatalf("reported total written = %d, want 200 (20 writes of 10 bytes each, all reported as fully written)", total)
	}
	if w.Len() != 50 {
		t.Fatalf("Len() = %d, want 50 (capped regardless of how many small writes it took to get there)", w.Len())
	}
}
