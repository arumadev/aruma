package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// Page tokens are opaque to clients, as production's are; theirs is not reproduced, only how it
// behaves (f1-list-documents.json token.*, f1-list-collection-ids.json token.*):
//
//   - a token that is not one ("abc") is "invalid page token";
//   - a ListDocuments token is bound to its listing: another parent, collection, order_by or
//     show_missing is "Invalid page token.", while another page_size or mask is accepted;
//   - a ListCollectionIds token is the last id alone, and resumes after it under any parent.
//
// A token is base64url of JSON: the listing's digest and where the next page starts.

// pageToken is a token's content.
type pageToken struct {
	// Listing is the SHA-256 of what binds a ListDocuments token; empty on ListCollectionIds.
	Listing []byte `json:"l,omitempty"`
	// After is the last document of the page (its path), or the last collection id.
	After []string `json:"a"`
}

var (
	errGarbageToken = wire.InvalidArgument("invalid page token")
	errForeignToken = wire.InvalidArgument("Invalid page token.")
)

func encodeToken(t pageToken) string {
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (pageToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageToken{}, errGarbageToken
	}
	var t pageToken
	if err := json.Unmarshal(b, &t); err != nil || t.After == nil {
		return pageToken{}, errGarbageToken
	}
	return t, nil
}

// listingDigest binds a ListDocuments token to its listing.
func listingDigest(parent, collectionID, order string, showMissing bool) []byte {
	h := sha256.New()
	missing := "0"
	if showMissing {
		missing = "1"
	}
	for _, s := range []string{parent, collectionID, order, missing} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}
