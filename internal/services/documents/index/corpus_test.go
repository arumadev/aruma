package index_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

// The production oracle: 105 query shapes recorded on chatapp-8ae21 with no composite index
// and only the default field override, each on a populated and on a never-written collection
// (test/oracle/documents/README.md).
const (
	corpusPath    = "../../../../test/oracle/documents/corpus/indexes-105.json"
	corpusProject = "chatapp-8ae21"
)

type corpusLink struct {
	Param   string    `json:"param"`
	Payload string    `json:"payload"`
	Index   restIndex `json:"index"`
}

// corpusRun is one run of a shape: on the populated collection (the case itself) or on the
// never-written one (emptyCollection).
type corpusRun struct {
	Collection string       `json:"collection"`
	Verdict    string       `json:"verdict"`
	HTTP       int          `json:"http"`
	Status     string       `json:"status"`
	Message    string       `json:"message"`
	Links      []corpusLink `json:"links"`
}

type corpusCase struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Request     struct {
		Body json.RawMessage `json:"body"`
	} `json:"request"`
	corpusRun
	EmptyCollection corpusRun `json:"emptyCollection"`
}

type corpus struct {
	Cases          []corpusCase `json:"cases"`
	Indexes        restIndexes  `json:"indexes"`
	FieldOverrides struct {
		Fields []restField `json:"fields"`
	} `json:"fieldOverrides"`
}

// The Admin API's JSON for indexes and fields, as firebase firestore:indexes and the corpus
// write them.
type restIndexField struct {
	FieldPath   string `json:"fieldPath"`
	Order       string `json:"order"`
	ArrayConfig string `json:"arrayConfig"`
}

type restIndex struct {
	Name       string           `json:"name"`
	QueryScope string           `json:"queryScope"`
	Fields     []restIndexField `json:"fields"`
	State      string           `json:"state"`
}

type restIndexes struct {
	Indexes []restIndex `json:"indexes"`
}

type restField struct {
	Name        string `json:"name"`
	IndexConfig struct {
		Indexes []restIndex `json:"indexes"`
	} `json:"indexConfig"`
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cases) != 105 {
		t.Fatalf("corpus has %d cases, want 105", len(c.Cases))
	}
	return c
}

