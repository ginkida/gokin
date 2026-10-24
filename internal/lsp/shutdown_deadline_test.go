package lsp

import (
	"context"
	"testing"
	"time"
)

func TestManagerCloseBoundsPolicyWatcherJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := &Manager{ctx: ctx, cancel: cancel, watchDone: make(chan struct{}), servers: map[string]*server{}}
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Close() }()
	select {
	case <-done:
		close(m.watchDone)
	case <-time.After(3 * time.Second):
		t.Error("Close waited indefinitely for a stuck policy watcher")
		close(m.watchDone)
		<-done
	}
}
