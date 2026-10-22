package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gokin/internal/donegate"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type reviewCapturePresenter struct {
	capturePresenter
	revisions            []string
	toolStarts, toolEnds int
}

func (p *reviewCapturePresenter) ResponseRevision(text string) {
	p.revisions = append(p.revisions, text)
}
func (p *reviewCapturePresenter) ToolStart(string, map[string]any)                 { p.toolStarts++ }
func (p *reviewCapturePresenter) ToolEnd(string, map[string]any, tools.ToolResult) { p.toolEnds++ }

func TestInteractiveCompletionReviewPresentsOneAnswerAndKeepsToolEvidence(t *testing.T) {
	for _, verdict := range []string{donegate.CompletionReviewUnchanged, "Corrected controller.go; verification found a limitation."} {
		t.Run(verdict, func(t *testing.T) {
			mock := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{}).EnqueueText(verdict)
			a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read", results: []tools.ToolResult{{Success: true, Content: "verified source"}}})
			presenter := &reviewCapturePresenter{}
			a.setPresenter(presenter)
			a.responseToolsUsed = []string{"edit"}
			a.responseTouchedPaths = []string{"controller.go"}
			response := "Implemented the controller."
			var usage turnUsageAccumulator
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if !a.runCompletionReviewIfNeeded(ctx, "implement the controller", &response, &usage) {
				t.Fatal("review stopped the original response")
			}
			if a.currentPresenter() != presenter {
				t.Fatal("review presenter was not restored")
			}
			if len(presenter.texts) != 1 || presenter.texts[0] != "Implemented the controller." {
				t.Fatalf("review duplicated or leaked prose: %v", presenter.texts)
			}
			if presenter.toolStarts != 1 || presenter.toolEnds != 1 {
				t.Fatal("quiet review hid tool results")
			}
			if usage.input == 0 || usage.modelRounds != 2 {
				t.Fatalf("quiet review lost usage accounting: %+v", usage)
			}
			if len(a.session.GetHistory()) < 3 {
				t.Fatal("review lost returned tool history")
			}
			if !strings.Contains(mock.Calls()[0].Message, donegate.CompletionReviewUnchanged) {
				t.Fatal("interactive review lacks an unchanged verdict")
			}
			if verdict == donegate.CompletionReviewUnchanged {
				if response != "Implemented the controller." || len(presenter.revisions) != 0 {
					t.Fatal("unchanged review replaced the response")
				}
			} else if response != verdict || len(presenter.revisions) != 1 || presenter.revisions[0] != verdict {
				t.Fatal("corrected review answer was not delivered")
			}
		})
	}
}

func TestInterruptedCompletionReviewRestoresPresenterAndKeepsDraft(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueStartupError(errors.New("synthetic unavailable review"))
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	presenter := &reviewCapturePresenter{}
	a.setPresenter(presenter)
	a.responseToolsUsed = []string{"edit"}
	a.responseTouchedPaths = []string{"controller.go"}
	response := "Implemented the controller."
	var usage turnUsageAccumulator
	if !a.runCompletionReviewIfNeeded(t.Context(), "implement the controller", &response, &usage) {
		t.Fatal("optional review discarded completed work")
	}
	if a.currentPresenter() != presenter || response != "Implemented the controller." || len(presenter.revisions) != 0 {
		t.Fatal("failed review corrupted presentation")
	}
	a.currentPresenter().StreamText("next turn")
	if len(presenter.texts) != 2 || presenter.texts[1] != "next turn" {
		t.Fatal("review kept suppressing later output")
	}
}
