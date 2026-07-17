package update

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// CreateBackup derives the backup ID from a second-resolution timestamp.
// Two backups started within the same second must not share an ID — the
// second copy would silently overwrite the first rollback point, and the
// user-facing rollback handle would become ambiguous.
func TestCreateBackupSameSecondCollision(t *testing.T) {
	dir := t.TempDir()
	rm := NewRollbackManager(dir, 5)

	binaryPath := filepath.Join(dir, "gokin")
	if err := os.WriteFile(binaryPath, []byte("fake-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Pre-create colliding backups for the surrounding seconds so that
	// whichever second CreateBackup lands in, the base-ID file is taken.
	now := time.Now()
	for _, delta := range []time.Duration{-time.Second, 0, time.Second} {
		id := now.Add(delta).Format("20060102-150405")
		name := fmt.Sprintf("gokin-9.9.9-%s", id)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	info, err := rm.CreateBackup(binaryPath, "9.9.9")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	if matched := regexp.MustCompile(`-2$`).MatchString(info.ID); !matched {
		t.Errorf("backup ID %q does not carry the -2 disambiguation suffix", info.ID)
	}
	content, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("backup file missing at %q: %v", info.Path, err)
	}
	if string(content) != "fake-binary" {
		t.Errorf("backup content = %q, want the new binary (clobber check)", content)
	}
}

// Without a name collision the backup ID keeps the plain timestamp format —
// the common path is unchanged by the disambiguation logic.
func TestCreateBackupNoCollisionKeepsPlainID(t *testing.T) {
	dir := t.TempDir()
	rm := NewRollbackManager(dir, 5)

	binaryPath := filepath.Join(dir, "gokin")
	if err := os.WriteFile(binaryPath, []byte("fake-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	info, err := rm.CreateBackup(binaryPath, "1.2.3")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if matched := regexp.MustCompile(`^\d{8}-\d{6}$`).MatchString(info.ID); !matched {
		t.Errorf("backup ID %q does not match the plain timestamp format", info.ID)
	}
	if info.Version != "1.2.3" || info.BinaryPath != binaryPath {
		t.Errorf("backup info mismatch: %+v", info)
	}
	if info.Size != int64(len("fake-binary")) || info.Checksum == "" {
		t.Errorf("backup metadata incomplete: %+v", info)
	}
}
