package inventory

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthy(t *testing.T) {
	if !Healthy() {
		t.Fatal("inventory should be healthy")
	}
}

func TestProductionSourceDistribution(t *testing.T) {
	counts := map[string]int{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "generated" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		switch extension := filepath.Ext(path); extension {
		case ".go", ".py", ".ts":
			counts[extension]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{".go": 1, ".py": 2, ".ts": 3}
	for extension, count := range want {
		if counts[extension] != count {
			t.Fatalf("source counts = %v, want %v", counts, want)
		}
	}
}
