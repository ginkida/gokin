package paging

import (
	"reflect"
	"testing"
)

func TestPage(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	cases := []struct {
		name       string
		page, size int
		want       []int
	}{
		{"first page starts at the first item", 1, 3, []int{0, 1, 2}},
		{"second page", 2, 3, []int{3, 4, 5}},
		{"last partial page", 4, 3, []int{9}},
		{"page past the end is empty", 5, 3, []int{}},
		{"page below 1 clamps to the first page", 0, 3, []int{0, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Page(items, tc.page, tc.size)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Page(items, %d, %d) = %v, want %v", tc.page, tc.size, got, tc.want)
			}
		})
	}
}
