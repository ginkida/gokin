package context

import (
	"sync"
	"testing"

	"gokin/internal/testkit"
)

func TestSummarizerRuntimeUpdatesAreRaceSafe(t *testing.T) {
	s := NewSummarizer(testkit.NewMockClient())

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.SetClient(testkit.NewMockClient())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.SetTaskContext(&TaskContext{Title: "live task", ArtifactPaths: []string{"app.go"}})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = s.clientSnapshot()
			_ = s.formatTaskContext()
			_ = s.ensureCriticalContext("summary")
		}
	}()
	wg.Wait()
}
