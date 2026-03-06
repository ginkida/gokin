package context

import (
	"testing"
)

func TestSummarizerFormatTaskContext(t *testing.T) {
	s := &Summarizer{}

	// No task context
	if s.formatTaskContext() != "" {
		t.Error("nil context should return empty string")
	}

	// With full context
	s.SetTaskContext(&TaskContext{
		Title:           "Add authentication",
		Description:     "Implement OAuth2 login flow",
		CurrentStep:     "Create login endpoint",
		SuccessCriteria: []string{"Tests pass", "Login works"},
		ArtifactPaths:   []string{"/api/auth.go", "/api/auth_test.go"},
	})

	result := s.formatTaskContext()
	if result == "" {
		t.Fatal("should return non-empty context")
	}

	// Check all parts are present
	checks := []string{
		"Add authentication",
		"Implement OAuth2 login flow",
		"Create login endpoint",
		"Tests pass",
		"/api/auth.go",
	}
	for _, check := range checks {
		if !contains(result, check) {
			t.Errorf("context should contain %q, got:\n%s", check, result)
		}
	}
}

func TestSummarizerFormatTaskContextPartial(t *testing.T) {
	s := &Summarizer{}

	// Only title
	s.SetTaskContext(&TaskContext{Title: "Fix bug"})
	result := s.formatTaskContext()
	if !contains(result, "Fix bug") {
		t.Error("should contain title")
	}

	// Clear
	s.SetTaskContext(nil)
	if s.formatTaskContext() != "" {
		t.Error("should be empty after clear")
	}
}

func TestSummarizerFormatTaskContextLongDescription(t *testing.T) {
	s := &Summarizer{}

	longDesc := make([]byte, 500)
	for i := range longDesc {
		longDesc[i] = 'x'
	}

	s.SetTaskContext(&TaskContext{
		Description: string(longDesc),
	})

	result := s.formatTaskContext()
	// Description should be truncated to 300 chars + "..."
	if len(result) > 350 {
		t.Errorf("long description should be truncated, got %d chars", len(result))
	}
}

func TestTaskContextStruct(t *testing.T) {
	tc := &TaskContext{
		Title:           "Refactor DB layer",
		Description:     "Extract database operations into repository pattern",
		CurrentStep:     "Create UserRepository interface",
		SuccessCriteria: []string{"All tests pass", "No direct DB calls in handlers"},
		ArtifactPaths:   []string{"internal/repo/user.go"},
	}

	if tc.Title == "" || tc.Description == "" || tc.CurrentStep == "" {
		t.Error("all fields should be set")
	}
	if len(tc.SuccessCriteria) != 2 {
		t.Errorf("criteria = %d", len(tc.SuccessCriteria))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
