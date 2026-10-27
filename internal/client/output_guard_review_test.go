package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOutputGuardWhitespaceFreeLanguage(t *testing.T) {
	for _, phrase := range []string{"让我继续分析这个问题。", "data=context.artifact_get('x');"} {
		for _, tool := range []string{"", "repl_exec"} {
			t.Run(tool+phrase, func(t *testing.T) {
				var guard outputGuard
				text := strings.Repeat(phrase, (32<<10)/len(phrase)+2)
				if failure, ok := OutputRecoveryError(guard.check("test-model", tool, text, DefaultMaxToolArgumentBytes)); !ok || failure.Period == 0 {
					t.Fatal("whitespace-free repetition bypassed the guard")
				}
			})
		}
	}
}

func TestOutputGuardOpaqueAlphabet(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("real binary fixture\x00\xff\x01"))
	for _, period := range []string{encoded, encoded + "\n", encoded + `\n`, "deadbeef01234567"} {
		for phase := range len(period) {
			var guard outputGuard
			text := strings.Repeat(period, (160<<10)/len(period)+2)[phase:]
			if err := guard.check("test-model", "", text, 0); err != nil {
				t.Fatalf("opaque period rejected at phase %d: %v", phase, err)
			}
		}
	}
}

func TestOutputGuardFileFixtureWindow(t *testing.T) {
	fixtures := map[string]string{
		"csv": strings.Repeat("alice,active,42\n", 3000),
		"sql": strings.Repeat("INSERT INTO users VALUES ('alice', 'active', 42);\n", 1800),
	}
	for tool, field := range map[string]string{"write": "content", "edit": "new_string", "batch": "replacement", "diff": "content", "harness": "code"} {
		for name, content := range fixtures {
			t.Run(tool+"/"+name, func(t *testing.T) {
				args := map[string]any{field: content}
				if tool == "edit" {
					args = map[string]any{"file_path": "fixture", "edits": []any{map[string]any{"old_string": "old", field: content}}}
				}
				data, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				var guard outputGuard
				for offset := 0; offset < len(data); offset += 137 {
					if err := guard.check("test-model", tool, string(data[offset:min(offset+137, len(data))]), DefaultMaxToolArgumentBytes); err != nil {
						t.Fatalf("legitimate %s fixture rejected: %v", name, err)
					}
				}
				if err := checkToolArgumentRepetition("test-model", tool, args); err != nil {
					t.Fatalf("completed fixture rejected: %v", err)
				}
			})
		}
	}
	for _, tool := range []string{"", "repl_exec", "write", "edit", "batch", "diff", "harness"} {
		t.Run(tool+"/threshold", func(t *testing.T) {
			var guard outputGuard
			phrase := "data = context.artifact_get('x'); "
			short := strings.Repeat(phrase, (32<<10)/len(phrase)+2)
			err := guard.check("test-model", tool, short, DefaultMaxToolArgumentBytes)
			fileContent := tool != "" && tool != "repl_exec"
			if (err == nil) != fileContent {
				t.Fatalf("32 KiB threshold: tool=%q error=%v", tool, err)
			}
			if fileContent {
				if err := guard.check("test-model", tool, strings.Repeat(phrase, (128<<10)/len(phrase)+2), DefaultMaxToolArgumentBytes); err == nil {
					t.Fatal("file-content guard failed to trip at 128 KiB")
				}
			}
		})
	}
}

func TestOllamaTextFileFixtureWindow(t *testing.T) {
	for _, tool := range []string{"write", "repl_exec"} {
		t.Run(tool, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{"content": strings.Repeat("alice,active,42\n", 3000)})
			if err != nil {
				t.Fatal(err)
			}
			// Put the tool header first, as requested by ToolCallFallbackPrompt.
			payload := []byte(fmt.Sprintf(`{"tool":%q,"args":%s}`, tool, args))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				for offset := 0; offset < len(payload); offset += 137 {
					_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": string(payload[offset:min(offset+137, len(payload))])}})
					w.(http.Flusher).Flush()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true})
			}))
			defer server.Close()
			c, err := NewOllamaClient(OllamaConfig{Model: "text-only-fixture", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := c.SendMessage(t.Context(), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			response, err := stream.CollectContext(t.Context())
			if tool == "write" {
				if err != nil || response.Text != string(payload) {
					t.Fatalf("text-encoded file fixture rejected: %v", err)
				}
			} else if _, ok := OutputRecoveryError(err); !ok {
				t.Fatalf("non-file fallback repetition accepted: %v", err)
			}
		})
	}
}
