package documents

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// listenWait bounds each step of the Listen tests; a snapshot that has not arrived by then never will.
const listenWait = 5 * time.Second

// listenColl is a collection of the test's own, so that listeners of different tests never see
// each other's writes.
func listenColl(c *firestore.Client) *firestore.CollectionRef {
	return c.Collection(fmt.Sprintf("listen-go-%d", time.Now().UnixNano()))
}

// nexter is what nextOf needs of the document and query snapshot iterators.
type nexter[S any] interface {
	Next() (S, error)
	Stop()
}

// nextOf waits for the iterator's next snapshot, and fails the test if none comes.
func nextOf[S any](t T, it nexter[S], what string) S {
	t.Helper()
	type result struct {
		s   S
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := it.Next()
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("%s: Next: %v", what, r.err)
		}
		return r.s
	case <-time.After(listenWait):
		t.Fatalf("%s: no snapshot within %s", what, listenWait)
		panic("unreachable")
	}
}

type change struct {
	Kind     firestore.DocumentChangeKind
	ID       string
	N        int64
	Old, New int
}

// changes summarises a query snapshot's DocumentChanges as (kind, id, n, old index, new index).
func changes(t T, s *firestore.QuerySnapshot) []change {
	t.Helper()
	var out []change
	for _, ch := range s.Changes {
		n, err := ch.Doc.DataAt("n")
		if err != nil {
			t.Fatalf("change %s: %v", ch.Doc.Ref.ID, err)
		}
		out = append(out, change{ch.Kind, ch.Doc.Ref.ID, n.(int64), ch.OldIndex, ch.NewIndex})
	}
	return out
}

// ids lists the snapshot's documents in order, as id:n.
func ids(t T, s *firestore.QuerySnapshot) []string {
	t.Helper()
	docs, err := s.Documents.GetAll()
	if err != nil {
		t.Fatalf("snapshot documents: %v", err)
	}
	out := []string{}
	for _, d := range docs {
		out = append(out, fmt.Sprintf("%s:%v", d.Ref.ID, d.Data()["n"]))
	}
	return out
}

func same(t T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %+v\n want %+v", what, got, want)
	}
}

func put(t T, ref *firestore.DocumentRef, data map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), listenWait)
	defer cancel()
	if _, err := ref.Set(ctx, data); err != nil {
		t.Fatalf("Set %s: %v", ref.ID, err)
	}
}

func drop(t T, ref *firestore.DocumentRef) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), listenWait)
	defer cancel()
	if _, err := ref.Delete(ctx); err != nil {
		t.Fatalf("Delete %s: %v", ref.ID, err)
	}
}

