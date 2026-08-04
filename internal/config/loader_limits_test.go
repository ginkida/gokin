package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromFileRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxConfigFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	err = loadFromFile(DefaultConfig(), path)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("loadFromFile oversized error = %v", err)
	}
}

func TestLoadFromFileBoundsEnvironmentExpansion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("HOME", strings.Repeat("h", 1024))
	repetitions := maxExpandedConfigBytes/1024 + 1
	content := "model:\n  name: \"" + strings.Repeat("${HOME}", repetitions) + "\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	err := loadFromFile(DefaultConfig(), path)
	if err == nil || !strings.Contains(err.Error(), "expanded") || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("loadFromFile expanded error = %v", err)
	}
}
