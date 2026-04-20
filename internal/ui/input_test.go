package ui

import (
	"reflect"
	"testing"
)

func TestRecordRecentCommand(t *testing.T) {
	newModel := func() *InputModel {
		m := NewInputModel(nil, "")
		return &m
	}

	t.Run("empty name ignored", func(t *testing.T) {
		m := newModel()
		m.RecordRecentCommand("")
		if len(m.recentCommands) != 0 {
			t.Errorf("empty name should not be recorded: %v", m.recentCommands)
		}
	})

	t.Run("push moves to front", func(t *testing.T) {
		m := newModel()
		m.RecordRecentCommand("a")
		m.RecordRecentCommand("b")
		m.RecordRecentCommand("c")
		want := []string{"c", "b", "a"}
		if !reflect.DeepEqual(m.recentCommands, want) {
			t.Errorf("got %v, want %v", m.recentCommands, want)
		}
	})

	t.Run("dedup bumps existing entry", func(t *testing.T) {
		m := newModel()
		m.RecordRecentCommand("a")
		m.RecordRecentCommand("b")
		m.RecordRecentCommand("a") // re-record a → moves to front
		want := []string{"a", "b"}
		if !reflect.DeepEqual(m.recentCommands, want) {
			t.Errorf("got %v, want %v", m.recentCommands, want)
		}
	})

	t.Run("caps at 5 entries", func(t *testing.T) {
		m := newModel()
		for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
			m.RecordRecentCommand(n)
		}
		want := []string{"g", "f", "e", "d", "c"}
		if !reflect.DeepEqual(m.recentCommands, want) {
			t.Errorf("got %v, want %v", m.recentCommands, want)
		}
	})

	t.Run("dedup in middle preserves order", func(t *testing.T) {
		m := newModel()
		m.RecordRecentCommand("a")
		m.RecordRecentCommand("b")
		m.RecordRecentCommand("c")
		m.RecordRecentCommand("b") // bump b from middle
		want := []string{"b", "c", "a"}
		if !reflect.DeepEqual(m.recentCommands, want) {
			t.Errorf("got %v, want %v", m.recentCommands, want)
		}
	})
}

func TestSetCommandAliases(t *testing.T) {
	m := NewInputModel(nil, "")
	m.SetCommandAliases(map[string]string{"P": "Plan", "c": "Commit"})
	// Lowercased on storage so matching is case-insensitive.
	if got := m.commandAliases["p"]; got != "plan" {
		t.Errorf("commandAliases[p] = %q, want plan", got)
	}
	if got := m.commandAliases["c"]; got != "commit" {
		t.Errorf("commandAliases[c] = %q, want commit", got)
	}
	// Nil input clears the map — explicit reset semantics.
	m.SetCommandAliases(nil)
	if m.commandAliases != nil {
		t.Errorf("nil input should clear aliases, got %v", m.commandAliases)
	}
}
