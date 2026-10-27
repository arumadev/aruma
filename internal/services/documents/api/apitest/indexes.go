package apitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The 105 query shapes of test/oracle/documents/corpus/indexes-105.json, which both transports
// replay through RunQuery and RunAggregationQuery: each ran on a never-written collection and on
// the populated aruma-ref-idx, whose documents record-indexes.mjs wrote and IndexSeed repeats.

// IndexCase is one recorded shape.
type IndexCase struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // query, aggregation, collection-group, collection-group-aggregation
	Description string `json:"description"`
	Request     struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	} `json:"request"`
	IndexOutcome
	Empty IndexOutcome `json:"emptyCollection"`
}

// IndexOutcome is a shape's answer on one collection: its verdict, ok or the error's status,
// with the message, or the documents' last three name segments in order, or the aggregates.
type IndexOutcome struct {
	HTTP       int               `json:"http"`
	Verdict    string            `json:"verdict"`
	Message    string            `json:"message"`
	IDs        []string          `json:"ids"`
	Aggregates []json.RawMessage `json:"aggregates"`
}

// Collections the recorder queried, populated and never written.
const (
	IndexCollection  = "aruma-ref-idx"
	IndexGroup       = "aruma-ref-idxcg"
	emptyCollection  = "aruma-ref-idx-empty"
	emptyGroup       = "aruma-ref-idx-emptycg"
	indexRootPattern = "{root}"
)

// IndexCases are the recorded shapes.
func IndexCases(t testing.TB) []IndexCase {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "test", "oracle", "documents", "corpus", "indexes-105.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []IndexCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) != 105 {
		t.Fatalf("%d recorded shapes, want 105", len(f.Cases))
	}
	return f.Cases
}

// Body is the case's request body on the populated collection, or on the never-written one,
// whose cursor references point into it as the recorder's did; root is the documents root.
func (c *IndexCase) Body(root string, empty bool) string {
	s := string(c.Request.Body)
	if empty {
		s = strings.ReplaceAll(s, `"`+IndexGroup+`"`, `"`+emptyGroup+`"`)
		s = strings.ReplaceAll(s, `"`+IndexCollection+`"`, `"`+emptyCollection+`"`)
		s = strings.ReplaceAll(s, indexRootPattern+"/"+IndexCollection+"/", indexRootPattern+"/"+emptyCollection+"/")
	}
	return strings.ReplaceAll(s, indexRootPattern, root)
}

// Want is the recorded outcome on the collection.
func (c *IndexCase) Want(empty bool) IndexOutcome {
	if empty {
		return c.Empty
	}
	return c.IndexOutcome
}

// IndexDoc is a document record-indexes.mjs wrote before the populated run.
type IndexDoc struct {
	Path   string // under the documents root
	Fields string // the REST JSON of its fields
}

// IndexSeed are the documents of the populated run: d1..d6, d1..d4 again under d1, d5 as e1
// under d2.
func IndexSeed() []IndexDoc {
	data := []string{
		`{"a":{"integerValue":"1"},"b":{"integerValue":"1"},"c":{"stringValue":"x"},"d":{"integerValue":"10"},"tags":{"arrayValue":{"values":[{"stringValue":"red"},{"stringValue":"blue"}]}},"m":{"mapValue":{"fields":{"x":{"integerValue":"1"},"y":{"stringValue":"p"}}}}}`,
		`{"a":{"integerValue":"1"},"b":{"integerValue":"2"},"c":{"stringValue":"y"},"d":{"integerValue":"20"},"tags":{"arrayValue":{"values":[{"stringValue":"red"}]}},"m":{"mapValue":{"fields":{"x":{"integerValue":"2"},"y":{"stringValue":"q"}}}}}`,
		`{"a":{"integerValue":"2"},"b":{"integerValue":"1"},"c":{"stringValue":"x"},"d":{"integerValue":"30"},"tags":{"arrayValue":{"values":[{"stringValue":"green"}]}},"m":{"mapValue":{"fields":{"x":{"integerValue":"1"},"y":{"stringValue":"q"}}}}}`,
		`{"a":{"integerValue":"2"},"b":{"integerValue":"2"},"c":{"stringValue":"z"},"d":{"integerValue":"40"},"tags":{"arrayValue":{}},"m":{"mapValue":{"fields":{"x":{"integerValue":"3"}}}}}`,
		`{"a":{"integerValue":"3"},"b":{"integerValue":"3"},"c":{"stringValue":"y"},"d":{"integerValue":"50"},"tags":{"arrayValue":{"values":[{"stringValue":"blue"},{"stringValue":"green"}]}},"m":{"mapValue":{"fields":{"x":{"integerValue":"2"},"y":{"stringValue":"p"}}}}}`,
		`{"a":{"integerValue":"1"},"b":{"integerValue":"1"},"c":{"stringValue":"z"},"d":{"integerValue":"60"}}`,
	}
	var docs []IndexDoc
	for i, f := range data {
		docs = append(docs, IndexDoc{IndexCollection + "/d" + string(rune('1'+i)), f})
	}
	for i, f := range data[:4] {
		docs = append(docs, IndexDoc{IndexCollection + "/d1/" + IndexGroup + "/d" + string(rune('1'+i)), f})
	}
	return append(docs, IndexDoc{IndexCollection + "/d2/" + IndexGroup + "/e1", data[4]})
}

// LastSegments are a document name's last three segments, as the recorder kept them.
func LastSegments(name string) string {
	s := strings.Split(name, "/")
	return strings.Join(s[max(0, len(s)-3):], "/")
}
