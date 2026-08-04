package memory

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"

	"gokin/internal/fileutil"
)

const (
	maxProjectLearningFileBytes   int64 = 16 << 20
	maxProjectLearningPreferences       = 4096
	maxProjectLearningCommands          = 1000
	maxProjectLearningPatterns          = 1000
	maxProjectLearningFileTypes         = 256
	maxProjectLearningExamples          = 5
	maxProjectLearningTags              = 32
	maxProjectLearningConventions       = 64
)

type projectLearningStorageError struct {
	err error
}

func (e *projectLearningStorageError) Error() string { return e.err.Error() }
func (e *projectLearningStorageError) Unwrap() error { return e.err }

func ensureProjectLearningStorage(gokinDir, learningPath, markdownPath string) error {
	if err := fileutil.EnsurePrivateDir(gokinDir); err != nil {
		return fmt.Errorf("prepare project learning directory: %w", err)
	}
	for _, path := range []string{learningPath, markdownPath} {
		if err := fileutil.SecurePrivateFile(path); err != nil {
			return fmt.Errorf("secure project learning file %q: %w", path, err)
		}
	}
	return nil
}

func readProjectLearningFile(path string) ([]byte, error) {
	data, err := fileutil.ReadPrivateFile(path, maxProjectLearningFileBytes)
	if err != nil {
		return nil, &projectLearningStorageError{err: err}
	}
	return data, nil
}

func writeProjectLearningFile(path string, data []byte) error {
	if int64(len(data)) > maxProjectLearningFileBytes {
		return fmt.Errorf("project learning file exceeds %d-byte limit", maxProjectLearningFileBytes)
	}
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}

func sanitizeProjectData(data *ProjectData) {
	if data.Preferences == nil {
		data.Preferences = make(map[string]string)
	}
	if len(data.Preferences) > maxProjectLearningPreferences {
		keys := sortedPreferenceKeys(data.Preferences)
		bounded := make(map[string]string, maxProjectLearningPreferences)
		for _, key := range keys {
			if key == "" {
				continue
			}
			bounded[key] = data.Preferences[key]
			if len(bounded) == maxProjectLearningPreferences {
				break
			}
		}
		data.Preferences = bounded
	} else {
		delete(data.Preferences, "")
	}

	data.Commands = sanitizeLearnedCommands(data.Commands)
	data.Patterns = sanitizeLearnedPatterns(data.Patterns)
	data.FileTypes = sanitizeLearnedFileTypes(data.FileTypes)
}

func sanitizeLearnedCommands(commands []LearnedCommand) []LearnedCommand {
	sort.SliceStable(commands, func(i, j int) bool {
		if !commands[i].LastUsed.Equal(commands[j].LastUsed) {
			return commands[i].LastUsed.After(commands[j].LastUsed)
		}
		if commands[i].UsageCount != commands[j].UsageCount {
			return commands[i].UsageCount > commands[j].UsageCount
		}
		return commands[i].Command < commands[j].Command
	})
	seen := make(map[string]struct{}, min(len(commands), maxProjectLearningCommands))
	bounded := make([]LearnedCommand, 0, min(len(commands), maxProjectLearningCommands))
	for _, command := range commands {
		if command.Command == "" {
			continue
		}
		if _, exists := seen[command.Command]; exists {
			continue
		}
		if command.UsageCount < 0 {
			command.UsageCount = 0
		}
		if math.IsNaN(command.SuccessRate) || math.IsInf(command.SuccessRate, 0) {
			command.SuccessRate = 0.5
		} else {
			command.SuccessRate = min(1, max(0, command.SuccessRate))
		}
		if math.IsNaN(command.AvgDuration) || math.IsInf(command.AvgDuration, 0) || command.AvgDuration < 0 {
			command.AvgDuration = 0
		}
		seen[command.Command] = struct{}{}
		bounded = append(bounded, command)
		if len(bounded) == maxProjectLearningCommands {
			break
		}
	}
	return bounded
}

func sanitizeLearnedPatterns(patterns []LearnedPattern) []LearnedPattern {
	sort.SliceStable(patterns, func(i, j int) bool {
		if !patterns[i].LastUsed.Equal(patterns[j].LastUsed) {
			return patterns[i].LastUsed.After(patterns[j].LastUsed)
		}
		if patterns[i].UsageCount != patterns[j].UsageCount {
			return patterns[i].UsageCount > patterns[j].UsageCount
		}
		return patterns[i].Name < patterns[j].Name
	})
	seen := make(map[string]struct{}, min(len(patterns), maxProjectLearningPatterns))
	bounded := make([]LearnedPattern, 0, min(len(patterns), maxProjectLearningPatterns))
	for _, pattern := range patterns {
		if pattern.Name == "" {
			continue
		}
		if _, exists := seen[pattern.Name]; exists {
			continue
		}
		if pattern.UsageCount < 0 {
			pattern.UsageCount = 0
		}
		pattern.Examples = uniqueStringTail(pattern.Examples, maxProjectLearningExamples)
		pattern.Tags = uniqueStringHead(pattern.Tags, maxProjectLearningTags)
		seen[pattern.Name] = struct{}{}
		bounded = append(bounded, pattern)
		if len(bounded) == maxProjectLearningPatterns {
			break
		}
	}
	return bounded
}

func sanitizeLearnedFileTypes(fileTypes []LearnedFileType) []LearnedFileType {
	sort.SliceStable(fileTypes, func(i, j int) bool {
		if fileTypes[i].UsageCount != fileTypes[j].UsageCount {
			return fileTypes[i].UsageCount > fileTypes[j].UsageCount
		}
		return fileTypes[i].Extension < fileTypes[j].Extension
	})
	seen := make(map[string]struct{}, min(len(fileTypes), maxProjectLearningFileTypes))
	bounded := make([]LearnedFileType, 0, min(len(fileTypes), maxProjectLearningFileTypes))
	for _, fileType := range fileTypes {
		if fileType.Extension == "" {
			continue
		}
		if _, exists := seen[fileType.Extension]; exists {
			continue
		}
		if fileType.UsageCount < 0 {
			fileType.UsageCount = 0
		}
		fileType.Conventions = uniqueStringTail(fileType.Conventions, maxProjectLearningConventions)
		seen[fileType.Extension] = struct{}{}
		bounded = append(bounded, fileType)
		if len(bounded) == maxProjectLearningFileTypes {
			break
		}
	}
	return bounded
}

func uniqueStringHead(values []string, limit int) []string {
	seen := make(map[string]struct{}, min(len(values), limit))
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func uniqueStringTail(values []string, limit int) []string {
	seen := make(map[string]struct{}, min(len(values), limit))
	reversed := make([]string, 0, min(len(values), limit))
	for i := len(values) - 1; i >= 0 && len(reversed) < limit; i-- {
		value := values[i]
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		reversed = append(reversed, value)
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}
