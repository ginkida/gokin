package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/skills"
	"gokin/internal/tools"
)

func TestSkillCommandListsAndSubmitsExplicitWorkflow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "release", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: release\ndescription: Release safely\ndisable-model-invocation: true\n---\nFirst=$1\nSecond=$2\nAll=$ARGUMENTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}})
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewSkillToolWithCatalog(catalog))
	app := &fakeAppForMCP{registry: registry}
	command := &SkillCommand{}

	listed, err := command.Execute(context.Background(), nil, app)
	if err != nil || !strings.Contains(listed, "release") {
		t.Fatalf("list = %q err=%v", listed, err)
	}
	loaded, err := command.Execute(context.Background(), []string{"release", "hello world", "second"}, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{PromptMarker, "First=hello world", "Second=second", `All="hello world" second`} {
		if !strings.Contains(loaded, want) {
			t.Fatalf("loaded command result missing %q: %q", want, loaded)
		}
	}
	if !strings.HasPrefix(loaded, PromptMarker) {
		t.Fatalf("loaded command result = %q", loaded)
	}
}

func TestSkillCommandMissingRuntimeIsHonest(t *testing.T) {
	_, err := (&SkillCommand{}).Execute(context.Background(), nil, &fakeAppForMCP{})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestSkillCommandPreservesRawQuotedArguments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "raw", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	document := `---
name: Raw arguments
description: Verify lossless command handoff
---
Raw=$ARGUMENTS
First=$1
Second=$2
Third=$3`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}})))
	app := &fakeAppForMCP{registry: registry}
	input := `/skill raw '$HOME' pre"hello world"post "C:\Program Files"`
	handler := NewHandler()
	name, args, ok := handler.Parse(input)
	if !ok || name != "skill" {
		t.Fatalf("Parse(%q) = %q %#v %v", input, name, args, ok)
	}

	ctx := WithRawInvocation(context.Background(), input)
	loaded, err := (&SkillCommand{}).Execute(ctx, args, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Raw='$HOME' pre"hello world"post "C:\Program Files"`,
		"First=$HOME",
		"Second=prehello worldpost",
		`Third=C:\Program Files`,
	} {
		if !strings.Contains(loaded, want) {
			t.Fatalf("lossless command result missing %q:\n%s", want, loaded)
		}
	}
}
