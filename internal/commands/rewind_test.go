package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/rewind"
)

type rewindFakeApp struct {
	*fakeAppForMCP
	points   []rewind.Point
	id, mode string
	guide    string
}

func (a *rewindFakeApp) ListRewindPoints(context.Context) ([]rewind.Point, error) {
	return a.points, nil
}
func (a *rewindFakeApp) RewindToPoint(_ context.Context, id, mode string) (string, error) {
	a.id, a.mode = id, mode
	return "restored", nil
}
func TestRewindCommandSelectsStableIDAndRequiresMode(t *testing.T) {
	a := &rewindFakeApp{fakeAppForMCP: &fakeAppForMCP{}, points: []rewind.Point{{ID: strings.Repeat("a", 32), Prompt: "first\nforged"}, {ID: strings.Repeat("b", 32), Prompt: "second"}}}
	c := &RewindCommand{}
	result, err := c.Execute(t.Context(), nil, a)
	if err != nil || strings.Contains(result, "first\nforged") || !strings.Contains(result, "first forged") {
		t.Fatal("unsafe or missing list", result, err)
	}
	for _, args := range [][]string{{"1"}, {"0", "both"}, {"3", "both"}, {"1", "unsafe"}, {"1", "both", "extra"}} {
		if _, err := c.Execute(t.Context(), args, a); err == nil {
			t.Fatal("invalid selection accepted", args)
		}
	}
	if a.id != "" {
		t.Fatal("invalid selection changed session")
	}
	if _, err := c.Execute(t.Context(), []string{"2", "conversation"}, a); err != nil {
		t.Fatal(err)
	}
	if a.id != strings.Repeat("b", 32) || a.mode != "conversation" {
		t.Fatal("wrong checkpoint selected")
	}
}

func (a *rewindFakeApp) SummarizeAtPoint(_ context.Context, id, mode, guide string) (string, error) {
	a.id, a.mode, a.guide = id, mode, guide
	return "summarized", nil
}
func (a *rewindFakeApp) ReadSummaryTranscript(_ context.Context, id string) (string, error) {
	a.id = id
	return "original transcript", nil
}
func TestRewindSummaryGuidanceRoundTripsQuotedInput(t *testing.T) {
	a := &rewindFakeApp{fakeAppForMCP: &fakeAppForMCP{}, points: []rewind.Point{{ID: strings.Repeat("c", 32)}}}
	handler := NewHandler()
	name, args, ok := handler.Parse(`/rewind 1 summarize-from -- "Keep Bob's migration and \"rollback\" details"`)
	if !ok || name != "rewind" {
		t.Fatal("command parse failed")
	}
	if _, err := (&RewindCommand{}).Execute(t.Context(), args, a); err != nil {
		t.Fatal(err)
	}
	if a.mode != "summarize-from" || a.guide != `Keep Bob's migration and "rollback" details` {
		t.Fatal("guidance changed", a.guide)
	}
	if result, err := (&RewindCommand{}).Execute(t.Context(), []string{"transcript", "archive-id"}, a); err != nil || result != "original transcript" || a.id != "archive-id" {
		t.Fatal("transcript route failed", result, err)
	}
}
