package core

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  error
	}{
		{"", "", nil},
		{"/", "", nil},
		{"/a/b", "a/b", nil}, // JS
		{"a/b", "a/b", nil},  // Android, iOS
		{"/a//b/", "a/b", nil},
		{"/users/مرحبا/😀", "users/مرحبا/😀", nil},
		{"/a b/-x_~", "a b/-x_~", nil},
		{"/bad/x.y", "", ErrInvalidPath}, // emulator: "Invalid token in path"
		{"/a#", "", ErrInvalidPath},
		{"/a$", "", ErrInvalidPath},
		{"/a[0]", "", ErrInvalidPath},
		{"/a\x01", "", ErrInvalidPath},
		{"/a\x7f", "", ErrInvalidPath},
		{"/.info/connected", "", ErrInvalidPath},
		{"/a/.priority", "", ErrInvalidPath}, // only writes address priorities
		{strings.Repeat("/s", 32), strings.Repeat("s/", 31) + "s", nil},
		{strings.Repeat("/s", 33), "", ErrPathTooDeep},
		{"/" + strings.Repeat("a", 768), strings.Repeat("a", 768), nil},
		// Keys count, separators do not (emulator 4.11.2, golden/limits.json).
		{"/" + strings.Repeat("a", 384) + "/" + strings.Repeat("b", 384), strings.Repeat("a", 384) + "/" + strings.Repeat("b", 384), nil},
		{"/" + strings.Repeat("a", 769), "", ErrPathTooLong},
		{"/" + strings.Repeat("م", 385), "", ErrPathTooLong}, // 770 bytes of UTF-8
	}
	for _, c := range cases {
		p, err := ParsePath(c.in)
		if !errors.Is(err, c.err) {
			t.Errorf("ParsePath(%q): err %v, want %v", c.in, err, c.err)
			continue
		}
		if err == nil && p.String() != c.want {
			t.Errorf("ParsePath(%q) = %q, want %q", c.in, p, c.want)
		}
	}
}

func TestParseWritePath(t *testing.T) {
	for in, want := range map[string]string{
		"/a/.priority":  "a/.priority",
		"a/b/.priority": "a/b/.priority",
		"/.priority":    ".priority",
		"/a/b":          "a/b",
	} {
		p, err := ParseWritePath(in)
		if err != nil || p.String() != want {
			t.Errorf("ParseWritePath(%q) = %q, %v; want %q", in, p, err, want)
		}
	}
	for _, in := range []string{"/a/.priority/b", "/.priority/x", "/a/.value"} {
		if _, err := ParseWritePath(in); err == nil {
			t.Errorf("ParseWritePath(%q) should fail", in)
		}
	}
	p, _ := ParseWritePath("/a/.priority")
	if !p.IsPriority() || p.Parent().String() != "a" {
		t.Errorf("priority path %q", p)
	}
}

func TestPathMethods(t *testing.T) {
	a, _ := ParsePath("a/b")
	c := a.Child("c")
	if c.String() != "a/b/c" || a.String() != "a/b" {
		t.Error("Child must not modify its receiver")
	}
	if !a.Contains(c) || c.Contains(a) || !a.Contains(a) || !(Path{}).Contains(a) {
		t.Error("Contains")
	}
	if a.Rel(c).String() != "c" || c.Parent().String() != "a/b" || c.Last() != "c" {
		t.Error("Rel, Parent or Last")
	}
	// Appending to a parent must not overwrite its child.
	d := c.Parent().Child("d")
	if c.String() != "a/b/c" || d.String() != "a/b/d" {
		t.Errorf("Parent shares memory: %q %q", c, d)
	}
	if (Path{}).Parent().String() != "" || (Path{}).Last() != "" || !(Path{}).IsRoot() {
		t.Error("root")
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"a", "A-b_c", "0", "-1", " ", "مرحبا", "😀", "a:b", "a@b", "%2E"} {
		if !ValidKey(k) {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []string{"", ".", "a.b", "#", "$x", "[", "]", "a/b", "\x00", "\x1f", "\x7f", ".priority"} {
		if ValidKey(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}
