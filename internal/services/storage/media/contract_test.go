package media

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// The contract's signature and modes (S2-0). S2-F tests the serving.
var _ func(http.ResponseWriter, *http.Request, Item, io.ReadSeeker, Mode, Limits) error = Serve

func TestModesAreDistinct(t *testing.T) {
	seen := map[Mode]bool{}
	for _, m := range []Mode{RulesDecided, Token, Console, GCS} {
		if m == 0 || seen[m] {
			t.Errorf("mode %d", m)
		}
		seen[m] = true
	}
	var names []string
	typ := reflect.TypeFor[Item]()
	for i := range typ.NumField() {
		names = append(names, typ.Field(i).Name+" "+typ.Field(i).Type.String())
	}
	want := "Name string; ContentType string; ContentEncoding string; ContentDisposition string; ContentLanguage string; " +
		"CacheControl string; Size int64; ETag string; Updated time.Time; Generation int64; Metageneration int64; CRC32C uint32; MD5 [16]uint8"
	if got := strings.Join(names, "; "); got != want {
		t.Errorf("Item:\n got %s\nwant %s", got, want)
	}
	lt := reflect.TypeFor[Limits]()
	if got := fmt.Sprintf("%d %s %s %s %s", lt.NumField(), lt.Field(0).Name, lt.Field(1).Name, lt.Field(2).Name, lt.Field(2).Type); got != "3 DecompressedBytes MinBytesPerSecond Cut func(error)" {
		t.Errorf("Limits: %s", got)
	}
}
