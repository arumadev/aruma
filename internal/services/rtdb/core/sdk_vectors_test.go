package core

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The vectors under testdata/sdk were computed by the JS SDK's own code, @firebase/database
// 1.1.5 (firebase-js-sdk d7bb0e16f2), and by the emulator 4.11.2; testdata/sdk/README.md says
// how. Each file lists the SDK source lines it exercises.

func loadVectors(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/sdk/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// parse reads an SDK input value, given as its JSON text.
func parse(t *testing.T, raw json.RawMessage) *Node {
	t.Helper()
	n, err := ParseData(raw, nil, 10<<20)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return n
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestHashMatchesSDK(t *testing.T) {
	var f struct {
		Vectors []struct {
			Name      string          `json:"name"`
			JSON      json.RawMessage `json:"json"`
			JSONText  string          `json:"jsonText"`
			Hash      string          `json:"hash"`
			Canonical string          `json:"canonical"`
		} `json:"vectors"`
		IEEE754 []struct {
			SDK   string `json:"sdk"`
			True  string `json:"ieee754"`
			Equal bool   `json:"equal"`
		} `json:"ieee754"`
	}
	loadVectors(t, "hash.json", &f)

	// The SDK's doubleToIEEE754String is wrong for some numbers; the server hashes the true
	// bits as the emulator does (hash.go). Mapping each true hex to the SDK's must turn the
	// server's canonical text into the SDK's; only then may the hashes differ.
	toSDK := map[string]string{}
	for _, v := range f.IEEE754 {
		if !v.Equal {
			toSDK[v.True] = v.SDK
		}
	}
	if len(toSDK) == 0 {
		t.Fatal("hash.json lists no IEEE-754 mismatches; the vectors changed")
	}
	number := regexp.MustCompile(`number:[0-9a-f]{16}`)
	checked, corrected := 0, 0
	for _, v := range f.Vectors {
		raw := v.JSON
		if v.JSONText != "" {
			raw = json.RawMessage(v.JSONText)
		}
		if strings.Contains(v.Canonical, "8000000000000000") {
			// -0: JSON.stringify sends 0, so no SDK ever writes it; the server stores 0.
			continue
		}
		n := parse(t, raw)
		got := hashText(n)
		mapped := number.ReplaceAllStringFunc(got, func(s string) string {
			if sdk, ok := toSDK[s[len("number:"):]]; ok {
				return "number:" + sdk
			}
			return s
		})
		switch {
		case mapped != v.Canonical:
			t.Errorf("%s: canonical\n got  %q\n want %q", v.Name, got, v.Canonical)
		case got != v.Canonical:
			corrected++ // the true bits, where the SDK's are wrong
		default:
			if h := Hash(n); h != v.Hash {
				t.Errorf("%s: hash %q, want %q", v.Name, h, v.Hash)
			}
		}
		checked++
	}
	if checked < 60 {
		t.Errorf("only %d hash vectors checked", checked)
	}
	t.Logf("%d hash vectors, %d with the true IEEE-754 bits in place of the SDK's", checked, corrected)
}

func TestIEEE754(t *testing.T) {
	var f struct {
		IEEE754 []struct {
			Value json.Number `json:"value"`
			True  string      `json:"ieee754"`
		} `json:"ieee754"`
	}
	loadVectors(t, "hash.json", &f)
	for _, v := range f.IEEE754 {
		x, err := v.Value.Float64()
		if err != nil {
			t.Fatal(err)
		}
		if got := ieee754Hex(x); got != v.True {
			t.Errorf("%s: %s, want %s", v.Value, got, v.True)
		}
	}
}

func TestHashMatchesEmulatorETags(t *testing.T) {
	var f struct {
		Vectors []struct {
			Name string          `json:"name"`
			JSON json.RawMessage `json:"json"`
			ETag string          `json:"etag"`
		} `json:"vectors"`
	}
	loadVectors(t, "emulator_etags.json", &f)
	for _, v := range f.Vectors {
		if got := Hash(parse(t, v.JSON)); got != v.ETag {
			t.Errorf("%s: hash %q, emulator ETag %q", v.Name, got, v.ETag)
		}
	}
}

func TestKeyOrderMatchesSDK(t *testing.T) {
	var f struct {
		Keys       []string `json:"keys"`
		SortedKeys []string `json:"sortedKeys"`
		KeyPairs   []struct {
			A, B string
			Cmp  int
		} `json:"keyPairs"`
	}
	loadVectors(t, "order.json", &f)
	n := map[string]*Node{}
	for _, k := range f.Keys {
		n[k] = Bool(true)
	}
	got := Object(n).Keys()
	if len(got) != len(f.SortedKeys) {
		t.Fatalf("%d keys sorted, want %d", len(got), len(f.SortedKeys))
	}
	for i := range got {
		// Where the SDK ties ("00" and "-0", the same number at the same length) its order is
		// the input's; the server breaks the tie by text (order.go).
		ai, _ := keyInt(got[i])
		bi, _ := keyInt(f.SortedKeys[i])
		if got[i] != f.SortedKeys[i] && !(ai == bi && len(got[i]) == len(f.SortedKeys[i])) {
			t.Errorf("sorted keys\n got  %q\n want %q", got, f.SortedKeys)
			break
		}
	}
	for _, p := range f.KeyPairs {
		if !ValidKey(p.A) || !ValidKey(p.B) {
			continue // the SDK's [MIN_NAME] and [MAX_NAME] sentinels never reach a server
		}
		if p.Cmp == 0 && p.A != p.B {
			continue // an SDK tie between different keys, which the server breaks by text
		}
		if got := sign(CompareKeys(p.A, p.B)); got != p.Cmp {
			t.Errorf("CompareKeys(%q, %q) = %d, want %d", p.A, p.B, got, p.Cmp)
		}
	}
	if len(f.KeyPairs) < 40 {
		t.Errorf("only %d key pairs", len(f.KeyPairs))
	}
}

type namedPair struct {
	Index string          `json:"index"`
	AN    string          `json:"an"`
	A     json.RawMessage `json:"a"`
	BN    string          `json:"bn"`
	B     json.RawMessage `json:"b"`
	Cmp   int             `json:"cmp"`
}

func TestValueOrderMatchesSDK(t *testing.T) {
	var f struct {
		ValuePairs    []namedPair `json:"valuePairs"`
		PriorityPairs []namedPair `json:"priorityPairs"`
		ChildPairs    []namedPair `json:"childPairs"`
	}
	loadVectors(t, "order.json", &f)
	check := func(kind string, ix Index, pairs []namedPair) {
		for _, p := range pairs {
			an, bn := p.AN, p.BN
			if an == "" {
				an, bn = "k", "k"
			}
			got := sign(ix.Compare(an, parse(t, p.A), bn, parse(t, p.B)))
			if got != p.Cmp {
				t.Errorf("%s %s: %s:%s vs %s:%s = %d, want %d", kind, p.Index, an, p.A, bn, p.B, got, p.Cmp)
			}
		}
		if len(pairs) < 20 {
			t.Errorf("only %d %s pairs", len(pairs), kind)
		}
	}
	check("value", ByValue, f.ValuePairs)
	check("priority", ByPriority, f.PriorityPairs)
	byIndex := map[string][]namedPair{}
	for _, p := range f.ChildPairs {
		byIndex[p.Index] = append(byIndex[p.Index], p)
	}
	for index, pairs := range byIndex {
		p, err := ParsePath(index)
		if err != nil {
			t.Fatal(err)
		}
		for _, pair := range pairs {
			got := sign(ByChild(p).Compare(pair.AN, parse(t, pair.A), pair.BN, parse(t, pair.B)))
			if got != pair.Cmp {
				t.Errorf("child %s: %s:%s vs %s:%s = %d, want %d", index, pair.AN, pair.A, pair.BN, pair.B, got, pair.Cmp)
			}
		}
	}
}

func TestExportMatchesSDK(t *testing.T) {
	var f struct {
		Vectors []struct {
			JSON   json.RawMessage `json:"json"`
			Val    json.RawMessage `json:"val"`
			Export json.RawMessage `json:"export"`
		} `json:"vectors"`
	}
	loadVectors(t, "export.json", &f)
	same := func(a, b []byte) bool {
		var x, y any
		if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
			return false
		}
		xa, _ := json.Marshal(x)
		ya, _ := json.Marshal(y)
		return string(xa) == string(ya)
	}
	for _, v := range f.Vectors {
		n := parse(t, v.JSON)
		if got := n.JSON(); !same(got, v.Val) {
			t.Errorf("%s: val %s, want %s", v.JSON, got, v.Val)
		}
		if got := n.ExportJSON(); !same(got, v.Export) {
			t.Errorf("%s: export %s, want %s", v.JSON, got, v.Export)
		}
		// The export format reads back to the same node.
		if back := parse(t, n.ExportJSON()); !Equal(back, n) {
			t.Errorf("%s: export does not round-trip", v.JSON)
		}
	}
}
