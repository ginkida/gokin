package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- GetSharedProjectLearning ---

func TestGetSharedProjectLearning_ReturnsCachedInstance(t *testing.T) {
	// Clear the cache to ensure a clean state
	projectLearningMu.Lock()
	for k := range projectLearningCache {
		delete(projectLearningCache, k)
	}
	projectLearningMu.Unlock()

	dir := t.TempDir()

	pl1, err := GetSharedProjectLearning(dir)
	if err != nil {
		t.Fatalf("GetSharedProjectLearning: %v", err)
	}
	pl2, err := GetSharedProjectLearning(dir)
	if err != nil {
		t.Fatalf("GetSharedProjectLearning (second): %v", err)
	}

	if pl1 != pl2 {
		t.Error("GetSharedProjectLearning should return the same cached instance for the same root")
	}
}

func TestGetSharedProjectLearning_DifferentRootsReturnDifferentInstances(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	pl1, _ := GetSharedProjectLearning(dir1)
	pl2, _ := GetSharedProjectLearning(dir2)

	if pl1 == pl2 {
		t.Error("different project roots should return different instances")
	}
}

// --- normalizeProjectRoot ---

func TestNormalizeProjectRoot(t *testing.T) {
	dir := t.TempDir()

	// Symlink test: create a symlink to dir and verify resolution
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)

	normalized := normalizeProjectRoot(link)
	expected := normalizeProjectRoot(dir)

	if normalized != expected {
		t.Errorf("normalizeProjectRoot(link) = %q, want %q (same as target)", normalized, expected)
	}
}

// --- LearnCommand failure path ---

func TestProjectLearning_LearnCommand_Failure(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnCommand("go test", "run tests", true, 100.0)
	pl.LearnCommand("go test", "", false, 200.0)

	cmds := pl.GetFrequentCommands(10)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].SuccessRate >= 1.0 {
		t.Errorf("after a failure, SuccessRate should be < 1.0, got %f", cmds[0].SuccessRate)
	}
}

func TestProjectLearning_LearnCommand_NewCommand(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnCommand("npm test", "", true, 0)

	cmds := pl.GetFrequentCommands(10)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Command != "npm test" {
		t.Errorf("Command = %q, want 'npm test'", cmds[0].Command)
	}
}

func TestProjectLearning_LearnCommand_UpdatesAvgDuration(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnCommand("make build", "", true, 100.0)
	pl.LearnCommand("make build", "", true, 200.0)

	cmds := pl.GetFrequentCommands(10)
	if cmds[0].AvgDuration == 0 {
		t.Error("AvgDuration should be non-zero after multiple calls")
	}
}

// --- LearnPattern ---

func TestProjectLearning_LearnPattern_UpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnPattern("auth-pattern", "initial description", []string{"example1"}, []string{"auth"})
	pl.LearnPattern("auth-pattern", "updated description", []string{"example2"}, []string{"auth", "security"})

	patterns := pl.GetRecentPatterns(10)
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].UsageCount != 2 {
		t.Errorf("UsageCount = %d, want 2", patterns[0].UsageCount)
	}
	if len(patterns[0].Examples) != 2 {
		t.Errorf("Examples count = %d, want 2", len(patterns[0].Examples))
	}
}

func TestProjectLearning_LearnPattern_LimitsExamples(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	for i := range 10 {
		pl.LearnPattern("many-examples", "desc", []string{"ex" + string(rune('A'+i))}, nil)
	}

	patterns := pl.GetRecentPatterns(10)
	if len(patterns[0].Examples) > 5 {
		t.Errorf("Examples count = %d, should be capped at 5", len(patterns[0].Examples))
	}
}

// --- LearnFileType ---

func TestProjectLearning_LearnFileType_UpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnFileType(".go", []string{"gofmt"})
	pl.LearnFileType(".go", []string{"go vet", "gofmt"})

	// Verify it was saved and reloaded correctly
	pl.Flush()
	pl2, _ := NewProjectLearning(dir)
	t.Cleanup(func() { _ = pl2.Flush() })

	// FileTypes are stored in learning.yaml, not rendered in project-memory.md.
	// Verify the YAML was persisted and reloaded.
	data, err := os.ReadFile(pl2.Path())
	if err != nil {
		t.Fatalf("ReadFile learning.yaml: %v", err)
	}
	yamlContent := string(data)
	if !strings.Contains(yamlContent, ".go") {
		t.Error("reloaded YAML should contain '.go' file type entry")
	}
	if !strings.Contains(yamlContent, "go vet") {
		t.Error("reloaded YAML should contain 'go vet' convention added on second call")
	}
}

// --- GetPreference / GetPreferences ---

