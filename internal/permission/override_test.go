package permission

import "testing"

func TestSetToolRiskOverride_OverridesBuiltIn(t *testing.T) {
	const name = "test_tool_override_builtin"
	t.Cleanup(func() { ClearToolRiskOverride(name) })

	// Without an override, an unknown tool gets RiskMedium (fallback).
	if got := GetToolRiskLevel(name); got != RiskMedium {
		t.Fatalf("baseline: got %v, want RiskMedium", got)
	}

	SetToolRiskOverride(name, RiskHigh)
	if got := GetToolRiskLevel(name); got != RiskHigh {
		t.Errorf("after override: got %v, want RiskHigh", got)
	}

	ClearToolRiskOverride(name)
	if got := GetToolRiskLevel(name); got != RiskMedium {
		t.Errorf("after clear: got %v, want RiskMedium", got)
	}
}

func TestSetToolRiskOverride_ShadowsBuiltInName(t *testing.T) {
	// "read" is RiskLow by default. Override to High — verify the built-in
	// entry doesn't take precedence over the override.
	t.Cleanup(func() { ClearToolRiskOverride("read") })

	if got := GetToolRiskLevel("read"); got != RiskLow {
		t.Fatalf("baseline read: got %v, want RiskLow", got)
	}
	SetToolRiskOverride("read", RiskHigh)
	if got := GetToolRiskLevel("read"); got != RiskHigh {
		t.Errorf("overridden read: got %v, want RiskHigh", got)
	}
}

func TestSetToolRiskOverride_EmptyNameNoOp(t *testing.T) {
	// Passing empty name should not poison the override map.
	SetToolRiskOverride("", RiskHigh)
	if got := GetToolRiskLevel(""); got == RiskHigh {
		t.Error("empty name should not accept override")
	}
}

func TestClearToolRiskOverride_UnknownNameNoOp(t *testing.T) {
	// Clearing a non-existent override is safe (no panic, no error).
	ClearToolRiskOverride("never_registered_tool_name")
}

func TestParseRiskLevel_KnownValues(t *testing.T) {
	cases := map[string]RiskLevel{
		"low":     RiskLow,
		"LOW":     RiskLow,
		"  Low ":  RiskLow,
		"medium":  RiskMedium,
		"high":    RiskHigh,
		"HIGH":    RiskHigh,
		"":        RiskMedium, // default
		"nonsense": RiskMedium, // unknown → default (safe)
	}
	for input, want := range cases {
		if got := ParseRiskLevel(input); got != want {
			t.Errorf("ParseRiskLevel(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestParseRiskLevel_UnknownDefaultsToMedium(t *testing.T) {
	// Explicit test for the security-conscious default: a typo should not
	// silently lower risk (e.g. "lower" ≠ "low" should not become RiskLow).
	if got := ParseRiskLevel("lower"); got != RiskMedium {
		t.Errorf("ParseRiskLevel(\"lower\") = %v, want RiskMedium (don't silently lower)", got)
	}
}
