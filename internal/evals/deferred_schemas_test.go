package evals

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestEvalDeferredSchemaFlagReachesAgentAndBindsResume(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	var hashes []string
	for _, deferred := range []bool{false, true} {
		value := strconv.FormatBool(deferred)
		results, err := Run(t.Context(), RunOptions{
			DeferredSchemas: deferred, ManifestPath: manifestPath, FixturesRoot: fixturesRoot,
			WorkRoot: filepath.Join(root, value), OutputPath: filepath.Join(root, value+".jsonl"),
			AgentCommand: `printf '%s' "$GOKIN_DEFERRED_SCHEMAS"`, Timeout: 5 * time.Second,
		})
		if err != nil || len(results) != 1 {
			t.Fatal(results, err)
		}
		if results[0].Agent.OutputPreview != value || results[0].Metadata["deferred_schemas"] != value {
			t.Fatal("schema mode lost from eval process/provenance", results[0])
		}
		hashes = append(hashes, results[0].RunSpecHash)
	}
	if hashes[0] == "" || hashes[0] == hashes[1] {
		t.Fatal("resume could silently change schema mode")
	}
}