func TestProjectLearning_GetPreference_MissingKey(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	val := pl.GetPreference("nonexistent")
	if val != "" {
		t.Errorf("GetPreference for missing key = %q, want empty", val)
	}
}

func TestProjectLearning_GetPreferences_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.SetPreference("key1", "val1")
	prefs := pl.GetPreferences()
	prefs["key1"] = "MUTATED"

	if pl.GetPreference("key1") != "val1" {
		t.Error("GetPreferences should return a copy, not the internal map")
	}
}

// --- GetFrequentCommands ---

func TestProjectLearning_GetFrequentCommands_SortedByUsage(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnCommand("cmd-a", "", true, 0)
	pl.LearnCommand("cmd-b", "", true, 0)
	pl.LearnCommand("cmd-b", "", true, 0)

	cmds := pl.GetFrequentCommands(10)
	if len(cmds) < 2 {
		t.Fatalf("expected at least 2 commands, got %d", len(cmds))
	}
	// cmd-b has UsageCount=2, cmd-a has UsageCount=1
	if cmds[0].Command != "cmd-b" {
		t.Errorf("first command should be 'cmd-b' (higher usage), got %q", cmds[0].Command)
	}
}

func TestProjectLearning_GetFrequentCommands_Limit(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnCommand("cmd-a", "", true, 0)
	pl.LearnCommand("cmd-b", "", true, 0)
	pl.LearnCommand("cmd-c", "", true, 0)

	cmds := pl.GetFrequentCommands(2)
	if len(cmds) != 2 {
		t.Errorf("expected 2 commands, got %d", len(cmds))
	}
}

// --- GetSuccessfulCommands ---

func TestProjectLearning_GetSuccessfulCommands(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	// Command with high success rate and enough usage
	pl.LearnCommand("good-cmd", "", true, 0)
	pl.LearnCommand("good-cmd", "", true, 0)

	// Command with low success rate
	pl.LearnCommand("bad-cmd", "", false, 0)
	pl.LearnCommand("bad-cmd", "", false, 0)

	result := pl.GetSuccessfulCommands(0.5)
	found := false
	for _, cmd := range result {
		if cmd.Command == "good-cmd" {
			found = true
		}
	}
	if !found {
		t.Error("GetSuccessfulCommands should include 'good-cmd'")
	}

	for _, cmd := range result {
		if cmd.Command == "bad-cmd" {
			t.Error("GetSuccessfulCommands should NOT include 'bad-cmd' (low success rate)")
		}
	}
}

// --- GetPatternsByTag ---

func TestProjectLearning_GetPatternsByTag(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnPattern("pattern-a", "desc a", nil, []string{"auth", "security"})
	pl.LearnPattern("pattern-b", "desc b", nil, []string{"testing"})

	result := pl.GetPatternsByTag("auth")
	if len(result) != 1 {
		t.Fatalf("expected 1 pattern with tag 'auth', got %d", len(result))
	}
	if result[0].Name != "pattern-a" {
		t.Errorf("pattern name = %q, want 'pattern-a'", result[0].Name)
	}
}

func TestProjectLearning_GetPatternsByTag_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnPattern("pattern-a", "desc a", nil, []string{"Auth"})

	result := pl.GetPatternsByTag("auth")
	if len(result) != 1 {
		t.Errorf("case-insensitive tag search: expected 1, got %d", len(result))
	}
}

// --- GetRecentPatterns ---

func TestProjectLearning_GetRecentPatterns_SortedByLastUsed(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.LearnPattern("old-pattern", "old", nil, nil)
	time.Sleep(10 * time.Millisecond)
	pl.LearnPattern("new-pattern", "new", nil, nil)

	result := pl.GetRecentPatterns(10)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 patterns, got %d", len(result))
	}
	if result[0].Name != "new-pattern" {
		t.Errorf("most recent pattern should be 'new-pattern', got %q", result[0].Name)
	}
}

// --- FormatForPrompt ---

func TestProjectLearning_FormatForPrompt_WithContent(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.SetPreference("test_command", "go test ./...")
	pl.SetPreference("fact:db_driver", "postgres")
	pl.SetPreference("convention:naming", "camelCase")
	pl.LearnPattern("live-card", "description", nil, nil)
	pl.LearnCommand("make build", "build the project", true, 0)
	pl.LearnCommand("make build", "", true, 0)

	result := pl.FormatForPrompt()
	if !strings.Contains(result, "Project Learning") {
		t.Error("FormatForPrompt should contain 'Project Learning' header")
	}
	if !strings.Contains(result, "Preferences") {
		t.Error("FormatForPrompt should contain 'Preferences' section")
	}
	if !strings.Contains(result, "Facts") {
		t.Error("FormatForPrompt should contain 'Facts' section")
	}
	if !strings.Contains(result, "Conventions") {
		t.Error("FormatForPrompt should contain 'Conventions' section")
	}
	if !strings.Contains(result, "Learned Patterns") {
		t.Error("FormatForPrompt should contain 'Learned Patterns' section")
	}
}

