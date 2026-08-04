package context

import (
	stdcontext "context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/genai"
)

func TestContextMemoryLoadRejectsOversizedFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, limit := range map[string]int64{
		sessionMemoryFilename: maxSessionMemoryFileBytes,
		workingMemoryFilename: maxWorkingMemoryFileBytes,
	} {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(limit + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		_ = file.Close()
	}

	session := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	session.mu.Lock()
	session.content = "current session"
	session.mu.Unlock()
	session.LoadFromDisk()
	if got := session.GetContent(); got != "current session" {
		t.Fatalf("oversized session load replaced content: %q", got)
	}

	working := NewWorkingMemoryManager(root)
	working.content = "current working"
	working.LoadFromDisk()
	if got := working.GetContent(); got != "current working" {
		t.Fatalf("oversized working load replaced content: %q", got)
	}
}

func TestBoundContextMemoryContentPreservesUTF8AndMarksTruncation(t *testing.T) {
	for _, limit := range []int64{maxWorkingMemoryFileBytes, maxSessionMemoryFileBytes} {
		content := strings.Repeat("я", int(limit))
		bounded := boundContextMemoryContent(content, limit)
		if int64(len(bounded)) > limit {
			t.Fatalf("bounded content size = %d, limit %d", len(bounded), limit)
		}
		if !utf8.ValidString(bounded) {
			t.Fatal("bounded content is not valid UTF-8")
		}
		if !strings.HasSuffix(bounded, contextMemoryTruncationMarker) {
			t.Fatal("bounded content does not disclose truncation")
		}
	}
}

func TestWriteContextMemoryFileRejectsOversizedContent(t *testing.T) {
	root := t.TempDir()
	err := writeContextMemoryFile(root, workingMemoryFilename, make([]byte, maxWorkingMemoryFileBytes+1), maxWorkingMemoryFileBytes)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized write error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".gokin")); !os.IsNotExist(err) {
		t.Fatalf("oversized write created storage directory: %v", err)
	}
}

type oversizedContextMemorySummarizer struct{}

func (oversizedContextMemorySummarizer) Summarize(stdcontext.Context, []*genai.Content, string) (string, error) {
	return strings.Repeat("я", int(maxSessionMemoryFileBytes)), nil
}

func TestSessionMemoryBoundsLLMSummaryBeforePromptAndPersistence(t *testing.T) {
	manager := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	manager.SetSummarizer(oversizedContextMemorySummarizer{})
	manager.extractWithLLM(nil, "fallback", 0)

	content := manager.GetContent()
	if int64(len(content)) > maxSessionMemoryFileBytes {
		t.Fatalf("session prompt content size = %d", len(content))
	}
	if !utf8.ValidString(content) || !strings.HasSuffix(content, contextMemoryTruncationMarker) {
		t.Fatal("bounded LLM summary is invalid or does not disclose truncation")
	}
	info, err := os.Stat(manager.filePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxSessionMemoryFileBytes {
		t.Fatalf("persisted session memory size = %d", info.Size())
	}
}
