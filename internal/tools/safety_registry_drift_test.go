package tools

import "testing"

func TestSafetyMetadataCoversRegisteredTools(t *testing.T) {
	v := NewDefaultSafetyValidator()
	known := map[string]bool{}
	for _, tool := range DefaultRegistry(t.TempDir()).List() {
		known[tool.Name()] = true
	}
	for _, name := range DefaultLazyRegistry(t.TempDir()).Names() {
		known[name] = true
	}
	for name := range known {
		meta, ok := v.GetMetadata(name)
		if !ok || meta.Name != name || meta.SafetyLevel == "" || meta.Impact == "" {
			t.Errorf("registered tool %q has no complete safety metadata", name)
		}
	}
	for name := range v.metadata {
		if !known[name] {
			t.Errorf("safety metadata names unregistered tool %q", name)
		}
	}
	check, err := v.ValidateSafety(t.Context(), "glob", map[string]any{"pattern": "*.go"})
	if err != nil || !check.IsValid || len(check.Warnings) != 0 {
		t.Fatalf("glob safety: %+v, %v", check, err)
	}
}
