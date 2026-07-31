package app

import (
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/testkit"
)

func stringPointer(value string) *string {
	return &value
}

func TestRunSystemPromptCompositionDoesNotPersistCustomization(t *testing.T) {
	mock := testkit.NewMockClient()
	session := chat.NewSession()
	application := &App{client: mock, session: session}

	if err := application.ConfigureRunSystemPrompt(
		stringPointer("replacement"), "appendix"); err != nil {
		t.Fatal(err)
	}
	application.applySystemInstruction(mock, "canonical base", true)

	if got := mock.SystemInstruction(); got != "replacement\n\nappendix" {
		t.Fatalf("client system instruction = %q", got)
	}
	if got := session.GetSystemInstruction(); got != "canonical base" {
		t.Fatalf("persisted system instruction = %q, want canonical base", got)
	}
}

func TestRunSystemPromptSupportsEmptyReplacementAndDefaultAppend(t *testing.T) {
	t.Run("explicit empty replacement", func(t *testing.T) {
		application := &App{}
		if err := application.ConfigureRunSystemPrompt(stringPointer(""), "appendix"); err != nil {
			t.Fatal(err)
		}
		if got := application.composeRunSystemInstruction("base"); got != "appendix" {
			t.Fatalf("composition = %q, want appendix only", got)
		}
	})

	t.Run("append generated prompt", func(t *testing.T) {
		application := &App{}
		if err := application.ConfigureRunSystemPrompt(nil, "appendix"); err != nil {
			t.Fatal(err)
		}
		if got := application.composeRunSystemInstruction("base\n"); got != "base\n\nappendix" {
			t.Fatalf("composition = %q", got)
		}
	})
}

func TestStartupSystemPromptResumeRebuildsOnlyWhenCustomized(t *testing.T) {
	t.Run("ordinary resume preserves saved prompt", func(t *testing.T) {
		mock := testkit.NewMockClient()
		session := chat.NewSession()
		session.SetSystemInstruction("saved canonical")
		application := &App{client: mock, session: session}

		application.applyStartupSystemInstruction(true)

		if got := mock.SystemInstruction(); got != "saved canonical" {
			t.Fatalf("client prompt = %q", got)
		}
	})

	t.Run("custom resume rebuilds and stays out of session", func(t *testing.T) {
		workDir := t.TempDir()
		mock := testkit.NewMockClient()
		session := chat.NewSession()
		session.SetSystemInstruction("stale saved prompt")
		application := &App{
			client:        mock,
			session:       session,
			config:        config.DefaultConfig(),
			workDir:       workDir,
			promptBuilder: appcontext.NewPromptBuilder(workDir, &appcontext.ProjectInfo{}),
		}
		if err := application.ConfigureRunSystemPrompt(nil, "RUN-ONLY-MARKER"); err != nil {
			t.Fatal(err)
		}

		application.applyStartupSystemInstruction(true)

		runtimePrompt := mock.SystemInstruction()
		if !strings.Contains(runtimePrompt, "RUN-ONLY-MARKER") ||
			strings.Contains(runtimePrompt, "stale saved prompt") {
			t.Fatalf("runtime prompt was not rebuilt correctly: %q", runtimePrompt)
		}
		persisted := session.GetSystemInstruction()
		if persisted == "stale saved prompt" || strings.Contains(persisted, "RUN-ONLY-MARKER") {
			t.Fatalf("persisted prompt leaked/stayed stale: %q", persisted)
		}
	})

	t.Run("bare resume never restores a full saved prompt", func(t *testing.T) {
		workDir := t.TempDir()
		mock := testkit.NewMockClient()
		session := chat.NewSession()
		session.SetSystemInstruction("FULL-SAVED-PROMPT")
		cfg := config.DefaultConfig()
		cfg.Bare = true
		promptBuilder := appcontext.NewPromptBuilder(workDir, &appcontext.ProjectInfo{})
		promptBuilder.SetBareMode(true)
		application := &App{
			client:        mock,
			session:       session,
			config:        cfg,
			workDir:       workDir,
			promptBuilder: promptBuilder,
		}

		application.applyStartupSystemInstruction(true)

		if got := mock.SystemInstruction(); strings.Contains(got, "FULL-SAVED-PROMPT") ||
			!strings.Contains(got, "Read, Edit, and Bash") {
			t.Fatalf("bare resume prompt = %q", got)
		}
		if got := session.GetSystemInstruction(); strings.Contains(got, "FULL-SAVED-PROMPT") {
			t.Fatalf("bare canonical prompt was not refreshed: %q", got)
		}
	})
}

func TestPrepareHeadlessRuntimeKeepsRunPromptClientOnly(t *testing.T) {
	mock := testkit.NewMockClient()
	application, _ := newHeadlessPolicyTestApp(
		t, mock, &appHeadlessScriptedTool{name: "unused"})
	application.promptBuilder = appcontext.NewPromptBuilder(
		application.workDir, &appcontext.ProjectInfo{})
	if err := application.ConfigureRunSystemPrompt(nil, "HEADLESS-RUN-ONLY"); err != nil {
		t.Fatal(err)
	}

	application.prepareHeadlessRuntime()

	if !strings.Contains(mock.SystemInstruction(), "HEADLESS-RUN-ONLY") {
		t.Fatalf("headless client prompt = %q", mock.SystemInstruction())
	}
	if strings.Contains(application.session.GetSystemInstruction(), "HEADLESS-RUN-ONLY") {
		t.Fatalf("headless prompt leaked into session: %q",
			application.session.GetSystemInstruction())
	}
}

func TestRunSystemPromptPropagatesToSubAgentContext(t *testing.T) {
	workDir := t.TempDir()
	application := &App{
		promptBuilder: appcontext.NewPromptBuilder(workDir, &appcontext.ProjectInfo{}),
	}
	if err := application.ConfigureRunSystemPrompt(
		stringPointer("REPLACEMENT-MARKER"), "APPEND-MARKER"); err != nil {
		t.Fatal(err)
	}

	context := application.buildSubAgentProjectContext("inspect tests")
	for _, marker := range []string{
		"Invocation-scoped system instructions",
		"REPLACEMENT-MARKER",
		"APPEND-MARKER",
	} {
		if !strings.Contains(context, marker) {
			t.Fatalf("sub-agent context lacks %q: %q", marker, context)
		}
	}
}

func TestConfigureRunSystemPromptRejectsUnsafeOrOversizedText(t *testing.T) {
	application := &App{}
	if err := application.ConfigureRunSystemPrompt(
		stringPointer("contains\x00nul"), ""); err == nil {
		t.Fatal("NUL replacement unexpectedly accepted")
	}
	oversized := strings.Repeat("x", MaxRunSystemPromptBytes+1)
	if err := application.ConfigureRunSystemPrompt(nil, oversized); err == nil {
		t.Fatal("oversized appendix unexpectedly accepted")
	}
}
