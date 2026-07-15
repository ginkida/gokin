package context

import (
	"strings"
	"sync"
	"testing"
)

func TestPromptBuilderConcurrentBuildAndInvalidation(t *testing.T) {
	b := NewPromptBuilder("/tmp/project", &ProjectInfo{
		Type:          ProjectTypeGo,
		Name:          "project",
		TestFramework: "go test ./...",
	})

	const iterations = 200
	var wg sync.WaitGroup
	run := func(fn func(int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				fn(i)
			}
		}()
	}

	run(func(int) { _ = b.Build() })
	run(func(i int) {
		b.Invalidate()
		b.SetDetectedContext("context")
		b.SetToolHints("use targeted tests")
		b.SetLastMessage("fix the race")
	})
	run(func(i int) {
		b.SetProvider([]string{"glm", "kimi"}[i%2])
		b.SetPrefixCachingEnabled(i%2 == 0)
		b.SetPlanMode(i%2 == 0)
	})
	run(func(int) {
		_ = b.BuildPlanExecutionPrompt("Reliability", "Keep work resumable", []PlanStepInfo{{ID: 1, Title: "Verify"}})
	})
	run(func(int) { _ = b.BuildSubAgentPromptForTask("audit tool execution") })
	run(func(int) { _ = b.GetProjectSummary() })
	wg.Wait()

	b.SetProvider("glm")
	b.SetPlanMode(false)
	b.SetDetectedContext("stable context")
	b.Invalidate()
	got := b.Build()
	for _, want := range []string{"You are Gokin", "stable context", "GLM-specific"} {
		if !strings.Contains(got, want) {
			t.Fatalf("final prompt missing %q", want)
		}
	}
}
