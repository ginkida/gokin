package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
)

// fakeAppForInit only overrides GetWorkDir — InitCommand touches nothing else.
type fakeAppForInit struct {
	fakeAppForMCP
	workDir string
}

func (f *fakeAppForInit) GetWorkDir() string { return f.workDir }

// ─── formatTimeAgo ───────────────────────────────────────────────────────────

func TestFormatTimeAgo(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, "unknown"},
		{"just_now", time.Now(), "just now"},
		{"5m", time.Now().Add(-5 * time.Minute), "5m ago"},
		{"2h", time.Now().Add(-2 * time.Hour), "2h ago"},
		{"yesterday", time.Now().Add(-24 * time.Hour), "yesterday"},
		{"3d", time.Now().Add(-72 * time.Hour), "3d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTimeAgo(tc.t)
			switch tc.name {
			case "5m":
				if !strings.HasSuffix(got, "m ago") {
					t.Errorf("got %q, want *m ago", got)
				}
			case "2h":
				if !strings.HasSuffix(got, "h ago") {
					t.Errorf("got %q, want *h ago", got)
				}
			case "3d":
				if !strings.HasSuffix(got, "d ago") {
					t.Errorf("got %q, want *d ago", got)
				}
			default:
				if got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// Note: formatCompactionResult is already covered by compact_test.go
// (TestFormatCompactionResult + TestFormatCompactionResult_NoOpSentinelsAreNotErrors
// + TestFormatCompactionResult_RealFailureIsNotANoOp). Do not re-declare here.

// ─── InitCommand.detectTemplate ──────────────────────────────────────────────

func TestInitCommand_DetectTemplate(t *testing.T) {
	dir := t.TempDir()
	c := &InitCommand{}

	// Go
	goMod := filepath.Join(dir, "go.mod")
	mustWrite(t, goMod, "module test")
	if tpl := c.detectTemplate(dir); !strings.Contains(tpl, "go build") || !strings.Contains(tpl, "go test") {
		t.Errorf("Go template: %q", tpl)
	}
	os.Remove(goMod)

	// Node
	pkgJSON := filepath.Join(dir, "package.json")
	mustWrite(t, pkgJSON, "{}")
	if tpl := c.detectTemplate(dir); !strings.Contains(tpl, "npm test") {
		t.Errorf("Node template: %q", tpl)
	}
	os.Remove(pkgJSON)

	// Python
	reqFile := filepath.Join(dir, "requirements.txt")
	mustWrite(t, reqFile, "flask")
	if tpl := c.detectTemplate(dir); !strings.Contains(tpl, "pytest") {
		t.Errorf("Python template: %q", tpl)
	}
	os.Remove(reqFile)

	// Rust
	cargoFile := filepath.Join(dir, "Cargo.toml")
	mustWrite(t, cargoFile, "[package]")
	if tpl := c.detectTemplate(dir); !strings.Contains(tpl, "cargo build") {
		t.Errorf("Rust template: %q", tpl)
	}
	os.Remove(cargoFile)

	// Generic fallback
	if tpl := c.detectTemplate(dir); !strings.Contains(tpl, "Project Overview") {
		t.Errorf("Generic template: %q", tpl)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ─── InitCommand.Execute ─────────────────────────────────────────────────────

func TestInitCommand_Execute_Success(t *testing.T) {
	dir := t.TempDir()
	app := &fakeAppForInit{workDir: dir}
	out, err := (&InitCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Created GOKIN.md") {
		t.Errorf("success message: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "GOKIN.md")); err != nil {
		t.Errorf("GOKIN.md not created: %v", err)
	}
}

func TestInitCommand_Execute_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	gokinPath := filepath.Join(dir, "GOKIN.md")
	mustWrite(t, gokinPath, "existing")
	app := &fakeAppForInit{workDir: dir}
	out, err := (&InitCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No-op: GOKIN.md already exists") {
		t.Errorf("exists message: %q", out)
	}
	data, _ := os.ReadFile(gokinPath)
	if string(data) != "existing" {
		t.Errorf("file overwritten: %q", data)
	}
}

func TestInitCommand_Metadata(t *testing.T) {
	c := &InitCommand{}
	if c.Name() != "init" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage empty")
	}
	md := c.GetMetadata()
	if md.Category != CategoryGit {
		t.Errorf("metadata = %+v", md)
	}
}

// ─── getDataDir ──────────────────────────────────────────────────────────────

func TestGetDataDir_XDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	got, err := getDataDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "gokin")
	if got != want {
		t.Errorf("XDG: got %q, want %q", got, want)
	}
}

func TestGetDataDir_Fallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	got, err := getDataDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "gokin")
	if got != want {
		t.Errorf("fallback: got %q, want %q", got, want)
	}
}

// ─── runtimeProviderForConfig ────────────────────────────────────────────────

func TestRuntimeProviderForConfig_Nil(t *testing.T) {
	if got := runtimeProviderForConfig(nil); got != "" {
		t.Errorf("nil: got %q, want empty", got)
	}
}

func TestRuntimeProviderForConfig_Explicit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Model.Provider = "glm"
	if got := runtimeProviderForConfig(cfg); got != "glm" {
		t.Errorf("explicit: got %q, want glm", got)
	}
}

func TestRuntimeProviderForConfig_Auto(t *testing.T) {
	cfg := &config.Config{}
	cfg.Model.Provider = "auto"
	cfg.Model.Name = "deepseek-v4-pro"
	got := runtimeProviderForConfig(cfg)
	// DetectKnownProviderFromModel should pick "deepseek"; accept either that
	// or empty if detection rules change.
	if got != "" && got != "deepseek" {
		t.Errorf("auto+model: got %q, want deepseek or empty", got)
	}
}

// ─── getCommandExample ───────────────────────────────────────────────────────

func TestGetCommandExample_Known(t *testing.T) {
	got := getCommandExample("save")
	if !strings.Contains(got, "/save") {
		t.Errorf("save example: %q", got)
	}
}

func TestGetCommandExample_Unknown(t *testing.T) {
	if got := getCommandExample("nonexistent-cmd-xyz"); got != "" {
		t.Errorf("unknown: got %q, want empty", got)
	}
}

// ─── getRelatedCommands ──────────────────────────────────────────────────────

func TestGetRelatedCommands(t *testing.T) {
	cases := []struct {
		name     string
		contains string
	}{
		{"compact", "/clear"},
		{"stats", "/cost"},
		{"memory-governance", "/memory"},
		{"health", "/stats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getRelatedCommands(tc.name)
			if !strings.Contains(got, tc.contains) {
				t.Errorf("related(%s) = %q, want to contain %q", tc.name, got, tc.contains)
			}
		})
	}
}

func TestGetRelatedCommands_Unknown(t *testing.T) {
	if got := getRelatedCommands("nonexistent"); got != "" {
		t.Errorf("unknown: got %q, want empty", got)
	}
}
