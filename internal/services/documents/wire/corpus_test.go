package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The production recordings of test/oracle/documents/corpus judge this package: these tests
// replay them offline.

const corpusDir = "../../../../test/oracle/documents/corpus"

// corpusProject is the project the recordings ran on; recorded paths name it {project}.
const corpusProject = "chatapp-8ae21"

func readCorpus(t *testing.T, name string, into any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusDir, name))
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// compactRE matches the recorder's compact form of a long request string.
var compactRE = regexp.MustCompile(`^<string (\d+) bytes, sha256 ([0-9a-f]{64}), starts "(.*)">$`)

// expand rebuilds a string the recorder compacted, as value's corpus test does: every long
// string it sends repeats its start, or is a name or path whose ids repeat one character, which
// the SHA-256 confirms.
func expand(t *testing.T, s string) string {
	t.Helper()
	m := compactRE.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	n, _ := strconv.Atoi(m[1])
	var start string
	if err := json.Unmarshal([]byte(`"`+m[3]+`"`), &start); err != nil || start == "" {
		t.Fatalf("compacted string %q: %v", s, err)
	}
	ok := func(full string) bool {
		sum := sha256.Sum256([]byte(full))
		return hex.EncodeToString(sum[:]) == m[2]
	}
	for _, period := range []int{1, 4} {
		if period > len(start) {
			break
		}
		if full := strings.Repeat(start[:period], n/period+1)[:n]; ok(full) {
			return full
		}
	}
	if b, err := base64.StdEncoding.DecodeString(start[:4]); err == nil {
		for k := range 3 {
			if full := base64.StdEncoding.EncodeToString(bytes.Repeat(b[:1], n/4*3-k)); ok(full) {
				return full
			}
		}
	}
	if full, found := expandName(start, n, ok); found {
		return full
	}
	t.Fatalf("compacted string %q cannot be rebuilt", s[:min(len(s), 80)])
	return ""
}

// expandName rebuilds a long resource name or path as record-f0.mjs and record-values.mjs
// build them: the recordings keep only its size and hash.
func expandName(_ string, _ int, ok func(string) bool) (string, bool) {
	for _, full := range recordedNames() {
		if ok(full) {
			return full, true
		}
	}
	return "", false
}

// recordedNames are the long names the recorders send, as full resource names and as recorded
// GET paths (whose project is {project}).
func recordedNames() []string {
	const c = "aruma-ref-f0"
	var rels []string
	for _, n := range []int{1500, 1501} { // id_1500_bytes, id_1501_bytes
		rels = append(rels, c+"/"+strings.Repeat("i", n))
	}
	{ // name_over_6kib
		segs := []string{c}
		for _, ch := range "abcde" {
			segs = append(segs, strings.Repeat(string(ch), 1500), "c")
		}
		rels = append(rels, strings.Join(segs[:len(segs)-1], "/"))
	}
	for _, l := range []struct {
		size int
		ch   string
	}{{6127, "e"}, {6127, "f"}, {6128, "f"}, {6128, "e"}, {6144, "e"}, {6145, "e"}} { // name_bytes_*
		segs := []string{c}
		for _, ch := range "abcd" {
			segs = append(segs, strings.Repeat(string(ch), 1400), "c")
		}
		head := strings.Join(segs, "/")
		rels = append(rels, head+"/"+strings.Repeat(l.ch, l.size-len(head)-1))
	}
	for _, n := range []int{99, 100, 101} { // depth_*
		segs := []string{c, "d"}
		for i := 1; i <= n; i++ {
			segs = append(segs, "c"+strconv.Itoa(i), "d")
		}
		rels = append(rels, strings.Join(segs, "/"))
	}
	rels = append(rels, "c/"+strings.Repeat("i", 1501)) // values-validate ref.id_1501
	var out []string
	for _, rel := range rels {
		out = append(out,
			"projects/"+corpusProject+"/databases/(default)/documents/"+rel,
			"projects/{project}/databases/(default)/documents/"+rel)
	}
	return out
}

// expandAll expands every compacted string in a decoded JSON value.
func expandAll(t *testing.T, x any) any {
	t.Helper()
	switch y := x.(type) {
	case string:
		return expand(t, y)
	case map[string]any:
		out := make(map[string]any, len(y))
		for k, v := range y {
			out[expand(t, k)] = expandAll(t, v)
		}
		return out
	case []any:
		out := make([]any, len(y))
		for i, v := range y {
			out[i] = expandAll(t, v)
		}
		return out
	}
	return x
}

// toJSON encodes a decoded JSON value back to bytes.
func toJSON(t *testing.T, x any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(x); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(buf.Bytes())
}

// sameJSON reports whether two JSON texts hold the same value, ignoring member order.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("not JSON: %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("not JSON: %s: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}
