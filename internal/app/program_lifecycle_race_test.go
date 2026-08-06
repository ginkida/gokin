package app

import (
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHasProgramConcurrentLifecycleAccess(t *testing.T) {
	a := &App{}
	program := &tea.Program{}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			a.programMu.Lock()
			if i%2 == 0 {
				a.program = program
			} else {
				a.program = nil
			}
			a.programMu.Unlock()
		}
	}()
	for range 2 {
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = a.hasProgram()
			}
		}()
	}
	wg.Wait()
}
