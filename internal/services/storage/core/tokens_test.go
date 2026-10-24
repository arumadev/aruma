package core

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
)

// create_token and delete_token, as the emulator answers them (tokens.json) and CT checks them:
// admins only, each a new metageneration, and deleting the last token makes another (E §5).
func TestCreateAndDeleteToken(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	first := newDownloadToken()
	f.put(t, "t.txt", seed{body: "token test", tokens: []string{first}})

	for _, p := range []Principal{alice, {}} {
		if _, err := f.s.CreateToken(ctx, f.b, "t.txt", p); err != ErrPermissionDenied {
			t.Errorf("create_token by kind %d: %v", p.Kind, err)
		}
		if _, err := f.s.DeleteToken(ctx, f.b, "t.txt", p, first); err != ErrPermissionDenied {
			t.Errorf("delete_token by kind %d: %v", p.Kind, err)
		}
	}
	if len(f.ops) != 0 {
		t.Errorf("an admin-only operation asked the rules: %+v", f.ops)
	}
	o, err := f.s.CreateToken(ctx, f.b, "t.txt", admin)
	if err != nil || len(o.DownloadTokens) != 2 || o.DownloadTokens[0] != first || o.Metageneration != 2 {
		t.Fatalf("create_token: %+v, %v", o, err)
	}
	second := o.DownloadTokens[1]
	o, err = f.s.DeleteToken(ctx, f.b, "t.txt", admin, first)
	if err != nil || !slices.Equal(o.DownloadTokens, []string{second}) || o.Metageneration != 3 {
		t.Fatalf("delete_token: %+v, %v", o, err)
	}
	if _, _, err := f.s.Open(ctx, f.b, "t.txt", Principal{}, first, 0); err != ErrPermissionDenied {
		t.Errorf("the deleted token opens: %v", err)
	}
	if _, r, err := f.s.Open(ctx, f.b, "t.txt", Principal{}, second, 0); err != nil {
		t.Errorf("the remaining token: %v", err)
	} else {
		r.Close()
	}
	// A token the object does not have changes the metageneration alone.
	o, err = f.s.DeleteToken(ctx, f.b, "t.txt", admin, "nope")
	if err != nil || !slices.Equal(o.DownloadTokens, []string{second}) || o.Metageneration != 4 {
		t.Errorf("an unknown token: %+v, %v", o, err)
	}
	// Deleting the last one makes another at once.
	o, err = f.s.DeleteToken(ctx, f.b, "t.txt", admin, second)
	if err != nil || len(o.DownloadTokens) != 1 || o.DownloadTokens[0] == second || len(o.DownloadTokens[0]) != 36 {
		t.Errorf("the last token: %+v, %v", o, err)
	}
	var ie *InvalidError
	if _, err := f.s.DeleteToken(ctx, f.b, "t.txt", admin, ""); !errors.As(err, &ie) {
		t.Errorf("no token: %v", err)
	}
	for _, call := range []func() error{
		func() error { _, err := f.s.CreateToken(ctx, f.b, "none.txt", admin); return err },
		func() error { _, err := f.s.DeleteToken(ctx, f.b, "none.txt", admin, second); return err },
	} {
		if err := call(); err != ErrNotFound {
			t.Errorf("a missing object: %v", err)
		}
	}
	// Tokens count against metadata_bytes: create_token stops at the limit.
	f.s.limits.MetadataBytes = 200
	var err2 error
	for range 10 {
		if _, err2 = f.s.CreateToken(ctx, f.b, "t.txt", admin); err2 != nil {
			break
		}
	}
	if !errors.As(err2, &ie) {
		t.Errorf("tokens past metadata_bytes: %v", err2)
	}
}

func TestMatchToken(t *testing.T) {
	a, b := newDownloadToken(), newDownloadToken()
	for _, c := range []struct {
		tokens []string
		token  string
		want   bool
	}{
		{[]string{a}, a, true},
		{[]string{a, b}, b, true},
		{[]string{a, b}, a, true},
		{[]string{a}, b, false},
		{[]string{a}, a[:35], false},
		{[]string{a}, a + a, false},
		{nil, a, false},
		{[]string{a}, "", false},
	} {
		if got := matchToken(c.tokens, c.token); got != c.want {
			t.Errorf("matchToken(%q, %q) = %v", c.tokens, c.token, got)
		}
	}
}

// GCS shows the tokens under the custom metadata key, /v0 in downloadTokens alone.
func TestTokensOnEachSurface(t *testing.T) {
	o := Object{Metadata: map[string]string{"note": "n"}, DownloadTokens: []string{"t1", "t2"}}
	if got := GCSMetadata(o); !maps.Equal(got, map[string]string{"note": "n", TokensKey: "t1,t2"}) {
		t.Errorf("GCSMetadata = %v", got)
	}
	if _, ok := o.Metadata[TokensKey]; ok {
		t.Error("GCSMetadata changed the object")
	}
	if got := FirebaseTokens(o); got != "t1,t2" {
		t.Errorf("FirebaseTokens = %q", got)
	}
	if got := GCSMetadata(Object{DownloadTokens: []string{"t"}}); !maps.Equal(got, map[string]string{TokensKey: "t"}) {
		t.Errorf("tokens alone: %v", got)
	}
	if GCSMetadata(Object{}) != nil || FirebaseTokens(Object{}) != "" {
		t.Error("an object without metadata or tokens")
	}
}
