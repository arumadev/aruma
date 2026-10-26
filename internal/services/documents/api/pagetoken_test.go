package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// FuzzPageToken: any token a client sends, forged ones included, is refused with a page token
// error or resumes at a document directly in the listing's collection under its parent, whose
// path the store can take apart (M2 of the F1 review: After with no collection id panicked).
func FuzzPageToken(f *testing.F) {
	parent, err := wire.ParseParentName("projects/p/databases/(default)/documents/c/d")
	if err != nil {
		f.Fatal(err)
	}
	listing := listingDigest(parent.String(), "s", "", false)
	for _, after := range [][]string{nil, {}, {"a"}, {"c", "d"}, {"c", "d", "s", "x"}, {"c", "d", "s", "x", "y", "z"},
		{"c", "d", "t", "x"}, {"c", "d", "s", "__x__"}, {"c", "d", "s", ""}, {"c", "d", "s", "a/b"}} {
		b, _ := json.Marshal(pageToken{Listing: listing, After: after})
		f.Add(base64.RawURLEncoding.EncodeToString(b), "s")
		f.Add(base64.RawURLEncoding.EncodeToString(b), "")
	}
	f.Add("abc", "s")
	f.Fuzz(func(t *testing.T, token, collectionID string) {
		tok, err := decodeToken(token)
		if err != nil {
			if !errors.Is(err, errGarbageToken) {
				t.Fatalf("decodeToken(%q): %v", token, err)
			}
			return
		}
		after, err := tokenAfter(tok.After, parent, collectionID)
		if err != nil {
			if !errors.Is(err, errForeignToken) {
				t.Fatalf("tokenAfter(%q): %v", tok.After, err)
			}
			return
		}
		if !after.IsDocument() || len(after) != len(parent.Path)+2 || !slices.Equal(after.Parent(), parent.Path) ||
			collectionID != "" && after.CollectionID() != collectionID {
			t.Fatalf("tokenAfter(%q) = %q, not a document of the listing", tok.After, after)
		}
	})
}
