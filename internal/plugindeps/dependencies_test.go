package plugindeps

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDependencyParsingAndNPMVersionRanges(t *testing.T) {
	dependencies, err := Parse(json.RawMessage(`["base", "cross@other", {"name":"tagged","version":"^2.1 || ~3.0","marketplace":"shared"}]`))
	if err != nil || len(dependencies) != 3 || dependencies[0].ID("bundle@team") != "base@team" || dependencies[1].ID("bundle@team") != "cross@other" {
		t.Fatalf("parse=%+v %v", dependencies, err)
	}
	for _, raw := range []string{`["base@"]`, `[{"name":"base","version":null}]`, `[{"name":"base","marketplace":3}]`, `[{"name":"base","unknown":"x"}]`, `[{"name":"base","name":"other"}]`, `["../escape"]`, `["base","base"]`, `{"base":true}`, `[{"name":"base","version":"not a range"}]`} {
		if _, err := Parse(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted malformed %s", raw)
		}
	}
	for _, tc := range []struct {
		version, rangeValue string
		want                bool
	}{
		{"2.1.5", "^2.0", true}, {"3.0.0", "^2.0", false}, {"0.2.9", "^0.2.1", true}, {"0.3.0", "^0.2.1", false},
		{"2.1.9", "~2.1.0", true}, {"2.2.0", "~2.1.0", false}, {"2.1.0", "=2.1.0", true}, {"2.1.1", "=2.1.0", false},
		{"3.0.1", "^2.1 || ~3.0", true}, {"1.5.0", "1.2.0 - 1.9.0", true}, {"2.0.0-beta.2", "^2.0.0-0", true},
		{"2.1.0-beta.1", "^2.0.0-0", false}, {"2.0.0-beta.1", "^2.0", false}, {"2.0.0-beta.3", "^2.0.0-0 <3.0.0", true},
		{"v2.0.0", "^2.0", true}, {"", "^2.0", false}, {"arbitrary", "", true},
	} {
		if got := Satisfies(tc.version, tc.rangeValue); got != tc.want {
			t.Fatalf("%s satisfies %s=%t want %t", tc.version, tc.rangeValue, got, tc.want)
		}
	}
}

func TestDependencyRuntimeTransitiveFailuresCyclesAndLocalOverride(t *testing.T) {
	nodes := map[string]Node{
		"bundle@team":    {ID: "bundle@team", Enabled: true, Dependencies: []Dependency{{Name: "middle"}}},
		"middle@team":    {ID: "middle@team", Enabled: true, Dependencies: []Dependency{{Name: "leaf", Version: "^2.0"}}},
		"leaf@team":      {ID: "leaf@team", Enabled: true, Version: "3.0.0"},
		"unrelated@team": {ID: "unrelated@team", Enabled: true},
	}
	issues := Check(nodes)
	if len(issues) != 2 || !strings.Contains(issues["bundle@team"], "leaf@team") {
		t.Fatalf("transitive=%+v", issues)
	}
	nodes["leaf@inline"] = Node{ID: "leaf@inline", Local: true, Enabled: true}
	if issues := Check(nodes); len(issues) != 0 {
		t.Fatalf("local override should bypass range: %+v", issues)
	}
	nodes["leaf@inline"] = Node{ID: "leaf@inline", Local: true, Enabled: false}
	if issues := Check(nodes); !strings.Contains(issues["middle@team"], "leaf@inline") {
		t.Fatalf("disabled local override=%+v", issues)
	}
	delete(nodes, "leaf@inline")
	nodes["leaf@team"] = Node{ID: "leaf@team", Version: "2.1.0", Enabled: true, Dependencies: []Dependency{{Name: "bundle"}}}
	if issues := Check(nodes); len(issues) != 3 || !strings.Contains(issues["bundle@team"], "circular") {
		t.Fatalf("cycle=%+v", issues)
	}
}
