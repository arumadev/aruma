package media

import (
	"io"
	"net/http"
	"reflect"
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
	if n := reflect.TypeFor[Item]().NumField(); n != 13 {
		t.Errorf("Item has %d fields; the contract has 13", n)
	}
}
