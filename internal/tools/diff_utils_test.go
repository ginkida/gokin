package tools

import "testing"

func TestIsTrivialDiff(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
		want bool
	}{
		{"identical", "hello world", "hello world", true},
		{"trailing newline added", "foo", "foo\n", true},
		{"spaces collapsed", "a  b  c", "a b c", true},
		{"tabs vs spaces", "a\tb\tc", "a b c", true},
		{"mixed whitespace trim", "  foo bar  ", "foo bar", true},
		{"empty vs whitespace", "", "   ", true},
		{"real content change", "hello", "goodbye", false},
		{"single char added", "foo bar", "foo bars", false},
		{"punctuation added", "hello world", "hello, world", false},
		{"line reordered", "a\nb\nc", "a\nc\nb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsTrivialDiff(c.old, c.new)
			if got != c.want {
				t.Errorf("IsTrivialDiff(%q, %q) = %v, want %v", c.old, c.new, got, c.want)
			}
		})
	}
}
