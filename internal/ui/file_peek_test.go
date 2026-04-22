package ui

import (
	"strings"
	"testing"
	"time"
)

// TestFilePeek_HiddenByDefault guards the invariant that a fresh panel
// doesn't draw anything. If a regression re-adds hero styling that shows
// something even without data, this test catches it.
func TestFilePeek_HiddenByDefault(t *testing.T) {
	p := NewFilePeekPanel(nil)
	if p.IsVisible() {
		t.Error("panel should not be visible before any ShowPeek call")
	}
	if out := p.View(120); out != "" {
		t.Errorf("hidden panel must render empty, got %q", out)
	}
}

// TestFilePeek_ShowAndExpireTTL encodes the core behaviour: the panel
// appears immediately when a peek arrives and disappears by itself after
// the TTL elapses. Earlier versions held the panel for 5s — that felt
// obstructive, especially on back-to-back reads, so we dropped it to
// ~1.5s. This test pins the new timing so it can't silently drift back up.
func TestFilePeek_ShowAndExpireTTL(t *testing.T) {
	p := NewFilePeekPanel(nil)
	p.ShowPeek(FilePeekMsg{FilePath: "foo.go", Action: "reading", Content: "hello"})

	if !p.IsVisible() {
		t.Fatal("panel should be visible immediately after ShowPeek")
	}

	// Force-expire and let Tick clear it.
	p.expires = time.Now().Add(-time.Millisecond)
	p.Tick()
	if p.IsVisible() {
		t.Error("panel should auto-hide once expiration passes")
	}

	if filePeekTTL > 3*time.Second {
		t.Errorf("filePeekTTL = %v is too long — user complained the old 5s was obtrusive", filePeekTTL)
	}
}

// TestFilePeek_SingleLine ensures the render output is compact. The earlier
// implementation drew a rounded-border block up to ~15 lines tall; users
// said that hid the agent's answer. The new version must stay a single
// visual line (no wrapping, no multi-line body).
func TestFilePeek_SingleLine(t *testing.T) {
	p := NewFilePeekPanel(nil)
	p.ShowPeek(FilePeekMsg{
		FilePath: "internal/tools/read.go",
		Action:   "reading",
		// Long content — old version would have rendered 10 of these lines.
		Content: strings.Repeat("a quite long line of code\n", 50),
	})

	out := p.View(120)
	if out == "" {
		t.Fatal("empty output for visible panel")
	}
	if strings.Count(out, "\n") > 0 {
		t.Errorf("output must stay on one line, got %d newlines: %q",
			strings.Count(out, "\n"), out)
	}
}

// TestFilePeek_FilenameProminent verifies the filename appears somewhere in
// the rendered output. The user's complaint was that the old panel "doesn't
// show normally what the agent is reading" — the filename was styled the
// same as 10 lines of file content, so it got lost visually.
func TestFilePeek_FilenameProminent(t *testing.T) {
	p := NewFilePeekPanel(nil)
	p.ShowPeek(FilePeekMsg{
		FilePath: "internal/tools/read.go",
		Action:   "reading",
		Content:  "package tools\n",
	})

	out := p.View(120)
	// shortenPath may collapse the prefix, but the basename must always be
	// visible — that's the primary signal.
	if !strings.Contains(out, "read.go") {
		t.Errorf("basename 'read.go' missing from output: %q", out)
	}
	if !strings.Contains(out, "Reading") {
		t.Errorf("action verb 'Reading' missing: %q", out)
	}
}

// TestFilePeek_NoFileContent guards a design invariant: we never leak a
// snippet of the file being read into the status line. Earlier versions
// rendered up to 10 lines of file content in the overlay — for secrets
// files or unfamiliar code that was both noisy and mildly concerning.
// The new compact design shows metadata only, never payload.
func TestFilePeek_NoFileContent(t *testing.T) {
	p := NewFilePeekPanel(nil)
	// Plant a recognisable sentinel in the content and assert it never
	// surfaces in the rendered line.
	sentinel := "SECRET_API_TOKEN_xyz42"
	p.ShowPeek(FilePeekMsg{
		FilePath: ".env",
		Action:   "reading",
		Content:  "DATABASE_URL=...\nAPI_TOKEN=" + sentinel + "\n",
	})

	out := p.View(120)
	if strings.Contains(out, sentinel) {
		t.Errorf("file content leaked into status line: %q", out)
	}
}

