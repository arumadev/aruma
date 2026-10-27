package documents

import (
	"strings"
	"testing"

	"cloud.google.com/go/firestore"
	pb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSDKQueries runs the Go SDK's queries, RunQuery and RunAggregationQuery (F3), on the
// documents of indexes-105.json's populated run: filters, orders, limits, cursors, OR, in, a
// collection group, the aggregations, and the shapes that need a composite index or a
// collection group exemption.
func TestSDKQueries(t *testing.T) {
	c := newClient(t)
	defer c.Close()
	ctx := t.Context()
	seed := []map[string]any{
		{"a": 1, "b": 1, "c": "x", "d": 10, "tags": []any{"red", "blue"}},
		{"a": 1, "b": 2, "c": "y", "d": 20, "tags": []any{"red"}},
		{"a": 2, "b": 1, "c": "x", "d": 30, "tags": []any{"green"}},
		{"a": 2, "b": 2, "c": "z", "d": 40, "tags": []any{}},
		{"a": 3, "b": 3, "c": "y", "d": 50, "tags": []any{"blue", "green"}},
		{"a": 1, "b": 1, "c": "z", "d": 60},
	}
	col := c.Collection("sdk-queries")
	b := c.BulkWriter(ctx)
	for i, d := range seed {
		id := "d" + string(rune('1'+i))
		if _, err := b.Set(col.Doc(id), d); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if _, err := b.Set(col.Doc(id).Collection("sdk-queries-cg").Doc("s"), d); err != nil {
				t.Fatal(err)
			}
		}
	}
	b.End()

	ids := func(t T, q firestore.Query) []string {
		t.Helper()
		snaps, err := q.Documents(t.Context()).GetAll()
		mustOK(t, err, "Documents")
		var out []string
		for _, s := range snaps {
			out = append(out, strings.TrimPrefix(s.Ref.Path, root()+"/"))
		}
		return out
	}
	for _, c := range []struct {
		name string
		q    firestore.Query
		want string
	}{
		{"where ==", col.Where("a", "==", 1), "d1 d2 d6"},
		{"where range", col.Where("d", ">", 25), "d3 d4 d5 d6"},
		{"order by desc and limit", col.OrderBy("d", firestore.Desc).Limit(2), "d6 d5"},
		{"limit to last", col.OrderBy("d", firestore.Asc).LimitToLast(2), "d5 d6"},
		{"start after", col.OrderBy("d", firestore.Asc).StartAfter(30), "d4 d5 d6"},
		{"end before", col.OrderBy("d", firestore.Asc).EndBefore(30), "d1 d2"},
		{"offset", col.OrderBy("d", firestore.Asc).Offset(4), "d5 d6"},
		{"in", col.Where("c", "in", []any{"x", "z"}), "d1 d3 d4 d6"},
		{"not in", col.Where("c", "not-in", []any{"x", "z"}), "d2 d5"},
		{"array-contains", col.Where("tags", "array-contains", "red"), "d1 d2"},
		{"array-contains-any", col.Where("tags", "array-contains-any", []any{"green", "blue"}), "d1 d3 d5"},
		{"or", col.WhereEntity(firestore.OrFilter{Filters: []firestore.EntityFilter{
			firestore.PropertyFilter{Path: "a", Operator: "==", Value: 3},
			firestore.PropertyFilter{Path: "b", Operator: "==", Value: 2}}}), "d2 d4 d5"},
		{"collection group", c.CollectionGroup("sdk-queries-cg").Query, "d1 d2 d3"},
	} {
		check(t, c.name, func(t T) {
			got := ids(t, c.q)
			for i, p := range got {
				got[i] = strings.TrimSuffix(strings.TrimPrefix(p, "sdk-queries/"), "/sdk-queries-cg/s")
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("%v, want %s", got, c.want)
			}
		})
	}
	check(t, "a shape that needs an index", func(t T) {
		_, err := col.Where("a", "==", 1).OrderBy("d", firestore.Asc).Documents(t.Context()).GetAll()
		wantCode(t, err, codes.FailedPrecondition, "")
		msg := status.Convert(err).Message()
		prefix := "The query requires an index. You can create it here: https://console.firebase.google.com/v1/r/project/" +
			live.expected.ProjectID + "/firestore/indexes?create_composite="
		if !strings.HasPrefix(msg, prefix) {
			t.Errorf("message %q", msg)
		}
	})
	check(t, "a collection group filter needs its exemption", func(t T) {
		_, err := c.CollectionGroup("sdk-queries-cg").Where("a", "==", 1).Documents(t.Context()).GetAll()
		wantCode(t, err, codes.FailedPrecondition, "")
		if msg := status.Convert(err).Message(); !strings.HasPrefix(msg, "The query requires a COLLECTION_GROUP_ASC index for collection sdk-queries-cg and field a.") {
			t.Errorf("message %q", msg)
		}
	})
	check(t, "count, sum, avg", func(t T) {
		q := col.Query
		res, err := q.NewAggregationQuery().WithCount("n").WithSum("d", "s").WithAvg("d", "v").Get(t.Context())
		mustOK(t, err, "aggregation")
		n, _ := res["n"].(*pb.Value)
		s, _ := res["s"].(*pb.Value)
		v, _ := res["v"].(*pb.Value)
		if n.GetIntegerValue() != 6 || s.GetIntegerValue() != 210 || v.GetDoubleValue() != 35 {
			t.Errorf("count %v, sum %v, avg %v", n, s, v)
		}
	})
}
