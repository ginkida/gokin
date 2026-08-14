package catalog

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestReady(t *testing.T) {
	if !Ready() {
		t.Fatal("catalog should be ready")
	}
}

func TestConfigurationCatalogContract(t *testing.T) {
	var documents []map[string]any
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "scratch" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return err
		}
		documents = append(documents, document)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 4 {
		t.Fatalf("config documents = %d, want 4", len(documents))
	}
	common := make(map[string]bool)
	for key := range documents[0] {
		common[key] = true
	}
	minimum, maximum := 1e9, -1e9
	for _, document := range documents {
		for key := range common {
			if _, ok := document[key]; !ok {
				delete(common, key)
			}
		}
		timeout := document["timeout"].(float64)
		if timeout < minimum {
			minimum = timeout
		}
		if timeout > maximum {
			maximum = timeout
		}
	}
	keys := make([]string, 0, len(common))
	for key := range common {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"timeout"}) || minimum != 5 || maximum != 30 {
		t.Fatalf("universal keys/range = %v, %.0f..%.0f", keys, minimum, maximum)
	}
}
