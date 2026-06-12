package window

import (
	"reflect"
	"testing"
)

func TestLastN(t *testing.T) {
	items := []string{"a", "b", "c", "d"}

	tests := []struct {
		name string
		n    int
		want []string
	}{
		{"zero", 0, nil},
		{"negative", -1, nil},
		{"one", 1, []string{"d"}},
		{"some", 2, []string{"c", "d"}},
		{"exact length", 4, []string{"a", "b", "c", "d"}},
		{"over length", 7, []string{"a", "b", "c", "d"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LastN(items, tt.n)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LastN(%v, %d) = %v, want %v", items, tt.n, got, tt.want)
			}
		})
	}
}