// corpusCatalog is the recorded project's declared configuration: no composite, and the
// database default override.
func corpusCatalog(t *testing.T, c corpus) *index.Catalog {
	t.Helper()
	var composites []index.Index
	for _, ix := range c.Indexes.Indexes {
		composites = append(composites, decodeIndex(t, ix).Index)
	}
	var overrides []index.FieldOverride
	for _, f := range c.FieldOverrides.Fields {
		group, path := splitName(t, f.Name, "/fields/")
		o := index.FieldOverride{CollectionGroup: group, Path: path, Indexes: []index.SingleFieldIndex{}}
		for _, ix := range f.IndexConfig.Indexes {
			r := decodeIndex(t, ix)
			o.Indexes = append(o.Indexes, index.SingleFieldIndex{Scope: r.Index.Scope, Mode: r.Index.Fields[0].Mode, State: r.Index.State})
		}
		overrides = append(overrides, o)
	}
	cat, err := index.NewCatalog(composites, overrides)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// splitName splits an Admin API name at sep into its collection group and what follows.
func splitName(t *testing.T, name, sep string) (group, rest string) {
	t.Helper()
	_, after, ok := strings.Cut(name, "/collectionGroups/")
	if !ok {
		t.Fatalf("name %q has no collection group", name)
	}
	group, rest, ok = strings.Cut(after, sep)
	if !ok {
		t.Fatalf("name %q has no %q", name, sep)
	}
	return group, rest
}

// decodeIndex reads an Admin API index (or a single-field index of a field, or a decoded
// console link) into the form Check suggests.
func decodeIndex(t *testing.T, ix restIndex) index.Required {
	t.Helper()
	var r index.Required
	if ix.Name != "" {
		if strings.Contains(ix.Name, "/fields/") {
			r.Exemption = true
			r.Index.CollectionGroup, _ = splitName(t, ix.Name, "/fields/")
		} else {
			r.Index.CollectionGroup, _ = splitName(t, ix.Name, "/indexes/")
		}
	}
	switch ix.QueryScope {
	case "COLLECTION":
		r.Index.Scope = index.ScopeCollection
	case "COLLECTION_GROUP":
		r.Index.Scope = index.ScopeCollectionGroup
	default:
		t.Fatalf("query scope %q", ix.QueryScope)
	}
	switch ix.State {
	case "", "READY":
	case "CREATING":
		r.Index.State = index.StateCreating
	default:
		t.Fatalf("state %q", ix.State)
	}
	for _, f := range ix.Fields {
		var mode index.FieldMode
		switch {
		case f.ArrayConfig == "CONTAINS":
			mode = index.Contains
		case f.Order == "ASCENDING":
			mode = index.Ascending
		case f.Order == "DESCENDING":
			mode = index.Descending
		default:
			t.Fatalf("index field %+v", f)
		}
		r.Index.Fields = append(r.Index.Fields, index.IndexField{Path: f.FieldPath, Mode: mode})
	}
	return r
}

// The REST JSON of StructuredQuery and StructuredAggregationQuery, as far as the rule reads it.
type restFieldRef struct {
	FieldPath string `json:"fieldPath"`
}

type restFilter struct {
	FieldFilter *struct {
		Field restFieldRef `json:"field"`
		Op    string       `json:"op"`
	} `json:"fieldFilter"`
	UnaryFilter *struct {
		Field restFieldRef `json:"field"`
		Op    string       `json:"op"`
	} `json:"unaryFilter"`
	CompositeFilter *struct {
		Op      string       `json:"op"`
		Filters []restFilter `json:"filters"`
	} `json:"compositeFilter"`
}

type restQuery struct {
	From []struct {
		CollectionID   string `json:"collectionId"`
		AllDescendants bool   `json:"allDescendants"`
	} `json:"from"`
	Where   *restFilter `json:"where"`
	OrderBy []struct {
		Field     restFieldRef `json:"field"`
		Direction string       `json:"direction"`
	} `json:"orderBy"`
}

type restAggregation struct {
	Count *struct{}                     `json:"count"`
	Sum   *struct{ Field restFieldRef } `json:"sum"`
	Avg   *struct{ Field restFieldRef } `json:"avg"`
}

type restBody struct {
	StructuredQuery            *restQuery `json:"structuredQuery"`
	StructuredAggregationQuery *struct {
		Aggregations    []restAggregation `json:"aggregations"`
		StructuredQuery *restQuery        `json:"structuredQuery"`
	} `json:"structuredAggregationQuery"`
}

var restOperators = map[string]index.Operator{
	"EQUAL":                 index.OpEqual,
	"IN":                    index.OpIn,
	"ARRAY_CONTAINS":        index.OpArrayContains,
	"ARRAY_CONTAINS_ANY":    index.OpArrayContainsAny,
	"IS_NULL":               index.OpIsNull,
	"IS_NAN":                index.OpIsNaN,
	"LESS_THAN":             index.OpLessThan,
	"LESS_THAN_OR_EQUAL":    index.OpLessThanOrEqual,
	"GREATER_THAN":          index.OpGreaterThan,
	"GREATER_THAN_OR_EQUAL": index.OpGreaterThanOrEqual,
	"NOT_EQUAL":             index.OpNotEqual,
	"NOT_IN":                index.OpNotIn,
	"IS_NOT_NULL":           index.OpIsNotNull,
	"IS_NOT_NAN":            index.OpIsNotNaN,
	"AND":                   index.OpAnd,
	"OR":                    index.OpOr,
}

func decodeFilter(f restFilter) (index.Filter, error) {
	op := func(s string) (index.Operator, error) {
		if o, ok := restOperators[s]; ok {
			return o, nil
		}
		return 0, fmt.Errorf("unknown operator %q", s)
	}
	switch {
	case f.FieldFilter != nil:
		o, err := op(f.FieldFilter.Op)
		return index.Where(f.FieldFilter.Field.FieldPath, o), err
	case f.UnaryFilter != nil:
		o, err := op(f.UnaryFilter.Op)
		return index.Where(f.UnaryFilter.Field.FieldPath, o), err
	case f.CompositeFilter != nil:
		o, err := op(f.CompositeFilter.Op)
		if err != nil {
			return index.Filter{}, err
		}
		c := index.Filter{Op: o}
		for _, sub := range f.CompositeFilter.Filters {
			s, err := decodeFilter(sub)
			if err != nil {
				return index.Filter{}, err
			}
			c.Filters = append(c.Filters, s)
		}
		return c, nil
	}
	return index.Filter{}, fmt.Errorf("empty filter")
}

// decodeShape reads a runQuery or runAggregationQuery request body into a QueryShape.
func decodeShape(body []byte) (index.QueryShape, error) {
	var b restBody
	if err := json.Unmarshal(body, &b); err != nil {
		return index.QueryShape{}, err
	}
	var q index.QueryShape
	sq := b.StructuredQuery
	if a := b.StructuredAggregationQuery; a != nil {
		sq = a.StructuredQuery
		for _, agg := range a.Aggregations {
			switch {
			case agg.Count != nil:
				q.Aggregations = append(q.Aggregations, index.Aggregation{Op: index.AggCount})
			case agg.Sum != nil:
				q.Aggregations = append(q.Aggregations, index.Aggregation{Op: index.AggSum, Field: agg.Sum.Field.FieldPath})
			case agg.Avg != nil:
				q.Aggregations = append(q.Aggregations, index.Aggregation{Op: index.AggAvg, Field: agg.Avg.Field.FieldPath})
			default:
				return q, fmt.Errorf("unknown aggregation")
			}
		}
	}
	if sq == nil || len(sq.From) != 1 {
		return q, fmt.Errorf("want a query with one from selector")
	}
	q.CollectionID = sq.From[0].CollectionID
	q.Scope = index.ScopeCollection
	if sq.From[0].AllDescendants {
		q.Scope = index.ScopeCollectionGroup
	}
	if sq.Where != nil {
		f, err := decodeFilter(*sq.Where)
		if err != nil {
			return q, err
		}
		q.Where = &f
	}
	for _, o := range sq.OrderBy {
		q.OrderBy = append(q.OrderBy, index.Order{Field: o.Field.FieldPath, Desc: o.Direction == "DESCENDING"})
	}
	return q, nil
}

// TestCorpusLinks encodes every recorded link's decoded index and compares the payload byte for
// byte, apart from the planner.
func TestCorpusLinks(t *testing.T) {
	c := loadCorpus(t)
	n := 0
	for _, cs := range c.Cases {
		for _, run := range []corpusRun{cs.corpusRun, cs.EmptyCollection} {
			for _, l := range run.Links {
				r := decodeIndex(t, l.Index)
				if got := base64.RawURLEncoding.EncodeToString(index.EncodeIndex(corpusProject, index.DefaultDatabase, r)); got != l.Payload {
					t.Errorf("%s %s: payload\n got %s\nwant %s", cs.ID, run.Collection, got, l.Payload)
				}
				n++
			}
		}
	}
	if n != 2*47 {
		t.Errorf("checked %d links, want 94 (47 per run)", n)
	}
}

// TestCorpus replays the 105 shapes, on both collections, against Check with the recorded
// project's catalogue, and compares the verdict, and for every FAILED_PRECONDITION the status,
// the message and the links, byte for byte.
func TestCorpus(t *testing.T) {
	c := loadCorpus(t)
	cat := corpusCatalog(t, c)
	matched := map[string]int{}
	for _, cs := range c.Cases {
		t.Run(cs.ID, func(t *testing.T) {
			q, err := decodeShape(cs.Request.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, run := range []struct {
				name string
				corpusRun
			}{{"populated", cs.corpusRun}, {"empty", cs.EmptyCollection}} {
				rq := q
				if run.name == "empty" {
					rq.CollectionID = run.Collection
				}
				d := index.Check(rq, cat, index.Enforce)
				if checkRun(t, run.name, cs.Description, d, run.corpusRun) {
					matched[run.name]++
				}
			}
		})
	}
	t.Logf("matched production: populated %d/105, empty %d/105", matched["populated"], matched["empty"])
}

func checkRun(t *testing.T, name, desc string, d index.Decision, want corpusRun) bool {
	t.Helper()
	switch want.Verdict {
	case "ok":
		if d.Verdict != index.Served {
			t.Errorf("%s (%s): %v %s %s, want served", name, desc, d.Verdict, d.Reason, d.Detail)
			return false
		}
		if d.Reject() || index.Suggest(corpusProject, index.DefaultDatabase, d) != nil {
			t.Errorf("%s: a served query rejects", name)
			return false
		}
		return true
	case "FAILED_PRECONDITION":
		if d.Verdict != index.NeedsIndex || !d.Reject() {
			t.Errorf("%s (%s): %v %s %s, want needs_index", name, desc, d.Verdict, d.Reason, d.Detail)
			return false
		}
		e := index.Suggest(corpusProject, index.DefaultDatabase, d)
		ok := true
		if index.HTTPStatus != want.HTTP || index.Status != want.Status {
			t.Errorf("%s: status %d %s, want %d %s", name, index.HTTPStatus, index.Status, want.HTTP, want.Status)
			ok = false
		}
		if e.Message != want.Message {
			t.Errorf("%s (%s): message\n got %q\nwant %q", name, desc, e.Message, want.Message)
			ok = false
		}
		var wantLinks []index.Link
		for _, l := range want.Links {
			wantLinks = append(wantLinks, index.Link{Param: l.Param, Payload: l.Payload})
		}
		if !slices.Equal(e.Links, wantLinks) {
			t.Errorf("%s: links\n got %v\nwant %v", name, e.Links, wantLinks)
			ok = false
		}
		return ok
	}
	t.Fatalf("%s: verdict %q", name, want.Verdict)
	return false
}
