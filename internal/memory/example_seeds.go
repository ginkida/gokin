package memory

import (
	"time"

	"gokin/internal/logging"
)

// SeedDefaults populates the example store with curated high-quality examples
// if the store is currently empty. Ensures fresh installations have useful
// reference examples for common error-prone task types.
func SeedDefaults(es *ExampleStore) {
	es.mu.RLock()
	count := len(es.examples)
	es.mu.RUnlock()
	if count > 0 {
		return
	}

	for _, seed := range curatedSeeds {
		addSeed(es, seed)
	}
	if err := es.save(); err != nil {
		// Was `_ = es.save()` — silent. If the seed save fails (disk
		// full, perm denied), the agent loses curated examples for the
		// entire session with no signal. Warn so post-mortem can see it.
		logging.Warn("failed to persist seeded example store — agent will run without curated examples this session",
			"error", err)
	}
}

type seedExample struct {
	TaskType string
	Prompt   string
	Agent    string
	Output   string
	Tools    []string
	Tags     []string
}

func addSeed(es *ExampleStore, seed seedExample) {
	es.mu.Lock()
	defer es.mu.Unlock()

	id := "seed-" + generateExampleID()
	example := &TaskExample{
		ID:           id,
		TaskType:     seed.TaskType,
		InputPrompt:  seed.Prompt,
		AgentType:    seed.Agent,
		ToolsUsed:    seed.Tools,
		FinalOutput:  seed.Output,
		SuccessScore: 0.95,
		Tags:         seed.Tags,
		Created:      time.Now(),
	}
	es.examples[id] = example
	es.byType[seed.TaskType] = append(es.byType[seed.TaskType], id)
}

var curatedSeeds = []seedExample{
	{
		TaskType: "implement",
		Prompt:   "Create a GitHub Actions CI workflow for this Go project",
		Agent:    "general",
		Output: "Created .github/workflows/ci.yml. Key decisions: " +
			"(1) Go version read from go.mod, not hardcoded — prevents version mismatch. " +
			"(2) All actions pinned to tags (actions/checkout@v4), never @master. " +
			"(3) Uses golangci-lint official action instead of curl|sh. " +
			"(4) Single test step with -race -v, not duplicate runs.",
		Tools: []string{"read", "glob", "write"},
		Tags:  []string{"github", "actions", "workflow", "ci", "go", "version", "lint"},
	},
	{
		TaskType: "implement",
		Prompt:   "Write tests for this Go package",
		Agent:    "general",
		Output: "Created *_test.go. Patterns applied: " +
			"(1) testing.Short() guard on tests making HTTP/network calls. " +
			"(2) Table-driven tests with t.Run for subtests. " +
			"(3) Every error return checked with t.Fatalf, never '_ = err'. " +
			"(4) All assertions use t.Errorf with got/want format. " +
			"(5) env vars saved/restored in defer for isolation.",
		Tools: []string{"read", "write"},
		Tags:  []string{"test", "testing", "unit", "go", "assertions", "table-driven"},
	},
	{
		TaskType: "refactoring",
		Prompt:   "Update the Go version across all config files",
		Agent:    "general",
		Output: "Updated go.mod and all .github/workflows/*.yml. " +
			"Verified version consistency: go directive in go.mod matches " +
			"go-version in every workflow file. Used single env var (GO_VERSION) " +
			"at top of each workflow to avoid repetition.",
		Tools: []string{"read", "glob", "grep", "edit"},
		Tags:  []string{"version", "go", "config", "consistency", "workflow", "go.mod"},
	},
	{
		TaskType: "implement",
		Prompt:   "Create a release workflow with GitHub Actions",
		Agent:    "general",
		Output: "Created .github/workflows/release.yml. Key patterns: " +
			"(1) Version derived from tag in same step where it's used (not step outputs as shell vars). " +
			"(2) Binaries archived as .tar.gz/.zip before upload. " +
			"(3) Homebrew formula generated AFTER release exists, not before. " +
			"(4) Shell heredocs use unquoted delimiter (<< EOF) for variable interpolation. " +
			"(5) SHA256 checksums generated in release job from all artifacts.",
		Tools: []string{"read", "write"},
		Tags:  []string{"release", "github", "actions", "workflow", "homebrew", "checksums", "archive"},
	},
}
