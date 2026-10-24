package core

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestApplyPatch(t *testing.T) {
	base := Object{Name: "a", ContentType: "text/plain", CacheControl: "no-cache", ContentLanguage: "en",
		Metadata: map[string]string{"a": "1", "b": "2"}, DownloadTokens: []string{"t"}}
	for _, c := range []struct {
		name   string
		p      Patch
		check  func(Object) bool
		wantMD map[string]string
	}{
		{"empty", Patch{}, func(o Object) bool {
			return o.ContentType == "text/plain" && slices.Equal(o.DownloadTokens, []string{"t"})
		},
			map[string]string{"a": "1", "b": "2"}},
		{"set and delete fields", Patch{Fields: map[string]*string{FieldContentType: ptr("image/png"), FieldCacheControl: nil, FieldContentEncoding: ptr("gzip")}},
			func(o Object) bool {
				return o.ContentType == "image/png" && o.CacheControl == "" && o.ContentEncoding == "gzip" && o.ContentLanguage == "en"
			}, map[string]string{"a": "1", "b": "2"}},
		{"merge", Patch{Metadata: map[string]*string{"b": nil, "c": ptr("3"), "a": ptr("x")}}, nil, map[string]string{"a": "x", "c": "3"}},
		{"delete a key that is not there", Patch{Metadata: map[string]*string{"z": nil}}, nil, map[string]string{"a": "1", "b": "2"}},
		{"clear", Patch{ClearMetadata: true}, func(o Object) bool { return len(o.DownloadTokens) == 1 }, map[string]string{}},
		{"clear then merge", Patch{ClearMetadata: true, Metadata: map[string]*string{"c": ptr("3"), "a": nil}}, nil, map[string]string{"c": "3"}},
		{"tokens", Patch{Tokens: &[]string{"u", "v"}}, func(o Object) bool { return slices.Equal(o.DownloadTokens, []string{"u", "v"}) },
			map[string]string{"a": "1", "b": "2"}},
		{"no tokens", Patch{Tokens: &[]string{}}, func(o Object) bool { return len(o.DownloadTokens) == 0 }, map[string]string{"a": "1", "b": "2"}},
	} {
		got := applyPatch(base, c.p)
		if !maps.Equal(got.Metadata, c.wantMD) || (c.check != nil && !c.check(got)) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if !maps.Equal(base.Metadata, map[string]string{"a": "1", "b": "2"}) || base.ContentType != "text/plain" {
		t.Errorf("applyPatch changed its object: %+v", base)
	}
}

// The token key never reaches the rules or a row from /v0, and is how GCS sets the tokens.
func TestSurfacePatch(t *testing.T) {
	v := "planted"
	in := Patch{Metadata: map[string]*string{TokensKey: &v, "k": &v}}
	for _, sf := range []Surface{SurfaceFirebase, SurfaceConsole} {
		p, err := surfacePatch(in, sf)
		if _, ok := p.Metadata[TokensKey]; err != nil || ok || p.Metadata["k"] == nil || p.Tokens != nil {
			t.Errorf("%v: %+v, %v", sf, p, err)
		}
		if _, err := surfacePatch(Patch{Tokens: &[]string{"x"}}, sf); err != ErrInvalidArgument {
			t.Errorf("%v with Tokens: %v", sf, err)
		}
	}
	if in.Metadata[TokensKey] == nil {
		t.Fatal("surfacePatch changed its caller's map")
	}
	p, err := surfacePatch(in, SurfaceGCS)
	if _, ok := p.Metadata[TokensKey]; err != nil || ok || p.Tokens == nil || !slices.Equal(*p.Tokens, []string{"planted"}) {
		t.Errorf("GCS: %+v, %v", p, err)
	}
	p, _ = surfacePatch(Patch{Metadata: map[string]*string{TokensKey: nil}}, SurfaceGCS)
	if p.Tokens == nil || len(*p.Tokens) != 0 {
		t.Errorf("GCS, the key deleted: %+v", p)
	}
	p, _ = surfacePatch(Patch{ClearMetadata: true}, SurfaceGCS)
	if p.Tokens == nil || len(*p.Tokens) != 0 {
		t.Errorf("GCS, metadata null: %+v", p)
	}
	p, _ = surfacePatch(Patch{ClearMetadata: true}, SurfaceFirebase)
	if p.Tokens != nil {
		t.Errorf("/v0, metadata null: %+v", p)
	}
	p, _ = surfacePatch(Patch{Metadata: map[string]*string{"k": nil}}, SurfaceGCS)
	if p.Tokens != nil {
		t.Errorf("GCS, another key: %+v", p)
	}
}

func TestSplitTokens(t *testing.T) {
	for in, want := range map[string][]string{
		"a":          {"a"},
		"a,b":        {"a", "b"},
		" a , b ,, ": {"a", "b"},
		"":           {},
		",":          {},
	} {
		if got := splitTokens(&in); !slices.Equal(got, want) {
			t.Errorf("splitTokens(%q) = %q", in, got)
		}
	}
	if got := splitTokens(nil); got == nil || len(got) != 0 {
		t.Errorf("nil: %v", got)
	}
}

func TestCheckPatch(t *testing.T) {
	s := &Service{limits: DefaultLimits()}
	ok := []Patch{
		{},
		{Fields: map[string]*string{FieldContentType: ptr("text/html; charset=utf-8"), FieldCacheControl: nil}},
		{Fields: map[string]*string{FieldContentDisposition: ptr("attachment; filename*=utf-8''na%C3%AFve.txt")}},
		{Fields: map[string]*string{FieldContentLanguage: ptr("ü")}},
		{Metadata: map[string]*string{"k": ptr("multi\nline value"), "ü": ptr("é"), "gone": nil}},
	}
	for _, p := range ok {
		if err := s.checkPatch(p); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	bad := []Patch{
		{Fields: map[string]*string{"md5Hash": ptr("x")}},
		{Fields: map[string]*string{"ContentType": ptr("x")}},
		{Fields: map[string]*string{FieldContentType: ptr("a\rb")}},
		{Fields: map[string]*string{FieldContentType: ptr("a\tb")}},
		{Fields: map[string]*string{FieldContentEncoding: ptr("\xff")}},
		{Fields: map[string]*string{FieldCacheControl: ptr(strings.Repeat("a", 1025))}},
		{Metadata: map[string]*string{"": ptr("x")}},
		{Metadata: map[string]*string{"a\x00": ptr("x")}},
		{Metadata: map[string]*string{"k": ptr("\x00")}},
		{Metadata: map[string]*string{"k": ptr("\xff")}},
	}
	for _, p := range bad {
		var ie *InvalidError
		if err := s.checkPatch(p); !errors.As(err, &ie) {
			t.Errorf("%+v: %v, want 400", p, err)
		}
	}
}

func TestMetadataSize(t *testing.T) {
	s := &Service{limits: DefaultLimits()}
	o := Object{Metadata: map[string]string{"ab": "cde"}}
	if n := metadataSize(o); n != 5 {
		t.Errorf("metadataSize = %d", n)
	}
	o.DownloadTokens = []string{"t1", "t2"}
	if n := metadataSize(o); n != 5+len(TokensKey)+5 {
		t.Errorf("with tokens: %d", n)
	}
	if err := s.checkObjectMetadata(Object{Metadata: map[string]string{TokensKey: "x"}}); err == nil {
		t.Error("the tokens' key in custom metadata is accepted")
	}
	if err := s.checkObjectMetadata(Object{Metadata: map[string]string{"k": strings.Repeat("v", 8<<10-1)}}); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if err := s.checkObjectMetadata(Object{Metadata: map[string]string{"k": strings.Repeat("v", 8<<10)}}); err == nil {
		t.Error("over the limit is accepted")
	}
}
