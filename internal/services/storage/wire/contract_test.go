package wire

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The tests of the contract commit (S2-0): the types' JSON shapes and the helpers that work from
// the contract. S2-B tests the formats and parsers.

func TestSetKeepsGoogleSpelling(t *testing.T) {
	h := http.Header{}
	Set(h, UploadURL, "https://aruma.test/v0/b/x/o?upload_id=1")
	if _, ok := h["X-Goog-Upload-URL"]; !ok {
		t.Errorf("the header is not set as spelled: %v", h)
	}
	if _, ok := h["X-Goog-Upload-Url"]; ok {
		t.Error("the header is set under its canonical form too")
	}
	Set(h, UploadURL, "b")
	if len(h[UploadURL]) != 1 || h[UploadURL][0] != "b" {
		t.Errorf("Set does not replace: %v", h[UploadURL])
	}
}

// The /v0 types write no null and drop what is unset; the lists a client iterates stay present.
func TestFirebaseShapes(t *testing.T) {
	b, _ := json.Marshal(FirebaseObject{Name: "a b.png", Bucket: "chat-app.aruma.test"})
	if string(b) != `{"name":"a b.png","bucket":"chat-app.aruma.test"}` {
		t.Errorf("FirebaseObject = %s", b)
	}
	b, _ = json.Marshal(FirebaseList{Prefixes: []string{}, Items: []FirebaseListItem{}})
	if string(b) != `{"prefixes":[],"items":[]}` {
		t.Errorf("an empty last page = %s", b)
	}
	b, _ = json.Marshal(FirebaseError{FirebaseErrorBody{404, "Not Found."}})
	if string(b) != `{"error":{"code":404,"message":"Not Found."}}` {
		t.Errorf("FirebaseError = %s", b)
	}
	if strings.Contains(string(b), "null") {
		t.Error("a null")
	}
}

func TestGCSErrorCodeIsANumber(t *testing.T) {
	b, _ := json.Marshal(GCSError{GCSErrorBody{Code: 404, Message: "No such object: b/a.txt",
		Errors: []GCSErrorItem{{"global", "notFound", "No such object: b/a.txt"}}}})
	want := `{"error":{"code":404,"message":"No such object: b/a.txt","errors":[{"domain":"global","reason":"notFound","message":"No such object: b/a.txt"}]}}`
	if string(b) != want {
		t.Errorf("GCSError = %s", b)
	}
}

func TestUploadCommandsHas(t *testing.T) {
	s := CommandUpload | CommandFinalize
	if !s.Has(CommandUpload) || !s.Has(CommandFinalize) || !s.Has(CommandUpload|CommandFinalize) || s.Has(CommandQuery) || s.Has(CommandUpload|CommandCancel) {
		t.Error("Has")
	}
}

func TestContentRangeKinds(t *testing.T) {
	for _, c := range []struct {
		form         string
		r            ContentRange
		query, final bool
	}{
		{"bytes 0-99/*", ContentRange{Start: 0, End: 99, Total: -1}, false, false},
		{"bytes 0-99/100", ContentRange{Start: 0, End: 99, Total: 100}, false, true},
		{"bytes */100", ContentRange{Start: -1, End: -1, Total: 100}, false, true},
		{"bytes 0-*/*", ContentRange{Start: 0, End: -1, Open: true, Total: -1}, false, true},
		{"bytes 5-*/10", ContentRange{Start: 5, End: -1, Open: true, Total: 10}, false, true},
		{"bytes */*", ContentRange{Start: -1, End: -1, Total: -1}, true, false},
	} {
		if c.r.Query() != c.query || c.r.Final() != c.final {
			t.Errorf("%s: Query %v Final %v, want %v %v", c.form, c.r.Query(), c.r.Final(), c.query, c.final)
		}
	}
}

// The formats and parsers of the contract: a change does not compile.
var (
	_ func(time.Time) string                                 = Time
	_ func(int64) string                                     = Int
	_ func(uint32) string                                    = CRC32C
	_ func([16]byte) string                                  = MD5
	_ func(int64, int64) string                              = ETag
	_ func(int64) string                                     = MediaETag
	_ func(uint32, [16]byte) string                          = HashHeader
	_ func(string) (ObjectPath, error)                       = SplitObjectPath
	_ func(string) (UploadCommands, error)                   = ParseUploadCommand
	_ func(string) (ContentRange, error)                     = ParseContentRange
	_ func(http.Header) int64                                = DeclaredLength
	_ func(http.Header) string                               = DeclaredType
	_ func(io.Reader, string, int64) (*RelatedReader, error) = NewRelatedReader
	_ func(string, int64) (Range, bool, error)               = ParseRange
	_ func(http.Header, string, string)                      = Set
)

func TestTypesArePinned(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[ObjectPath](), "Bucket Objects Object Rest"},
		{reflect.TypeFor[ContentRange](), "Start End Open Total"},
		{reflect.TypeFor[RelatedReader](), "JSON MediaType Media"},
		{reflect.TypeFor[Range](), "Start Length"},
	} {
		typ, want := c.typ, c.want
		var names []string
		for i := range typ.NumField() {
			names = append(names, typ.Field(i).Name)
		}
		if got := strings.Join(names, " "); got != want {
			t.Errorf("%v fields %q, want %q", typ, got, want)
		}
	}
}
