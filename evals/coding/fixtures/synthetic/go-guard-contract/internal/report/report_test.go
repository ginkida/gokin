package report

import "testing"

func TestRenderEmpty(t *testing.T) {
	if got := Render(nil); got != "" {
		t.Fatalf("Render(nil) = %q, want empty string", got)
	}
	if got := Render([]string{}); got != "" {
		t.Fatalf("Render([]string{}) = %q, want empty string", got)
	}
}

func TestRenderItems(t *testing.T) {
	want := "== report ==\n- a\n- b\n"
	if got := Render([]string{"a", "b"}); got != want {
		t.Fatalf("Render([a b]) = %q, want %q", got, want)
	}
}
