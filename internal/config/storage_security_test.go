package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWriteConfigFileRejectsOversizedDataBeforeCreatingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.yaml")
	err := WriteConfigFile(path, []byte(strings.Repeat("x", int(MaxConfigFileBytes)+1)))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("WriteConfigFile oversized error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); !os.IsNotExist(statErr) {
		t.Fatalf("oversized write created parent directory: %v", statErr)
	}
}

func TestUpdateConfigFileSerializesConcurrentReadModifyWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const writers = 40
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := UpdateConfigFile(path, func(existing []byte) ([]byte, error) {
				root := map[string]any{}
				if len(existing) > 0 {
					if err := yaml.Unmarshal(existing, &root); err != nil {
						return nil, err
					}
				}
				root[fmt.Sprintf("writer_%02d", i)] = i
				return yaml.Marshal(root)
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("UpdateConfigFile: %v", err)
		}
	}

	data, err := ReadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if len(root) != writers {
		t.Fatalf("concurrent config retained %d keys, want %d", len(root), writers)
	}
}
