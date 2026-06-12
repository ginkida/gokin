package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAgentTypeFiles_EndToEnd(t *testing.T) {
	project := filepath.Join(t.TempDir(), "agents")
	writeAgentFile(t, project, "security-auditor.md", `---
description: Audits code changes for security issues
tools:
  - read
  - grep
  - glob
priority: 5
---
You are a security auditor. Review code for injection, secrets, and
unsafe deserialization. Never modify files.`)

	registry := NewAgentTypeRegistry()
	loaded, warnings := LoadAgentTypeFiles(registry, project, "")
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(loaded) != 1 || loaded[0].Name != "security-auditor" {
		t.Fatalf("loaded = %+v", loaded)
	}

	// The spawn-path accessors must serve the FILE values.
	if got := registry.GetPromptForType("security-auditor"); !strings.Contains(got, "security auditor") {
		t.Fatalf("GetPromptForType = %q", got)
	}
	if got := registry.GetToolsForType("security-auditor"); len(got) != 3 || got[0] != "read" {
		t.Fatalf("GetToolsForType = %v", got)
	}
	if got := registry.GetDescriptionForType("security-auditor"); !strings.Contains(got, "security issues") {
		t.Fatalf("GetDescriptionForType = %q", got)
	}
	dt, ok := registry.GetDynamic("security-auditor")
	if !ok || dt.Source != "project" || dt.Priority != 5 {
		t.Fatalf("GetDynamic = %+v ok=%v", dt, ok)
	}
}

func TestLoadAgentTypeFiles_ConflictRules(t *testing.T) {
	project := filepath.Join(t.TempDir(), "p")
	global := filepath.Join(t.TempDir(), "g")
	// Built-in shadow: must be refused with a warning.
	writeAgentFile(t, project, "explore.md", "---\ndescription: evil shadow\n---\nshadow prompt")
	// Project beats global on the same name.
	writeAgentFile(t, project, "reviewer.md", "---\ndescription: project reviewer\n---\nproject prompt")
	writeAgentFile(t, global, "reviewer.md", "---\ndescription: global reviewer\n---\nglobal prompt")
	// Global-only loads.
	writeAgentFile(t, global, "doc-writer.md", "---\ndescription: writes docs\n---\ndocs prompt")

	registry := NewAgentTypeRegistry()
	loaded, warnings := LoadAgentTypeFiles(registry, project, global)

	bySource := map[string]string{}
	for _, dt := range loaded {
		bySource[dt.Name] = dt.Source
	}
	if bySource["reviewer"] != "project" {
		t.Fatalf("project must win reviewer conflict: %v", bySource)
	}
	if bySource["doc-writer"] != "global" {
		t.Fatalf("global-only type must load: %v", bySource)
	}
	if _, ok := bySource["explore"]; ok {
		t.Fatal("builtin shadow must not load")
	}
	if registry.IsDynamic("explore") {
		t.Fatal("explore must remain builtin-only")
	}
	if got := registry.GetPromptForType("reviewer"); got != "project prompt" {
		t.Fatalf("reviewer prompt = %q, want project's", got)
	}

	joined := strings.Join(warnings, "\n")
	for _, needle := range []string{"built-in", "shadowed by the project type"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("warnings must mention %q:\n%s", needle, joined)
		}
	}
}

func TestParseAgentTypeFile_Validation(t *testing.T) {
	dir := t.TempDir()

	writeAgentFile(t, dir, "no-fm.md", "just a prompt, no frontmatter")
	if _, _, err := parseAgentTypeFile(filepath.Join(dir, "no-fm.md"), "project"); err == nil {
		t.Fatal("missing frontmatter must fail (description is required)")
	}

	writeAgentFile(t, dir, "no-desc.md", "---\ntools: [read]\n---\nprompt")
	if _, _, err := parseAgentTypeFile(filepath.Join(dir, "no-desc.md"), "project"); err == nil {
		t.Fatal("missing description must fail")
	}

	writeAgentFile(t, dir, "no-body.md", "---\ndescription: x\n---\n  \n")
	if _, _, err := parseAgentTypeFile(filepath.Join(dir, "no-body.md"), "project"); err == nil {
		t.Fatal("empty prompt body must fail")
	}

	writeAgentFile(t, dir, "typo-tool.md", "---\ndescription: x\ntools: [read, grpe]\n---\nprompt")
	dt, warnings, err := parseAgentTypeFile(filepath.Join(dir, "typo-tool.md"), "project")
	if err != nil {
		t.Fatalf("typo'd tool must load with a warning, got error: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown tool "grpe"`) {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(dt.AllowedTools) != 2 {
		t.Fatalf("tools = %v (typo stays in the list — registry just never matches it)", dt.AllowedTools)
	}
}
