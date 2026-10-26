package index

import (
	"slices"
	"testing"
)

func TestPathSegments(t *testing.T) {
	for path, want := range map[string][]string{
		"a":             {"a"},
		"m.x":           {"m", "x"},
		"`a.b`.c":       {"a.b", "c"},
		"m.`x\\`y`":     {"m", "x`y"},
		"`a\\\\`.`b`":   {"a\\", "b"},
		NameField:       {NameField},
		"`unterminated": {"unterminated"},
	} {
		if got, _ := pathSegments(path); !slices.Equal(got, want) {
			t.Errorf("pathSegments(%q) = %q, want %q", path, got, want)
		}
	}
	for path, want := range map[string][]string{
		"a":           nil,
		"m.x.y":       {"m.x", "m"},
		"`a.b`.c":     {"`a.b`"},
		"m.`x.y`.`z`": {"m.`x.y`", "m"},
	} {
		if got := ancestors(path); !slices.Equal(got, want) {
			t.Errorf("ancestors(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestComparePaths(t *testing.T) {
	// Segment by segment, unescaped, not by canonical string: "`~`" is the segment "~", after
	// every letter, although its backtick sorts before them; "`a b`" is "a b", before "a0".
	sorted := []string{"a", "a.b", "a.z", "`a b`", "a0", "a_b", "b", "m.x", "`~`"}
	for i := range sorted {
		for j := range sorted {
			if got, want := comparePaths(sorted[i], sorted[j]), cmpInt(i, j); got != want {
				t.Errorf("comparePaths(%q, %q) = %d, want %d", sorted[i], sorted[j], got, want)
			}
		}
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
