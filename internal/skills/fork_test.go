package skills

import (
	"strings"
	"testing"
)

func TestCatalogForkSkillControls(t *testing.T) {
	for _, tc := range []struct {
		fields     string
		valid      bool
		agent      string
		background bool
	}{
		{"context: fork", true, "", true},
		{"context: fork\nagent: Explore\nbackground: false", true, "Explore", false},
		{"context: fork\nagent: team-reviewer", true, "team-reviewer", true},
		{"context: inline", false, "", false},
		{"context: [fork]", false, "", false},
		{"agent: Explore", false, "", false},
		{"background: false", false, "", false},
		{"context: fork\nagent: ''", false, "", false},
		{"context: fork\nbackground: maybe", false, "", false},
	} {
		t.Run(strings.ReplaceAll(tc.fields, "\n", " "), func(t *testing.T) {
			root := t.TempDir()
			writeSkill(t, root, "review", "---\ndescription: Review code\n"+tc.fields+"\n---\nReview $ARGUMENTS.")
			catalog := NewCatalog([]Root{{Path: root, Source: "claude-project"}})
			skill, ok := catalog.Get("review")
			if ok != tc.valid {
				t.Fatalf("loaded=%v warnings=%v", ok, catalog.Warnings())
			}
			if ok && (skill.Context != "fork" || skill.Agent != tc.agent || skill.Background != tc.background) {
				t.Fatalf("controls=%+v", skill)
			}
		})
	}
}

func TestCatalogForkModelSettings(t *testing.T) {
	for _, tc := range []struct {
		fields string
		valid  bool
	}{
		{"model: inherit", true}, {"model: sonnet\neffort: medium", true},
		{"model: glm-5.3-flash\neffort: max", true}, {"effort: xhigh", true},
		{"model: [glm-5.3]", false}, {"model: ''", false}, {"model: 7", false},
		{"effort: auto", false}, {"effort: enormous", false}, {"effort: [high]", false},
	} {
		t.Run(strings.ReplaceAll(tc.fields, "\n", " "), func(t *testing.T) {
			root := t.TempDir()
			writeSkill(t, root, "review", "---\ncontext: fork\n"+tc.fields+"\n---\nReview code.")
			catalog := NewCatalog([]Root{{Path: root, Source: "claude-project"}})
			_, ok := catalog.Get("review")
			if ok != tc.valid {
				t.Fatalf("loaded=%v warnings=%v", ok, catalog.Warnings())
			}
		})
	}
}

func TestCatalogInlineModelSettings(t *testing.T) {
	for _, fields := range []string{"model: sonnet", "model: inherit\neffort: max", "effort: medium", "model: glm-5.3-flash\neffort: low"} {
		t.Run(fields, func(t *testing.T) {
			root := t.TempDir()
			writeSkill(t, root, "review", "---\n"+fields+"\n---\nReview code.")
			catalog := NewCatalog([]Root{{Path: root, Source: "claude-project"}})
			loaded, ok := catalog.Get("review")
			if !ok || loaded.Context != "" || loaded.Model == "" && loaded.Effort == "" {
				t.Fatalf("skill=%+v warnings=%v", loaded, catalog.Warnings())
			}
		})
	}
}
