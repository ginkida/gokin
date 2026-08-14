package evals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestRejectsAmbiguousJSON(t *testing.T) {
	validScenario := `{
      "id":"s", "category":"test", "difficulty":"small", "prompt":"inspect",
      "fixture":"fixture", "expected_behaviors":["inspect"],
      "verification_commands":["go test ./..."], "success_criteria":["done"],
      "failure_signals":["wrong"], "max_tool_calls":1
    }`
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "duplicate root key",
			content: `{"version":1,"name":"first","name":"second","metrics":["m"],"scenarios":[` +
				validScenario + `]}`,
			want: `duplicate JSON key "name"`,
		},
		{
			name: "duplicate nested key",
			content: `{"version":1,"name":"eval","metrics":["m"],"scenarios":[` +
				strings.Replace(validScenario, `"max_tool_calls":1`, `"max_tool_calls":1,"max_tool_calls":2`, 1) + `]}`,
			want: `duplicate JSON key "max_tool_calls"`,
		},
		{
			name: "unknown contract field",
			content: `{"version":1,"name":"eval","metrics":["m"],"scenarios":[` +
				strings.Replace(validScenario, `"max_tool_calls":1`, `"max_tool_calls":1,"hybrid_min_file_index_refresh":1`, 1) + `]}`,
			want: `unknown field "hybrid_min_file_index_refresh"`,
		},
		{
			name:    "multiple documents",
			content: `{"version":1,"name":"eval","metrics":["m"],"scenarios":[` + validScenario + `]} {}`,
			want:    "multiple JSON values",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadManifest(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadManifest() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRejectDuplicateJSONKeysAllowsSameKeyInSeparateObjects(t *testing.T) {
	data := []byte(`[{"id":1},{"id":2}]`)
	if err := rejectDuplicateJSONKeys(data); err != nil {
		t.Fatalf("rejectDuplicateJSONKeys() rejected separate object keys: %v", err)
	}
}
