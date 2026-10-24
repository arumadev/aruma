package core

import (
	"testing"

	"github.com/arumadev/aruma/internal/platform/disposition"
)

// The stored default disposition is platform/disposition's escaping, the one media's download
// default uses too: PY-RR storedObjects (TestDefaultDisposition), and every byte alone.
func TestDefaultDispositionIsThePackages(t *testing.T) {
	const pyrr = "inline; filename*=utf-8''na%C3%AFve%20%27q%27%201+1%25%3Bx.txt" // PY-RR
	if got := defaultDisposition("dir/naïve 'q' 1+1%;x.txt"); got != pyrr {
		t.Errorf("%q, want %q", got, pyrr)
	}
	for c := range 256 {
		if c == '/' {
			continue
		}
		name := "dir/" + string([]byte{byte(c)})
		if got, want := defaultDisposition(name), "inline; "+disposition.Filename(name); got != want {
			t.Errorf("byte %#x: %q, want %q", c, got, want)
		}
	}
}
