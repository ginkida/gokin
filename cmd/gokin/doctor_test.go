package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorCommandRunsWithoutProviderInitialization(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
api:
  active_provider: ollama
  backend: ollama
model:
  name: mock-coder
  provider: ollama
`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldConfig, oldProvider, oldModel, oldBaseURL := cfgFile, provider, model, baseURL
	t.Cleanup(func() {
		cfgFile, provider, model, baseURL = oldConfig, oldProvider, oldModel, oldBaseURL
	})
	cfgFile, provider, model, baseURL = configPath, "", "", ""

	command := newDoctorCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"System Diagnostics",
		"Version:",
		"Backend: ollama",
		"Authentication not required",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("top-level doctor leaked ANSI escapes: %q", out.String())
	}
}

func TestDoctorCommandReportsMalformedConfigBeforeAppStartup(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(configPath, []byte("api: [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldConfig := cfgFile
	t.Cleanup(func() { cfgFile = oldConfig })
	cfgFile = configPath

	command := newDoctorCmd()
	command.SilenceUsage = true
	var out bytes.Buffer
	command.SetOut(&out)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unreadable configuration") {
		t.Fatalf("doctor malformed config error = %v", err)
	}
	if !strings.Contains(out.String(), "Configuration error:") ||
		!strings.Contains(out.String(), configPath) {
		t.Fatalf("doctor did not preserve configuration diagnosis:\n%s", out.String())
	}
}
