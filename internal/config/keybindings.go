package config

import (
	"fmt"
	"strings"
)

func ValidateKeybindings(profile string) error {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", "claude", "gokin":
		return nil
	default:
		return fmt.Errorf("invalid ui.keybindings %q: expected claude or gokin", profile)
	}
}