func TestProjectLearning_FormatForPrompt_Empty(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	result := pl.FormatForPrompt()
	if !strings.Contains(result, "Project Learning") {
		t.Error("FormatForPrompt should always contain the header")
	}
	// Should not contain section headers when empty
	if strings.Contains(result, "### Preferences") {
		t.Error("FormatForPrompt should not contain '### Preferences' when empty")
	}
}

// --- HasContent / Exists ---

func TestProjectLearning_HasContent_Empty(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	if pl.HasContent() {
		t.Error("HasContent should be false for empty store")
	}
}

func TestProjectLearning_HasContent_WithPreferences(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	pl.SetPreference("key", "val")
	if !pl.HasContent() {
		t.Error("HasContent should be true after setting a preference")
	}
}

func TestProjectLearning_Exists_FalseForNewDir(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	if pl.Exists() {
		t.Error("Exists should be false before any Flush")
	}
}

func TestProjectLearning_Exists_TrueAfterFlush(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	pl.SetPreference("key", "val")
	pl.Flush()

	if !pl.Exists() {
		t.Error("Exists should be true after Flush")
	}
}

// --- Path / MarkdownPath ---

func TestProjectLearning_Path(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })

	if !strings.HasSuffix(pl.Path(), "learning.yaml") {
		t.Errorf("Path = %q, should end with 'learning.yaml'", pl.Path())
	}
	if !strings.HasSuffix(pl.MarkdownPath(), "project-memory.md") {
		t.Errorf("MarkdownPath = %q, should end with 'project-memory.md'", pl.MarkdownPath())
	}
}

// --- FlushChanged ---

func TestProjectLearning_FlushChanged_NoDirtyReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	changed, err := pl.FlushChanged()
	if err != nil {
		t.Fatalf("FlushChanged: %v", err)
	}
	if changed {
		t.Error("FlushChanged should return changed=false when nothing is dirty")
	}
}

func TestProjectLearning_FlushChanged_DirtyReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	pl.SetPreference("key", "val")
	changed, err := pl.FlushChanged()
	if err != nil {
		t.Fatalf("FlushChanged: %v", err)
	}
	if !changed {
		t.Error("FlushChanged should return changed=true when dirty")
	}
}

// --- load from existing file ---

func TestProjectLearning_LoadFromExistingFile(t *testing.T) {
	dir := t.TempDir()

	pl1, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	pl1.SetPreference("persisted_key", "persisted_val")
	pl1.Flush()

	pl2, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning (reload): %v", err)
	}
	t.Cleanup(func() { _ = pl2.Flush() })

	if pl2.GetPreference("persisted_key") != "persisted_val" {
		t.Error("loaded store should contain persisted preference")
	}
}

// --- load with corrupt YAML ---

func TestProjectLearning_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	gokinDir := filepath.Join(dir, ".gokin")
	os.MkdirAll(gokinDir, 0755)
	os.WriteFile(filepath.Join(gokinDir, "learning.yaml"), []byte("not: valid: yaml: [[["), 0644)

	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning with corrupt YAML should not error: %v", err)
	}
	t.Cleanup(func() { _ = pl.Flush() })
}

// --- NewProjectLearning MkdirAll failure ---

func TestNewProjectLearning_MkdirFailure(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "iamfile")
	os.WriteFile(filePath, []byte("x"), 0644)

	_, err := NewProjectLearning(filePath)
	if err == nil {
		t.Error("NewProjectLearning should fail when MkdirAll fails")
	}
}

// --- splitProjectPreferences ---

func TestSplitProjectPreferences(t *testing.T) {
	prefs := map[string]string{
		"plain_key":         "val1",
		"fact:something":    "val2",
		"convention:naming": "val3",
	}

	plain, facts, conventions := splitProjectPreferences(prefs)

	if plain["plain_key"] != "val1" {
		t.Error("plain key should be in plain map")
	}
	if facts["something"] != "val2" {
		t.Error("fact: key should be stripped and placed in facts map")
	}
	if conventions["naming"] != "val3" {
		t.Error("convention: key should be stripped and placed in conventions map")
	}
}

// --- sortedPreferenceKeys ---

func TestSortedPreferenceKeys(t *testing.T) {
	m := map[string]string{"c": "3", "a": "1", "b": "2"}
	keys := sortedPreferenceKeys(m)

	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("sortedPreferenceKeys = %v, want [a b c]", keys)
	}
}
