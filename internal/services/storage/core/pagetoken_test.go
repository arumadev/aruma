package core

import (
	"bytes"
	"testing"

	"github.com/arumadev/aruma/internal/platform/crypto"
)

func otherKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	k, err := crypto.NewKeys(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPageTokenRoundTrip(t *testing.T) {
	s := &Service{keys: testKeys(t)}
	b := Bucket{testProject, "chat-app.aruma.test"}
	o := ListOptions{Prefix: "a/", Delimiter: "/", Surface: SurfaceFirebase}
	for _, c := range []pageCursor{{last: "a/b"}, {last: "a/c/", prefix: true}, {last: "a/" + string(bytes.Repeat([]byte("é"), 511))}} {
		o.PageToken = s.encodePageToken(b, o, c)
		got, err := s.decodePageToken(b, o)
		if err != nil || got != c {
			t.Errorf("%+v: decodes to %+v, %v", c, got, err)
		}
	}
	// Each part of the list is bound: a change to any one fails.
	o.PageToken = s.encodePageToken(b, o, pageCursor{last: "a/b"})
	for _, c := range []struct {
		b Bucket
		o ListOptions
	}{
		{Bucket{testProject + 1, b.Name}, o},
		{Bucket{testProject, b.Name + "x"}, o},
		{b, ListOptions{Prefix: "a", Delimiter: "//", Surface: SurfaceFirebase, PageToken: o.PageToken}}, // the same bytes, cut elsewhere
		{b, ListOptions{Prefix: "a/", Delimiter: "/", Surface: SurfaceConsole, PageToken: o.PageToken}},
		{b, ListOptions{Prefix: "a/", Delimiter: "/", StartOffset: "a", Surface: SurfaceFirebase, PageToken: o.PageToken}},
		{b, ListOptions{Prefix: "a/", Delimiter: "/", EndOffset: "b", Surface: SurfaceFirebase, PageToken: o.PageToken}},
	} {
		if _, err := s.decodePageToken(c.b, c.o); err != ErrInvalidPageToken {
			t.Errorf("%+v %+v: %v", c.b, c.o, err)
		}
	}
}

func TestPageCursorNext(t *testing.T) {
	if n, ok := (pageCursor{last: "a/b"}).next(); !ok || n != "a/b\x01" {
		t.Errorf("after an object: %q, %v", n, ok)
	}
	if n, ok := (pageCursor{last: "a/c/", prefix: true}).next(); !ok || n != "a/c0" {
		t.Errorf("after a prefix: %q, %v", n, ok)
	}
	if _, ok := (pageCursor{last: "\U0010FFFF", prefix: true}).next(); ok {
		t.Error("past the last prefix there is something")
	}
}

// Decoding never panics, and nothing but what encodePageToken made verifies.
func FuzzDecodePageToken(f *testing.F) {
	keys, err := crypto.NewKeys(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		f.Fatal(err)
	}
	s := &Service{keys: keys}
	b := Bucket{testProject, "chat-app.aruma.test"}
	o := ListOptions{Prefix: "a/", Delimiter: "/", Surface: SurfaceFirebase}
	f.Add(s.encodePageToken(b, o, pageCursor{last: "a/b"}))
	f.Add(s.encodePageToken(b, o, pageCursor{last: "a/c/", prefix: true}))
	f.Add("")
	f.Add("AQ")
	f.Fuzz(func(t *testing.T, token string) {
		o := o
		o.PageToken = token
		c, err := s.decodePageToken(b, o)
		if err != nil {
			if err != ErrInvalidPageToken {
				t.Fatalf("%q: %v", token, err)
			}
			return
		}
		if s.encodePageToken(b, o, c) != token {
			t.Fatalf("%q verifies but is not the token of %+v", token, c)
		}
	})
}
