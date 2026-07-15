package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	stdioWorkDirHelperFlag    = "GOKIN_MCP_WORKDIR_HELPER"
	stdioWorkDirHelperLog     = "GOKIN_MCP_WORKDIR_LOG"
	stdioWorkDirHelperCounter = "GOKIN_MCP_WORKDIR_COUNTER"
	stdioWorkDirParentSecret  = "GOKIN_MCP_WORKDIR_PARENT_SECRET"
	stdioWorkDirExpandedValue = "GOKIN_MCP_WORKDIR_EXPANDED_VALUE"
)

type stdioWorkDirRecord struct {
	Launch        int    `json:"launch"`
	Event         string `json:"event"`
	WorkDir       string `json:"work_dir"`
	ParentSecret  string `json:"parent_secret"`
	ExpandedValue string `json:"expanded_value"`
}

func TestClientStdioWorkDirInitialAndReconnect(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	logPath := filepath.Join(stateDir, "workdirs.jsonl")
	counterPath := filepath.Join(stateDir, "launch-count")

	t.Setenv(stdioWorkDirParentSecret, "must-not-reach-mcp-server")
	cfg := &ServerConfig{
		Name:      "workdir-helper",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestMCPStdioWorkDirHelperProcess$"},
		WorkDir:   workspace,
		Env: map[string]string{
			stdioWorkDirHelperFlag:    "1",
			stdioWorkDirHelperLog:     logPath,
			stdioWorkDirHelperCounter: counterPath,
			stdioWorkDirExpandedValue: "${" + stdioWorkDirParentSecret + "}",
		},
	}

	client, err := NewClient(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		// A stdio receive is blocked in Scanner.Scan. Close the process transport
		// after cancellation so the receive loop can observe EOF immediately.
		client.cancel()
		client.mu.RLock()
		transport := client.transport
		client.mu.RUnlock()
		_ = transport.Close()
		_ = client.Close()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	var records []stdioWorkDirRecord
	if !waitFor(5*time.Second, func() bool {
		records = readStdioWorkDirRecords(logPath)
		for _, record := range records {
			if record.Launch == 2 && record.Event == "initialized" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("stdio server did not reconnect and initialize; records = %+v", records)
	}

	for _, launch := range []int{1, 2} {
		record, ok := findStdioWorkDirRecord(records, launch, "started")
		if !ok {
			t.Fatalf("missing launch %d start record: %+v", launch, records)
		}
		assertSameDirectory(t, record.WorkDir, workspace)
		if record.ParentSecret != "" {
			t.Errorf("launch %d inherited parent secret %q", launch, record.ParentSecret)
		}
		wantLiteral := "${" + stdioWorkDirParentSecret + "}"
		if record.ExpandedValue != wantLiteral {
			t.Errorf("launch %d expanded unsafe parent env: got %q, want %q", launch, record.ExpandedValue, wantLiteral)
		}
	}
}

func TestNewStdioTransportWithWorkDirRejectsInvalidDirectory(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		workDir string
		want    string
	}{
		{name: "relative", workDir: "workspace", want: "must be absolute"},
		{name: "parent traversal", workDir: root + string(filepath.Separator) + "child" + string(filepath.Separator) + "..", want: "parent traversal"},
		{name: "missing", workDir: filepath.Join(root, "missing"), want: "no such file"},
		{name: "regular file", workDir: filePath, want: "not a directory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := NewStdioTransportWithWorkDir(os.Args[0], nil, nil, tt.workDir)
			if transport != nil {
				_ = transport.Close()
				t.Fatal("transport started for invalid work directory")
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

// TestMCPStdioWorkDirHelperProcess is executed in a subprocess by
// TestClientStdioWorkDirInitialAndReconnect. The first launch exits after MCP
// initialization to force the real client reconnect path; the second stays
// alive until its stdin is closed by test cleanup.
func TestMCPStdioWorkDirHelperProcess(t *testing.T) {
	if os.Getenv(stdioWorkDirHelperFlag) != "1" {
		return
	}
	if err := runStdioWorkDirHelper(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "workdir helper: %v\n", err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runStdioWorkDirHelper() error {
	counterPath := os.Getenv(stdioWorkDirHelperCounter)
	logPath := os.Getenv(stdioWorkDirHelperLog)
	launch, err := nextStdioWorkDirLaunch(counterPath)
	if err != nil {
		return err
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	record := stdioWorkDirRecord{
		Launch:        launch,
		Event:         "started",
		WorkDir:       workDir,
		ParentSecret:  os.Getenv(stdioWorkDirParentSecret),
		ExpandedValue: os.Getenv(stdioWorkDirExpandedValue),
	}
	if err := appendStdioWorkDirRecord(logPath, record); err != nil {
		return err
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var msg JSONRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return err
		}
		switch msg.Method {
		case MethodInitialize:
			if err := encoder.Encode(&JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result: map[string]any{
					"protocolVersion": ProtocolVersion,
					"serverInfo": map[string]any{
						"name":    "workdir-helper",
						"version": "test",
					},
				},
			}); err != nil {
				return err
			}
		case MethodInitialized:
			record.Event = "initialized"
			if err := appendStdioWorkDirRecord(logPath, record); err != nil {
				return err
			}
			if launch == 1 {
				return nil
			}
		}
	}
	return scanner.Err()
}

func nextStdioWorkDirLaunch(path string) (int, error) {
	launch := 1
	if raw, err := os.ReadFile(path); err == nil {
		previous, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if parseErr != nil {
			return 0, parseErr
		}
		launch = previous + 1
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(launch)), 0o600); err != nil {
		return 0, err
	}
	return launch, nil
}

func appendStdioWorkDirRecord(path string, record stdioWorkDirRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(raw, '\n'))
	return err
}

func readStdioWorkDirRecords(path string) []stdioWorkDirRecord {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var records []stdioWorkDirRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var record stdioWorkDirRecord
		if err := json.Unmarshal([]byte(line), &record); err == nil {
			records = append(records, record)
		}
	}
	return records
}

func findStdioWorkDirRecord(records []stdioWorkDirRecord, launch int, event string) (stdioWorkDirRecord, bool) {
	for _, record := range records {
		if record.Launch == launch && record.Event == event {
			return record, true
		}
	}
	return stdioWorkDirRecord{}, false
}

func assertSameDirectory(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat process cwd %q: %v", got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat expected cwd %q: %v", want, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Errorf("process cwd = %q, want same directory as %q", got, want)
	}
}
