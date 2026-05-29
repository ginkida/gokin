package memory

import (
	"testing"
)

func newTestProjectLearning(t *testing.T) *ProjectLearning {
	t.Helper()
	pl, err := NewProjectLearning(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	// Flush cancels the 2s debounced save timer so it can't fire into
	// t.TempDir during cleanup ("directory not empty").
	t.Cleanup(func() { _ = pl.Flush() })
	return pl
}

func TestProjectLearningLearnCommand(t *testing.T) {
	pl := newTestProjectLearning(t)

	// First observation of a new command: a success keeps the rate at 1.0,
	// usage count increments, and avg duration seeds to the first sample.
	pl.LearnCommand("go test ./...", "run tests", true, 1200)
	cmds := pl.GetFrequentCommands(0)
	if len(cmds) != 1 {
		t.Fatalf("after one LearnCommand, GetFrequentCommands = %d, want 1", len(cmds))
	}
	c := cmds[0]
	if c.UsageCount != 1 {
		t.Errorf("UsageCount = %d, want 1", c.UsageCount)
	}
	if c.SuccessRate != 1.0 {
		t.Errorf("SuccessRate after first success = %v, want 1.0", c.SuccessRate)
	}
	if c.AvgDuration != 1200 {
		t.Errorf("AvgDuration after first sample = %v, want 1200", c.AvgDuration)
	}

	// A failure must lower the success rate; usage count keeps climbing.
	pl.LearnCommand("go test ./...", "", false, 0)
	c = pl.GetFrequentCommands(0)[0]
	if c.UsageCount != 2 {
		t.Errorf("UsageCount after 2 calls = %d, want 2", c.UsageCount)
	}
	if c.SuccessRate >= 1.0 {
		t.Errorf("SuccessRate after a failure = %v, want < 1.0", c.SuccessRate)
	}
}

func TestProjectLearningGetFrequentCommandsOrderingAndLimit(t *testing.T) {
	pl := newTestProjectLearning(t)
	pl.LearnCommand("rare", "", true, 0)
	for range 3 {
		pl.LearnCommand("common", "", true, 0)
	}

	all := pl.GetFrequentCommands(0)
	if len(all) != 2 {
		t.Fatalf("GetFrequentCommands(0) = %d commands, want 2", len(all))
	}
	if all[0].Command != "common" {
		t.Errorf("most frequent = %q, want %q", all[0].Command, "common")
	}

	if got := pl.GetFrequentCommands(1); len(got) != 1 || got[0].Command != "common" {
		t.Errorf("GetFrequentCommands(1) = %+v, want [common]", got)
	}
}

func TestProjectLearningGetSuccessfulCommandsRequiresTwoUses(t *testing.T) {
	pl := newTestProjectLearning(t)
	// Used once at a perfect rate — excluded (UsageCount >= 2 is required so a
	// single lucky run doesn't masquerade as a reliable command).
	pl.LearnCommand("once", "", true, 0)
	// Used twice, both successes — included.
	pl.LearnCommand("twice", "", true, 0)
	pl.LearnCommand("twice", "", true, 0)

	got := pl.GetSuccessfulCommands(0.8)
	if len(got) != 1 || got[0].Command != "twice" {
		t.Fatalf("GetSuccessfulCommands(0.8) = %+v, want only [twice]", got)
	}
}

func TestProjectLearningLearnFileTypeSmoke(t *testing.T) {
	pl := newTestProjectLearning(t)
	// FileTypes has no public getter; this exercises the learn path (dedup of
	// conventions happens internally) and confirms it persists without error.
	pl.LearnFileType("go", []string{"gofmt", "table tests"})
	pl.LearnFileType("go", []string{"table tests", "race detector"}) // 1 dup, 1 new
	if err := pl.Flush(); err != nil {
		t.Fatalf("Flush after LearnFileType: %v", err)
	}
	if !pl.Exists() {
		t.Error("learning file not written after Flush")
	}
}

func TestProjectLearningPreferences(t *testing.T) {
	pl := newTestProjectLearning(t)
	if got := pl.GetPreference("missing"); got != "" {
		t.Errorf("GetPreference(missing) = %q, want empty", got)
	}
	pl.SetPreference("editor", "vim")
	pl.SetPreference("shell", "zsh")
	if got := pl.GetPreference("editor"); got != "vim" {
		t.Errorf("GetPreference(editor) = %q, want vim", got)
	}

	prefs := pl.GetPreferences()
	if prefs["editor"] != "vim" || prefs["shell"] != "zsh" {
		t.Errorf("GetPreferences = %v, missing entries", prefs)
	}
	// Must be a copy — mutating the result must not affect the store.
	prefs["editor"] = "emacs"
	if pl.GetPreference("editor") != "vim" {
		t.Error("GetPreferences returned a live map, not a copy")
	}
}

func TestProjectLearningPatternsByTagAndRecent(t *testing.T) {
	pl := newTestProjectLearning(t)
	pl.LearnPattern("retry", "retry with backoff", []string{"ex1"}, []string{"Reliability", "net"})
	pl.LearnPattern("cache", "memoize results", []string{"ex2"}, []string{"perf"})

	// Tag match is case-insensitive.
	tagged := pl.GetPatternsByTag("reliability")
	if len(tagged) != 1 || tagged[0].Name != "retry" {
		t.Fatalf("GetPatternsByTag(reliability) = %+v, want [retry]", tagged)
	}
	if got := pl.GetPatternsByTag("nope"); len(got) != 0 {
		t.Errorf("GetPatternsByTag(nope) = %d, want 0", len(got))
	}

	recent := pl.GetRecentPatterns(1)
	if len(recent) != 1 {
		t.Fatalf("GetRecentPatterns(1) = %d, want 1", len(recent))
	}
}

func TestProjectLearningHasContentAndExists(t *testing.T) {
	pl := newTestProjectLearning(t)
	if pl.HasContent() {
		t.Error("fresh store HasContent() = true, want false")
	}
	if pl.Exists() {
		t.Error("fresh store Exists() = true before any Flush, want false")
	}

	pl.SetPreference("editor", "vim")
	if !pl.HasContent() {
		t.Error("after SetPreference HasContent() = false, want true")
	}

	if err := pl.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if !pl.Exists() {
		t.Error("after Flush Exists() = false, want true")
	}
	if pl.Path() == "" {
		t.Error("Path() empty")
	}
}