// TestListen: DocumentRef.Snapshots and Query.Snapshots, with the writes made by a second client.
func TestListen(t *testing.T) {
	check(t, "document", func(t T) {
		listener, writer := newClient(t), newClient(t)
		defer listener.Close()
		defer writer.Close()
		coll := listenColl(listener)
		it := coll.Doc("d").Snapshots(context.Background())
		defer it.Stop()

		s := nextOf(t, it, "initial")
		same(t, "initial exists", s.Exists(), false)

		wref := writer.Collection(coll.ID).Doc("d")
		put(t, wref, map[string]any{"n": 1, "s": "a"})
		s = nextOf(t, it, "after set")
		same(t, "after set", [2]any{s.Exists(), s.Data()}, [2]any{true, map[string]any{"n": int64(1), "s": "a"}})

		ctx, cancel := context.WithTimeout(context.Background(), listenWait)
		defer cancel()
		if _, err := wref.Update(ctx, []firestore.Update{{Path: "n", Value: firestore.Increment(4)}}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		s = nextOf(t, it, "after update")
		same(t, "after update", s.Data(), map[string]any{"n": int64(5), "s": "a"})

		drop(t, wref)
		s = nextOf(t, it, "after delete")
		same(t, "after delete exists", s.Exists(), false)
	})

	check(t, "query changes", func(t T) {
		listener, writer := newClient(t), newClient(t)
		defer listener.Close()
		defer writer.Close()
		coll := listenColl(listener)
		wcoll := writer.Collection(coll.ID)
		put(t, wcoll.Doc("a"), map[string]any{"n": 1})
		put(t, wcoll.Doc("b"), map[string]any{"n": 2})

		it := coll.Where("n", ">", 0).OrderBy("n", firestore.Asc).Snapshots(context.Background())
		defer it.Stop()

		s := nextOf(t, it, "initial")
		same(t, "initial docs", ids(t, s), []string{"a:1", "b:2"})
		same(t, "initial changes", changes(t, s), []change{
			{firestore.DocumentAdded, "a", 1, -1, 0}, {firestore.DocumentAdded, "b", 2, -1, 1}})

		put(t, wcoll.Doc("c"), map[string]any{"n": 3})
		s = nextOf(t, it, "add")
		same(t, "add docs", ids(t, s), []string{"a:1", "b:2", "c:3"})
		same(t, "add changes", changes(t, s), []change{{firestore.DocumentAdded, "c", 3, -1, 2}})

		put(t, wcoll.Doc("a"), map[string]any{"n": 10})
		s = nextOf(t, it, "modify")
		same(t, "modify docs", ids(t, s), []string{"b:2", "c:3", "a:10"})
		same(t, "modify changes", changes(t, s), []change{{firestore.DocumentModified, "a", 10, 0, 2}})

		put(t, wcoll.Doc("b"), map[string]any{"n": 0}) // leaves the query
		s = nextOf(t, it, "leave")
		same(t, "leave docs", ids(t, s), []string{"c:3", "a:10"})
		same(t, "leave changes", changes(t, s), []change{{firestore.DocumentRemoved, "b", 2, 0, -1}})

		drop(t, wcoll.Doc("c"))
		s = nextOf(t, it, "delete")
		same(t, "delete docs", ids(t, s), []string{"a:10"})
		same(t, "delete changes", changes(t, s), []change{{firestore.DocumentRemoved, "c", 3, 0, -1}})
	})

	check(t, "limit query", func(t T) {
		listener, writer := newClient(t), newClient(t)
		defer listener.Close()
		defer writer.Close()
		coll := listenColl(listener)
		wcoll := writer.Collection(coll.ID)
		for i := 1; i <= 3; i++ {
			put(t, wcoll.Doc(fmt.Sprintf("d%d", i)), map[string]any{"n": i})
		}
		it := coll.OrderBy("n", firestore.Asc).Limit(2).Snapshots(context.Background())
		defer it.Stop()

		s := nextOf(t, it, "initial")
		same(t, "initial docs", ids(t, s), []string{"d1:1", "d2:2"})

		put(t, wcoll.Doc("d0"), map[string]any{"n": 0}) // pushes d2 out of the limit
		s = nextOf(t, it, "push out")
		same(t, "push out docs", ids(t, s), []string{"d0:0", "d1:1"})
		same(t, "push out changes", changes(t, s), []change{
			{firestore.DocumentRemoved, "d2", 2, 1, -1}, {firestore.DocumentAdded, "d0", 0, -1, 0}})

		drop(t, wcoll.Doc("d0")) // d2 comes back in
		s = nextOf(t, it, "pull in")
		same(t, "pull in docs", ids(t, s), []string{"d1:1", "d2:2"})
		same(t, "pull in changes", changes(t, s), []change{
			{firestore.DocumentRemoved, "d0", 0, 0, -1}, {firestore.DocumentAdded, "d2", 2, -1, 1}})
	})

	check(t, "stop", func(t T) {
		listener := newClient(t)
		defer listener.Close()
		it := listenColl(listener).Doc("d").Snapshots(context.Background())
		nextOf(t, it, "initial")
		it.Stop()
		done := make(chan error, 1)
		go func() { _, err := it.Next(); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, iterator.Done) {
				t.Fatalf("Next after Stop: %v, want iterator.Done", err)
			}
		case <-time.After(listenWait):
			t.Fatalf("Next after Stop did not return within %s", listenWait)
		}

		// A stopped listener's stream is gone: a new one on the same client works.
		it2 := listenColl(listener).Doc("e").Snapshots(context.Background())
		defer it2.Stop()
		nextOf(t, it2, "second listener")
	})
}
