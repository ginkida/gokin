package ui

import (
	"time"
)

// SpinnerType represents different spinner animation contexts.
type SpinnerType string

const (
	SpinnerThinking SpinnerType = "thinking" // AI thinking/processing
	SpinnerNetwork  SpinnerType = "network"  // Network operations
	SpinnerFile     SpinnerType = "file"     // File operations
	SpinnerSearch   SpinnerType = "search"   // Search operations
	SpinnerBuild    SpinnerType = "build"    // Build/compile operations
	SpinnerDefault  SpinnerType = "default"  // Default spinner
)

// Spinners provides different animation frames for different contexts.
var Spinners = map[SpinnerType][]string{
	SpinnerThinking: {".", "..", "...", ""},                             // Thinking dots
	SpinnerNetwork:  {"◜", "◠", "◝", "◞", "◡", "◟"},                     // Rotating arc
	SpinnerFile:     {"◐", "◓", "◑", "◒"},                               // Quarter circles
	SpinnerSearch:   {"◴", "◷", "◶", "◵"},                               // Clock-like
	SpinnerBuild:    {"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"},           // Building blocks
	SpinnerDefault:  {"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}, // Braille dots
}

// GetSpinnerFrame returns the current frame for a spinner type.
func GetSpinnerFrame(spinnerType SpinnerType) string {
	frames, ok := Spinners[spinnerType]
	if !ok {
		frames = Spinners[SpinnerDefault]
	}
	idx := int(time.Now().UnixMilli()/100) % len(frames)
	return frames[idx]
}
