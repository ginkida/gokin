package report

import (
	"reflect"
	"testing"
)

func TestKeysDeterministicOrder(t *testing.T) {
	totals := map[string]int{"delta": 4, "alpha": 1, "charlie": 3, "bravo": 2, "echo": 5}
	want := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for i := 0; i < 10; i++ {
		if got := Keys(totals); !reflect.DeepEqual(got, want) {
			t.Fatalf("Keys() = %v, want %v (run %d)", got, want, i)
		}
	}
}
