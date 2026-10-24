package core

import (
	"errors"
	"testing"
)

// Rewrite checks both names before anything reads: a source with NUL is 400 (ADR-023 §3-4), not
// the database's refusal of it as 503.
func TestRewriteValidatesBothNames(t *testing.T) {
	e := newEnv(t)
	e.insert(e.a, "ok.txt", "abc")
	for _, c := range []struct{ src, dst string }{
		{"a\x00b", "copy.txt"},
		{"ok.txt", "a\x00b"},
		{"a\nb", "copy.txt"},
		{"", "copy.txt"},
	} {
		_, err := e.s.Rewrite(t.Context(), e.a, c.src, e.a, c.dst, asAdmin(), nil, Preconditions{})
		if _, ok := errors.AsType[*InvalidError](err); !ok {
			t.Errorf("Rewrite(%q, %q): %v, want an InvalidError (400)", c.src, c.dst, err)
		}
	}
	e.checkUsage(projectA)
}
