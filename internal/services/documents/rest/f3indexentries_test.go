package rest_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

// TestIndexEntryLimitsReplay replays f3-index-entries.json: its requests are described, not
// stored (up to a megabyte each), so each is built again as the recorder built it
// (production/record-f3.mjs, indexEntries), sent in the recorded order, and its answer compared
// with production's: the 40,000 entries of a document (fields, array elements, map subfields),
// the entry size against the name's length, and the commit size a document of long-named entries
// meets, on its write and on its delete.
func TestIndexEntryLimitsReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", "f3-index-entries.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			ID       string                 `json:"id"`
			Request  struct{ Shape string } `json:"request"`
			Response struct {
				HTTP int             `json:"http"`
				Body json.RawMessage `json:"body"`
			} `json:"response"`
		} `json:"cases"`
		Summary []struct {
			Kind        string `json:"kind"`
			MaxAccepted int    `json:"maxAccepted"`
			NameBytes   int    `json:"nameBytes"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	total := map[string]int{}
	for _, s := range f.Summary {
		total[s.Kind] = s.MaxAccepted
		if s.Kind == "total" {
			total["nameBytes"] = s.NameBytes
		}
	}

	const ix = "aruma-ref-f3-ix"
	// nameOf is a document name of exactly l bytes below documents/.
	nameOf := func(l int, tag string) string {
		sum := l - (len(ix) + 5 + 8)
		ids := make([]string, 5)
		for i := range ids {
			n := sum / 5
			if i < sum%5 {
				n++
			}
			ids[i] = strings.Repeat("n", n)
			if i == 0 {
				ids[i] = tag + "-" + strings.Repeat("n", n-len(tag)-1)
			}
		}
		return ix + "/" + strings.Join(ids, "/c/")
	}
	integer := func(i int) string { return `{"integerValue":"` + strconv.Itoa(i) + `"}` }
	scalars := func(n int) string {
		var sb strings.Builder
		sb.WriteByte('{')
		for i := range n {
			if i > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, `"f%05d":%s`, i, integer(i))
		}
		sb.WriteByte('}')
		return sb.String()
	}
	array := func(n int) string {
		var sb strings.Builder
		sb.WriteString(`{"a":{"arrayValue":{"values":[`)
		for i := range n {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(integer(i))
		}
		sb.WriteString(`]}}}`)
		return sb.String()
	}
	str := func(n int) string { return `{"stringValue":"` + strings.Repeat("s", n) + `"}` }
	entryFields := map[string]string{
		"entry_no_fields":       `{}`,
		"entry_int":             `{"f":` + integer(1) + `}`,
		"entry_string_1500":     `{"f":` + str(1500) + `}`,
		"entry_string_3000":     `{"f":` + str(3000) + `}`,
		"entry_long_field_name": `{"` + strings.Repeat("x", 1500) + `":` + integer(1) + `}`,
	}
	update := func(name, fields string) string {
		return `{"writes":[{"update":{"name":"` + root + "/" + name + `","fields":` + fields + `}}]}`
	}

	idRE := regexp.MustCompile(`^(\w+)\.(?:n|name)_(\d+)$`)
	compared := 0
	for _, c := range f.Cases {
		var body string
		if c.ID == "total.delete" {
			body = `{"writes":[{"delete":"` + root + "/" + nameOf(total["nameBytes"], fmt.Sprintf("total%d", total["total"])) + `"}]}`
		} else {
			m := idRE.FindStringSubmatch(c.ID)
			if m == nil {
				t.Fatalf("%s: an unknown case", c.ID)
			}
			kind := m[1]
			n, _ := strconv.Atoi(m[2])
			switch kind {
			case "fields":
				body = update(fmt.Sprintf("%s/fields-%d", ix, n), scalars(n))
			case "array":
				body = update(fmt.Sprintf("%s/array-%d", ix, n), array(n))
			case "map":
				body = update(fmt.Sprintf("%s/map-%d", ix, n), `{"m":{"mapValue":{"fields":`+scalars(n)+`}}}`)
			case "total":
				body = update(nameOf(total["nameBytes"], fmt.Sprintf("total%d", n)), scalars(n))
			default:
				fields, ok := entryFields[kind]
				if !ok {
					t.Fatalf("%s: an unknown kind", c.ID)
				}
				body = update(nameOf(n, kind), fields)
			}
		}
		code, _, got := h.do(t, http.MethodPost, "/v1/projects/"+apitest.Project+"/databases/(default)/documents:commit", body, tok)
		compared++
		if code != c.Response.HTTP || !sameJSON(t, normalise(t, got), normalise(t, c.Response.Body)) {
			t.Errorf("%s (%s): %d %s\nwant %d %s", c.ID, c.Request.Shape, code, got, c.Response.HTTP, c.Response.Body)
		}
	}
	if compared != len(f.Cases) || compared < 100 {
		t.Errorf("compared %d of %d cases", compared, len(f.Cases))
	}
}
