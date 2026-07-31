package ui

import (
	"strings"
	"testing"
)

func TestMarkdownRenderingCanBeDisabledAndReenabled(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.SetMarkdownRendering(false)
	output.AppendTextStream("## Raw heading\n**raw bold**\n")
	output.FlushStream()

	plain := stripAnsi(output.Content())
	for _, syntax := range []string{"## Raw heading", "**raw bold**"} {
		if !strings.Contains(plain, syntax) {
			t.Fatalf("disabled Markdown rendering lost raw syntax %q:\n%s", syntax, plain)
		}
	}

	output.Clear()
	output.SetMarkdownRendering(true)
	output.AppendTextStream("## Rendered heading\n**rendered bold**\n")
	output.FlushStream()

	plain = stripAnsi(output.Content())
	for _, syntax := range []string{"## Rendered heading", "**rendered bold**"} {
		if strings.Contains(plain, syntax) {
			t.Fatalf("re-enabled Markdown rendering retained syntax %q:\n%s", syntax, plain)
		}
	}
	for _, text := range []string{"Rendered heading", "rendered bold"} {
		if !strings.Contains(plain, text) {
			t.Fatalf("re-enabled Markdown rendering lost text %q:\n%s", text, plain)
		}
	}
}

func TestDisablingMarkdownFlushesBufferedStreamText(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.AppendTextStream("buffered without newline")

	output.SetMarkdownRendering(false)
	if got := stripAnsi(output.Content()); !strings.Contains(got, "buffered without newline") {
		t.Fatalf("mode switch discarded parser-buffered text: %q", got)
	}

	output.AppendTextStream(" + **raw continuation**")
	if got := stripAnsi(output.Content()); !strings.Contains(got, "**raw continuation**") {
		t.Fatalf("plaintext continuation was unexpectedly rendered: %q", got)
	}
}

func TestMarkdownSettingUsesCompleteConfigSnapshot(t *testing.T) {
	m := *NewModel()
	if !m.output.markdownRendering {
		t.Fatal("precondition: Markdown rendering should be enabled by default")
	}

	next, _ := m.Update(ConfigUpdateMsg{Settings: map[string]bool{"markdown": false}})
	m = next.(Model)
	if m.output.markdownRendering {
		t.Fatal("markdown=false in settings snapshot was not applied")
	}

	next, _ = m.Update(ConfigUpdateMsg{Settings: map[string]bool{"markdown": true}})
	m = next.(Model)
	if !m.output.markdownRendering {
		t.Fatal("markdown=true in settings snapshot was not applied")
	}
}
