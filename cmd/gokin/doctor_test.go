package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/repl"
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

func TestDoctorCommandSkipsREPLProbeWhenInvocationCapabilityExcludesIt(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
api:
  active_provider: ollama
  backend: ollama
model:
  name: mock-coder
  provider: ollama
engine:
  mode: hybrid
`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldConfig, oldProvider, oldModel, oldBaseURL := cfgFile, provider, model, baseURL
	oldCeiling, oldDenied, oldDeniedCompat := toolCeiling, deniedTools, deniedToolsCompat
	oldDetector := doctorREPLDetector
	t.Cleanup(func() {
		cfgFile, provider, model, baseURL = oldConfig, oldProvider, oldModel, oldBaseURL
		toolCeiling, deniedTools, deniedToolsCompat = oldCeiling, oldDenied, oldDeniedCompat
		doctorREPLDetector = oldDetector
	})
	cfgFile, provider, model, baseURL = configPath, "", "", ""
	toolCeiling, deniedTools, deniedToolsCompat = []string{"read"}, nil, nil
	doctorREPLDetector = func(context.Context, string) repl.Availability {
		t.Fatal("capability-excluded top-level doctor performed a REPL probe")
		return repl.Availability{}
	}

	command := newDoctorCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stateful REPL disabled by invocation policy") {
		t.Fatalf("doctor omitted invocation-disabled runtime:\n%s", out.String())
	}

	// A deny-only invocation reaches the same early startup decision even
	// though its final allow ceiling would otherwise be derived from whatever
	// remains in the registry.
	toolCeiling, deniedTools = nil, []string{"repl_exec"}
	out.Reset()
	command = newDoctorCmd()
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stateful REPL disabled by invocation policy") {
		t.Fatalf("doctor omitted deny-only disabled runtime:\n%s", out.String())
	}
}
