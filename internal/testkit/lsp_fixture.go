package testkit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// RunLSPFixture is called from an explicitly selected test helper subprocess.
// It never runs in production, and exits before Go can write PASS to stdout.
func RunLSPFixture(t *testing.T) {
	if os.Getenv("GOKIN_LSP_FIXTURE") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	send := func(value any) {
		data, _ := json.Marshal(value)
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}
	log := func(value any) {
		path := os.Getenv("GOKIN_LSP_LOG")
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(3)
		}
		_ = json.NewEncoder(f).Encode(value)
		f.Close()
	}
	cwd, _ := os.Getwd()
	log(map[string]any{"event": "start", "pid": os.Getpid(), "cwd": cwd, "root": os.Getenv("CLAUDE_PLUGIN_ROOT"), "option": os.Getenv("LITERAL_OPTION")})
	documents := map[string]string{}
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			if line == "\r\n" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				length, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			}
		}
		if length <= 0 || length > 32<<20 {
			os.Exit(4)
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			os.Exit(0)
		}
		var msg struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			Result any            `json:"result"`
		}
		if json.Unmarshal(data, &msg) != nil {
			os.Exit(5)
		}
		log(map[string]any{"event": "message", "method": msg.Method, "params": msg.Params, "result": msg.Result})
		if msg.Method == "exit" {
			os.Exit(0)
		}
		if msg.ID == nil {
			if msg.Method == "textDocument/didOpen" || msg.Method == "textDocument/didChange" {
				doc, _ := msg.Params["textDocument"].(map[string]any)
				uri, _ := doc["uri"].(string)
				version, _ := doc["version"].(float64)
				text, _ := doc["text"].(string)
				if changes, ok := msg.Params["contentChanges"].([]any); ok && len(changes) > 0 {
					text, _ = changes[0].(map[string]any)["text"].(string)
				}
				documents[uri] = text
				if os.Getenv("GOKIN_LSP_NO_PUSH") != "1" {
					issues := []any{}
					if strings.Contains(text, "TYPE_ERROR") {
						issues = append(issues, map[string]any{"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "severity": 1, "message": "fixture type mismatch"})
					}
					send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "version": version - 1, "diagnostics": []any{map[string]any{"message": "STALE diagnostics"}}}})
					send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "version": version, "diagnostics": issues}})
				}
			}
			continue
		}
		var result any
		if msg.Method == os.Getenv("GOKIN_LSP_HANG_METHOD") {
			continue
		}
		switch msg.Method {
		case "initialize":
			encoding := os.Getenv("GOKIN_LSP_ENCODING")
			if encoding == "" {
				encoding = "utf-16"
			}
			caps := map[string]any{"positionEncoding": encoding, "textDocumentSync": map[string]any{"openClose": true, "change": 2}, "definitionProvider": true, "referencesProvider": true, "hoverProvider": true, "documentSymbolProvider": true, "workspaceSymbolProvider": true, "implementationProvider": true, "callHierarchyProvider": true}
			if os.Getenv("GOKIN_LSP_PULL") == "1" {
				caps["diagnosticProvider"] = map[string]any{"interFileDependencies": false, "workspaceDiagnostics": false}
			}
			result = map[string]any{"capabilities": caps}
			send(map[string]any{"jsonrpc": "2.0", "id": "configuration", "method": "workspace/configuration", "params": map[string]any{"items": []any{map[string]any{"section": "fixture"}}}})
		case "shutdown":
			result = nil
		case "textDocument/diagnostic":
			doc, _ := msg.Params["textDocument"].(map[string]any)
			uri, _ := doc["uri"].(string)
			issues := []any{}
			if strings.Contains(documents[uri], "TYPE_ERROR") {
				issues = append(issues, map[string]any{"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "severity": 1, "message": "fixture type mismatch"})
			}
			result = map[string]any{"kind": "full", "items": issues}
		case "textDocument/prepareCallHierarchy":
			result = []any{map[string]any{"name": "fixture-call", "kind": 12, "uri": firstURI(documents), "range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "selectionRange": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}}}
		case "callHierarchy/incomingCalls", "callHierarchy/outgoingCalls":
			result = []any{map[string]any{"echo": msg.Params}}
		default:
			if crash := os.Getenv("GOKIN_LSP_CRASH_ONCE"); crash != "" {
				if _, err := os.Stat(crash); os.IsNotExist(err) {
					_ = os.WriteFile(crash, []byte("crashed"), 0600)
					os.Exit(8)
				}
			}
			result = map[string]any{"echo": msg.Params, "version": os.Getenv("GOKIN_LSP_VERSION"), "uri": firstURI(documents)}
			if extra := os.Getenv("GOKIN_LSP_EXTRA_URI"); extra != "" {
				result.(map[string]any)["locations"] = []any{map[string]any{"uri": extra, "label": "denied-location"}}
			}
		}
		send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}
}
func firstURI(documents map[string]string) string {
	for uri := range documents {
		return uri
	}
	return ""
}