// TestFilePeek_MetaFormatting checks the "N lines · X KB" summary picks the
// right unit. Size thresholds are a bit fiddly — this nails them down.
func TestFilePeek_MetaFormatting(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", ""},
		{"small_bytes", "hi", "1 lines · 2 B"},
		{"kb", strings.Repeat("x", 2048), "1 lines · 2.0 KB"},
		{"mb", strings.Repeat("x", 2*1024*1024), "1 lines · 2.0 MB"},
		{"multiline", "a\nb\nc\n", "3 lines · 6 B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatPeekMeta(tc.content); got != tc.want {
				t.Errorf("formatPeekMeta(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestFilePeek_Hide explicitly tests the manual-hide path. Some callers
// want to dismiss early (e.g., when a more urgent modal opens).
func TestFilePeek_Hide(t *testing.T) {
	p := NewFilePeekPanel(nil)
	p.ShowPeek(FilePeekMsg{FilePath: "foo.go", Action: "reading"})
	p.Hide()
	if p.IsVisible() {
		t.Error("Hide() did not dismiss the panel")
	}
	if p.View(120) != "" {
		t.Error("View() should be empty after Hide()")
	}
}

// TestFilePeek_ActionIcon verifies the icon resolver reads from FilePeekIcons
// rather than hardcoding glyphs — so theming changes in styles.go take effect
// without touching file_peek.go. We don't pin specific glyphs (the project
// intentionally moved from emoji to minimal Unicode for terminal consistency)
// — we pin the *contract*:
//
//  1. Known actions return their mapped icon.
//  2. Unknown / empty actions fall back to FilePeekIcons["default"].
//  3. Case is normalized (lowercase).
func TestFilePeek_ActionIcon(t *testing.T) {
	if _, ok := FilePeekIcons["default"]; !ok {
		t.Fatal("FilePeekIcons must define a 'default' fallback")
	}

	for action, want := range FilePeekIcons {
		if action == "default" {
			continue
		}
		if got := actionIcon(action); got != want {
			t.Errorf("actionIcon(%q) = %q, want %q (from FilePeekIcons map)", action, got, want)
		}
		// Case-insensitivity: uppercase form must resolve to the same icon.
		if got := actionIcon(strings.ToUpper(action)); got != want {
			t.Errorf("actionIcon(%q upper) = %q, want %q (case should be normalized)", action, got, want)
		}
	}

	// Unknown and empty both fall back to default.
	fallback := FilePeekIcons["default"]
	for _, action := range []string{"", "garbage", "nonsense-action"} {
		if got := actionIcon(action); got != fallback {
			t.Errorf("actionIcon(%q) = %q, want default %q", action, got, fallback)
		}
	}
}

// TestFilePeek_ActionColor mirrors the icon test for color resolution.
// Unknown actions fall back to ColorMuted (quiet presence rather than alarm).
func TestFilePeek_ActionColor(t *testing.T) {
	for action, want := range FilePeekColors {
		if got := actionColor(action); got != want {
			t.Errorf("actionColor(%q) = %q, want %q", action, got, want)
		}
		if got := actionColor(strings.ToUpper(action)); got != want {
			t.Errorf("actionColor(%q upper) = %q, want %q (case should be normalized)", action, got, want)
		}
	}

	// Unknown must not crash and must not panic — return a muted fallback.
	got := actionColor("no-such-action")
	if got != ColorMuted {
		t.Errorf("actionColor(unknown) = %q, want ColorMuted (%q)", got, ColorMuted)
	}
}

// TestFilePeek_NarrowTerminalDoesNotPanic guards against a latent panic in
// shortenPath: earlier code computed pathBudget = width - 20 without
// clamping, and shortenPath's truncation math crashes on negative maxLen.
// Reproduces the crash scenario directly (tmux 4-column split pane, etc.).
func TestFilePeek_NarrowTerminalDoesNotPanic(t *testing.T) {
	p := NewFilePeekPanel(nil)
	p.ShowPeek(FilePeekMsg{
		FilePath: "/very/long/path/that/would/break/truncation/internal/tools/read.go",
		Action:   "reading",
		Content:  "abc",
	})

	for _, width := range []int{0, 1, 5, 10, 19, 20, 40} {
		// This should not panic for any width.
		out := p.View(width)
		if out == "" {
			t.Errorf("width=%d produced empty output even though panel is visible", width)
		}
	}
}

// TestFilePeek_ReshowResetsTTL guards against TTL exhaustion during a
// rapid back-to-back sequence. Each fresh ShowPeek must reset the timer
// so the *latest* peek gets its full TTL.
func TestFilePeek_ReshowResetsTTL(t *testing.T) {
	p := NewFilePeekPanel(nil)
	p.ShowPeek(FilePeekMsg{FilePath: "first.go", Action: "reading"})
	firstExpiry := p.expires

	time.Sleep(5 * time.Millisecond)
	p.ShowPeek(FilePeekMsg{FilePath: "second.go", Action: "reading"})
	if !p.expires.After(firstExpiry) {
		t.Error("second ShowPeek did not extend the expiration")
	}
	if p.peek.FilePath != "second.go" {
		t.Errorf("peek not updated to latest: got %q", p.peek.FilePath)
	}
}
