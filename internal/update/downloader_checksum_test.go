package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseChecksumFile(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())

	content := `# a comment line
ABCDEF0123  gokin-linux-amd64.tar.gz
0011aa22 gokin-darwin-arm64.tar.gz
deadbeef *gokin-windows-amd64.zip

` // trailing blank + comment exercised
	got := d.parseChecksumFile(content)

	// Checksums are lowercased; the leading '*' (binary-mode flag) is stripped.
	if got["gokin-linux-amd64.tar.gz"] != "abcdef0123" {
		t.Errorf("linux checksum = %q, want lowercased abcdef0123", got["gokin-linux-amd64.tar.gz"])
	}
	if got["gokin-darwin-arm64.tar.gz"] != "0011aa22" {
		t.Errorf("darwin checksum = %q", got["gokin-darwin-arm64.tar.gz"])
	}
	if got["gokin-windows-amd64.zip"] != "deadbeef" {
		t.Errorf("windows checksum = %q (the '*' must be stripped from the filename key)", got["gokin-windows-amd64.zip"])
	}
	// Comments and blank lines are skipped.
	if len(got) != 3 {
		t.Errorf("parsed %d entries, want 3 (comments/blanks skipped): %v", len(got), got)
	}
}

func TestComputeAndVerifyChecksum(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())
	f := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(f, []byte("hello gokin"), 0o644); err != nil {
		t.Fatal(err)
	}

	sum, err := d.ComputeChecksum(f)
	if err != nil {
		t.Fatalf("ComputeChecksum: %v", err)
	}
	if len(sum) != 64 {
		t.Fatalf("SHA256 hex length = %d, want 64", len(sum))
	}

	// Round-trip: the computed checksum verifies.
	if err := d.VerifyChecksum(f, sum); err != nil {
		t.Errorf("VerifyChecksum(self) = %v, want nil", err)
	}
	// Case-insensitive (hex is compared with EqualFold).
	if err := d.VerifyChecksum(f, uppercase(sum)); err != nil {
		t.Errorf("VerifyChecksum(uppercased) = %v, want nil", err)
	}
	// A wrong checksum is a mismatch.
	if err := d.VerifyChecksum(f, "00"+sum[2:]); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("VerifyChecksum(wrong) = %v, want ErrChecksumMismatch", err)
	}
	// An empty expected checksum must FAIL closed, not pass.
	if err := d.VerifyChecksum(f, ""); err == nil {
		t.Error("VerifyChecksum(empty) = nil, want mismatch (fail closed)")
	}
	// A missing file is an error, not a panic.
	if err := d.VerifyChecksum(filepath.Join(t.TempDir(), "nope.bin"), sum); err == nil {
		t.Error("VerifyChecksum(missing file) = nil, want error")
	}
}

func uppercase(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}
