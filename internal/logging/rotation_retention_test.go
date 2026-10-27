package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotationRetainsSessionBeginningAndRecentFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writer := newRotatingFileWriter(file, path, 0, 64)
	t.Cleanup(func() { _ = writer.Close() })
	for i := range 30 {
		record := strings.Repeat(string(rune('A'+i)), 48) + "\n"
		if _, err := writer.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
	}
	start, err := os.ReadFile(path + ".start")
	if err != nil || !strings.HasPrefix(string(start), "AAAA") {
		t.Fatalf("session beginning lost: %q, %v", start, err)
	}
	previous, err := os.ReadFile(path + ".old.1")
	if err != nil || len(previous) == 0 {
		t.Fatalf("older rotated log lost: %v", err)
	}
}

func TestRotationRetentionIsBoundedAndPrivate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writer := newRotatingFileWriter(file, path, 0, 64)
	defer writer.Close()
	for i := range 50 {
		if _, err := writer.Write([]byte(fmt.Sprintf("%02d %s\n", i, strings.Repeat("x", 40)))); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxLogBackups+2 {
		t.Fatalf("retained files=%d, want %d", len(entries), maxLogBackups+2)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 || info.Size() > 64 {
			t.Fatalf("segment %s: mode=%v bytes=%d", entry.Name(), info.Mode(), info.Size())
		}
		total += info.Size()
	}
	if total > 64*(maxLogBackups+2) {
		t.Fatalf("retention exceeded cap: %d", total)
	}
	latest, err := os.ReadFile(path + ".old")
	if err != nil || !strings.HasPrefix(string(latest), "48 ") {
		t.Fatalf("latest backup=%q, %v", latest, err)
	}
}

func TestRotationPinsNewSessionBeginning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	for _, session := range []string{"first", "second"} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		writer := newRotatingFileWriter(file, path, info.Size(), 64)
		if _, err := writer.Write([]byte(session + " starts\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(strings.Repeat("x", 60) + "\n")); err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
		pinned, err := os.ReadFile(path + ".start")
		if err != nil || !strings.Contains(string(pinned), session+" starts") {
			t.Fatalf("session %s beginning lost: %q, %v", session, pinned, err)
		}
	}
}

func TestRotationPinsCurrentSessionAfterReopen(t *testing.T) {
	for _, previousSize := range []int{0, 63, 64, 65} {
		t.Run(fmt.Sprint(previousSize), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.jsonl")
			if err := os.WriteFile(path, []byte(strings.Repeat("p", previousSize)), 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			writer := newRotatingFileWriter(file, path, int64(previousSize), 64)
			defer writer.Close()
			if _, err := writer.Write([]byte("CURRENT SESSION START\n")); err != nil {
				t.Fatal(err)
			}
			for range 30 {
				if _, err := writer.Write([]byte(strings.Repeat("x", 48) + "\n")); err != nil {
					t.Fatal(err)
				}
			}
			pinned, err := os.ReadFile(path + ".start")
			if err != nil || !strings.Contains(string(pinned), "CURRENT SESSION START") {
				t.Fatalf("pinned previous session instead of current beginning: %q, %v", pinned, err)
			}
		})
	}
}
