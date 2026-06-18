package agent

import (
	"testing"

	"gokin/internal/config"
)

// TestSubAgentThinkingBudget pins the per-agent adaptive thinking: lightweight
// agents (explore/bash/guide) skip reasoning on auto; general/plan reason; off
// is always 0; on forces a budget even for lightweight types.
func TestSubAgentThinkingBudget(t *testing.T) {
	cases := []struct {
		typ  AgentType
		mode string
		want int32
	}{
		// auto — type is the complexity signal
		{AgentTypeExplore, config.ThinkingModeAuto, 0},
		{AgentTypeBash, config.ThinkingModeAuto, 0},
		{AgentTypeGuide, config.ThinkingModeAuto, 0},
		{AgentTypeGeneral, config.ThinkingModeAuto, 8192},
		{AgentTypePlan, config.ThinkingModeAuto, 8192},
		{AgentType("custom"), config.ThinkingModeAuto, 8192}, // unknown = heavy
		// empty mode resolves to auto
		{AgentTypeGeneral, "", 8192},
		{AgentTypeExplore, "", 0},
		// off — never reason
		{AgentTypeGeneral, config.ThinkingModeOff, 0},
		{AgentTypeExplore, config.ThinkingModeOff, 0},
		// on — force; lightweight gets the floor, heavy gets full
		{AgentTypeExplore, config.ThinkingModeOn, 4096},
		{AgentTypeGeneral, config.ThinkingModeOn, 8192},
	}
	for _, c := range cases {
		if got := SubAgentThinkingBudget(c.typ, c.mode); got != c.want {
			t.Errorf("SubAgentThinkingBudget(%q, %q) = %d, want %d", c.typ, c.mode, got, c.want)
		}
	}
}

// TestAgentTypeIsLightweight pins the classification the budget helper relies on.
func TestAgentTypeIsLightweight(t *testing.T) {
	light := []AgentType{AgentTypeExplore, AgentTypeBash, AgentTypeGuide}
	heavy := []AgentType{AgentTypeGeneral, AgentTypePlan, AgentType("custom")}
	for _, t2 := range light {
		if !t2.IsLightweight() {
			t.Errorf("%q should be lightweight", t2)
		}
	}
	for _, t2 := range heavy {
		if t2.IsLightweight() {
			t.Errorf("%q should NOT be lightweight", t2)
		}
	}
}
