package router

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/toolschema"
)

func TestRouterDeferredPublicationSurvivesTaskChangesAndPlanMode(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("Analyzed.").EnqueueText("Ready.").EnqueueText("Plan.")
	registry := tools.DefaultRegistry(testkit.ResolvedTempDir(t))
	executor := tools.NewExecutor(registry, mock, time.Second)
	r := NewRouter(&RouterConfig{Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100}, executor, nil, mock, registry, false, t.TempDir())
	toolschema.Install(mock, toolschema.New(true))
	mock.SetTools(registry.GeminiTools())
	first, _ := json.Marshal(mock.GetTools())
	ctx := tools.ContextWithDeferredCatalog(t.Context(), registry, registry.GeminiTools())
	for _, prompt := range []string{"analyze the module", "implement the feature"} {
		if _, _, err := r.Execute(ctx, nil, prompt); err != nil {
			t.Fatal(err)
		}
		next, _ := json.Marshal(mock.GetTools())
		if !bytes.Equal(first, next) {
			t.Fatal("routing changed the schema prefix")
		}
	}
	r.SetPlanMode(true)
	if _, _, err := r.Execute(ctx, nil, "analyze the module"); err != nil {
		t.Fatal(err)
	}
	names := declNames(mock.GetTools())
	if names["write"] || names["bash"] || names["edit"] || !names["tool_search"] || !names["use_tool"] {
		t.Fatal("plan-mode publication lost its safety boundary", names)
	}
}
