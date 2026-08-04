package audit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAuditLoadFiltersNilEntriesAndKeepsConfiguredTail(t *testing.T) {
	logger, err := NewLogger(t.TempDir(), "bounded-load", Config{
		Enabled: true, MaxEntries: 2, MaxResultLen: 1000, RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []*Entry{
		{ToolName: "first"}, nil, {ToolName: "second"}, {ToolName: "third"},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logger.getFilePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := logger.load(); err != nil {
		t.Fatal(err)
	}
	if len(logger.entries) != 2 || logger.entries[0].ToolName != "second" || logger.entries[1].ToolName != "third" {
		t.Fatalf("bounded entries = %+v", logger.entries)
	}
	if stats := logger.Stats(); stats.TotalEntries != 2 {
		t.Fatalf("stats after null filtering = %+v", stats)
	}
}

func TestAuditLoadRejectsOversizedFile(t *testing.T) {
	logger, err := NewLogger(t.TempDir(), "oversized-load", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(logger.getFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAuditLogFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.load(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized audit load error = %v", err)
	}
}

func TestNewLoggerRejectsNegativeLimits(t *testing.T) {
	for _, cfg := range []Config{
		{Enabled: true, MaxEntries: -1},
		{Enabled: true, MaxResultLen: -1},
		{Enabled: true, RetentionDays: -1},
	} {
		if _, err := NewLogger(t.TempDir(), "negative-limits", cfg); err == nil {
			t.Fatalf("NewLogger accepted negative limits: %+v", cfg)
		}
	}
}
