package app

import "gokin/internal/ui"

// Internal review retains live tool outcomes, progress, warnings and permission
// decisions. Only its provisional prose/thinking is withheld until its verdict.
type completionReviewPresenter struct{ agentPresenter }

func (*completionReviewPresenter) StreamText(string)     {}
func (*completionReviewPresenter) StreamThinking(string) {}

func (a *App) quietCompletionReview() func() {
	a.mu.Lock()
	previous := a.presenter
	delegate := previous
	if delegate == nil {
		delegate = &tuiPresenter{app: a}
	}
	quiet := &completionReviewPresenter{agentPresenter: delegate}
	a.presenter = quiet
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		if a.presenter == quiet {
			a.presenter = previous
		}
		a.mu.Unlock()
	}
}

func (p *tuiPresenter) ResponseRevision(text string) {
	if p.app.hasProgram() {
		p.app.safeSendToProgram(ui.ResponseRevisedMsg{Text: text})
	}
}

func (a *App) presentReviewedRevision(text string) {
	presenter := a.currentPresenter()
	if revisions, ok := presenter.(interface{ ResponseRevision(string) }); ok {
		revisions.ResponseRevision(text)
	} else {
		presenter.StreamText("\n\nUpdated result\n\n" + text)
	}
}
