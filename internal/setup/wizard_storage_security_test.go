package setup

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gokin/internal/config"
)

func TestSaveProviderConfigRejectsOversizedExistingFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "gokin", "config.yaml")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(config.MaxConfigFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := saveProviderConfig("glm", "test-key", "glm-5.2"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("saveProviderConfig oversized error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != config.MaxConfigFileBytes+1 {
		t.Fatalf("oversized existing config was replaced: size=%d", info.Size())
	}
}

func TestSaveProviderConfigRejectsOversizedResultBeforeCreatingDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	key := strings.Repeat("x", int(config.MaxConfigFileBytes)+1)
	if _, err := saveProviderConfig("glm", key, "glm-5.2"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("saveProviderConfig oversized result error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "gokin")); !os.IsNotExist(err) {
		t.Fatalf("oversized result created config directory: %v", err)
	}
}

func TestConcurrentProviderSavesPreserveEveryProviderKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	providers := []string{"glm", "kimi", "deepseek", "minimax"}
	var wg sync.WaitGroup
	errs := make(chan error, len(providers))
	for _, provider := range providers {
		provider := provider
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := saveProviderConfig(provider, "key-for-"+provider, "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("saveProviderConfig: %v", err)
		}
	}

	rootConfig, err := loadRawConfigOrEmpty(filepath.Join(root, "gokin", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := ensureMap(rootConfig, "api")
	for _, provider := range providers {
		if got := api[provider+"_key"]; got != "key-for-"+provider {
			t.Fatalf("%s key = %v", provider, got)
		}
	}
}
