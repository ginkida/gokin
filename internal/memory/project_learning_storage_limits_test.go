package memory

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestProjectLearningRejectsOversizedDurableState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "learning.yaml")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxProjectLearningFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()

	if _, err := NewProjectLearning(root); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized project learning error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Size(); got != maxProjectLearningFileBytes+1 {
		t.Fatalf("oversized file was changed: size = %d", got)
	}
}

func TestProjectLearningLoadSanitizesAndBoundsDurableState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	data := ProjectData{
		Preferences: make(map[string]string, maxProjectLearningPreferences+2),
		Commands:    make([]LearnedCommand, maxProjectLearningCommands+1),
		Patterns:    make([]LearnedPattern, maxProjectLearningPatterns+1),
		FileTypes:   make([]LearnedFileType, maxProjectLearningFileTypes+1),
	}
	data.Preferences[""] = "invalid"
	for i := 0; i <= maxProjectLearningPreferences; i++ {
		data.Preferences[fmt.Sprintf("preference-%05d", i)] = "value"
	}
	for i := range data.Commands {
		data.Commands[i] = LearnedCommand{
			Command:     fmt.Sprintf("command-%04d", i),
			UsageCount:  i,
			LastUsed:    time.Unix(int64(i), 0),
			SuccessRate: 0.75,
		}
	}
	data.Commands[maxProjectLearningCommands].UsageCount = -1
	data.Commands[maxProjectLearningCommands].SuccessRate = math.NaN()
	data.Commands[maxProjectLearningCommands].AvgDuration = -1
	for i := range data.Patterns {
		data.Patterns[i] = LearnedPattern{
			Name:       fmt.Sprintf("pattern-%04d", i),
			LastUsed:   time.Unix(int64(i), 0),
			Examples:   []string{"one", "two", "three", "four", "five", "six", "six"},
			Tags:       repeatedStrings("tag", maxProjectLearningTags+1),
			UsageCount: i,
		}
	}
	for i := range data.FileTypes {
		data.FileTypes[i] = LearnedFileType{
			Extension:   fmt.Sprintf(".ext%03d", i),
			UsageCount:  i,
			Conventions: repeatedStrings("convention", maxProjectLearningConventions+1),
		}
	}

	raw, err := yaml.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) > maxProjectLearningFileBytes {
		t.Fatalf("test fixture unexpectedly exceeds file limit: %d", len(raw))
	}
	if err := os.WriteFile(filepath.Join(dir, "learning.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	pl, err := NewProjectLearning(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(pl.data.Preferences); got != maxProjectLearningPreferences {
		t.Fatalf("preferences = %d, want %d", got, maxProjectLearningPreferences)
	}
	if got := len(pl.data.Commands); got != maxProjectLearningCommands {
		t.Fatalf("commands = %d, want %d", got, maxProjectLearningCommands)
	}
	if got := len(pl.data.Patterns); got != maxProjectLearningPatterns {
		t.Fatalf("patterns = %d, want %d", got, maxProjectLearningPatterns)
	}
	if got := len(pl.data.FileTypes); got != maxProjectLearningFileTypes {
		t.Fatalf("file types = %d, want %d", got, maxProjectLearningFileTypes)
	}
	newest := pl.data.Commands[0]
	if newest.Command != fmt.Sprintf("command-%04d", maxProjectLearningCommands) {
		t.Fatalf("newest command = %q", newest.Command)
	}
	if newest.UsageCount != 0 || newest.SuccessRate != 0.5 || newest.AvgDuration != 0 {
		t.Fatalf("invalid command metrics were not repaired: %#v", newest)
	}
	if len(pl.data.Patterns[0].Examples) != maxProjectLearningExamples || len(pl.data.Patterns[0].Tags) != maxProjectLearningTags {
		t.Fatalf("pattern nested limits not applied: %#v", pl.data.Patterns[0])
	}
	if len(pl.data.FileTypes[0].Conventions) != maxProjectLearningConventions {
		t.Fatalf("file type conventions = %d", len(pl.data.FileTypes[0].Conventions))
	}
}

func TestProjectLearningRuntimeCardinalityLimits(t *testing.T) {
	pl, err := NewProjectLearning(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pl.saveFunc = func() {}

	for i := 0; i < maxProjectLearningCommands; i++ {
		pl.data.Commands = append(pl.data.Commands, LearnedCommand{Command: fmt.Sprintf("command-%d", i)})
	}
	pl.LearnCommand("newest-command", "", true, 0)
	if len(pl.data.Commands) != maxProjectLearningCommands {
		t.Fatalf("runtime commands = %d", len(pl.data.Commands))
	}

	for i := 0; i < maxProjectLearningPatterns; i++ {
		pl.data.Patterns = append(pl.data.Patterns, LearnedPattern{Name: fmt.Sprintf("pattern-%d", i)})
	}
	pl.LearnPattern("newest-pattern", "", nil, nil)
	if len(pl.data.Patterns) != maxProjectLearningPatterns {
		t.Fatalf("runtime patterns = %d", len(pl.data.Patterns))
	}

	for i := 0; i < maxProjectLearningFileTypes; i++ {
		pl.data.FileTypes = append(pl.data.FileTypes, LearnedFileType{Extension: fmt.Sprintf(".ext%d", i)})
	}
	pl.LearnFileType(".new", repeatedStrings("convention", maxProjectLearningConventions+1))
	if len(pl.data.FileTypes) != maxProjectLearningFileTypes {
		t.Fatalf("runtime file types = %d", len(pl.data.FileTypes))
	}
	for _, fileType := range pl.data.FileTypes {
		if fileType.Extension == ".new" && len(fileType.Conventions) != maxProjectLearningConventions {
			t.Fatalf("runtime conventions = %d", len(fileType.Conventions))
		}
	}

	for i := 0; i < maxProjectLearningPreferences; i++ {
		pl.data.Preferences[fmt.Sprintf("preference-%d", i)] = "value"
	}
	pl.SetPreference("over-limit", "rejected")
	if got := len(pl.data.Preferences); got != maxProjectLearningPreferences {
		t.Fatalf("runtime preferences = %d", got)
	}
	if got := pl.GetPreference("over-limit"); got != "" {
		t.Fatalf("over-limit preference retained: %q", got)
	}
}

func repeatedStrings(prefix string, count int) []string {
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return values
}
