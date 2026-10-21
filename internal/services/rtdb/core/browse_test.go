package core

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/store/storetest"
)

// storeTree writes v at p in one transaction.
func storeTree(t *testing.T, store *PGStore, db DatabaseInfo, p Path, v *Node) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.Begin(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Write(ctx, p, v); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// allKeys pages through the children of p, limit at a time, and returns the keys in the order
// the pages gave them.
func allKeys(t *testing.T, store *PGStore, db DatabaseInfo, p Path, limit int) []string {
	t.Helper()
	var keys []string
	after := ""
	for range 10000 {
		page, err := store.shallow(context.Background(), db, p, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Children) > limit {
			t.Fatalf("page of %d children, limit %d", len(page.Children), limit)
		}
		for _, c := range page.Children {
			keys = append(keys, c.Key)
		}
		if page.Next == "" {
			return keys
		}
		after = page.Next
	}
	t.Fatal("paging does not end")
	return nil
}

func TestShallowPaging(t *testing.T) {
	store := NewPGStore(storetest.NewPool(t))
	ctx := context.Background()
	db, err := store.OpenDatabase(ctx, 482915736204, "chat-app-default-rtdb")
	if err != nil {
		t.Fatal(err)
	}

	// Keys built from bytes on both sides of '/' in path order: ' ', '!' and '-' sort before
	// it, '0' and letters after. Objects, leaves, objects with a priority (a row of their own
	// without a value), at the root and one level down, beside siblings of that level whose
	// rows sort between its own ("list-x", "list!").
	alphabet := []string{"a", "b", "-", " ", "!", "0"}
	rng := rand.New(rand.NewPCG(1, 2))
	for round := range 30 {
		list := map[string]*Node{}
		for range 1 + rng.IntN(40) {
			var k strings.Builder
			for range 1 + rng.IntN(4) {
				k.WriteString(alphabet[rng.IntN(len(alphabet))])
			}
			switch rng.IntN(3) {
			case 0:
				list[k.String()] = Number(float64(rng.IntN(9)))
			case 1:
				list[k.String()] = Object(map[string]*Node{"x": Bool(true)})
			default:
				list[k.String()] = Object(map[string]*Node{"x": String("y"), "z": Number(1)}).WithPriority(Number(2))
			}
		}
		tree := Object(map[string]*Node{"list": Object(list), "list-x": Number(1), "list!": Object(map[string]*Node{"q": Number(2)})})
		storeTree(t, store, db, Path{}, tree)

		want := slices.Sorted(func(yield func(string) bool) {
			for k := range list {
				if !yield(k) {
					return
				}
			}
		})
		for _, limit := range []int{1, 2, 3, 7} {
			want := want
			if len(list) <= limit {
				want = Object(list).Keys() // one page holds them all
			}
			if got := allKeys(t, store, db, Path{"list"}, limit); !slices.Equal(got, want) {
				t.Fatalf("round %d, pages of %d:\n got %q\nwant %q", round, limit, got, want)
			}
		}
		// The root's children, with no upper bound on their rows.
		if got := allKeys(t, store, db, Path{}, 1); !slices.Equal(got, []string{"list", "list!", "list-x"}) {
			t.Fatalf("root: %q", got)
		}

		// A page holding every child is in the order a client sees them, with leaf values.
		page, err := store.shallow(ctx, db, Path{"list"}, "", MaxShallowPage)
		if err != nil {
			t.Fatal(err)
		}
		keys := make([]string, len(page.Children))
		for i, c := range page.Children {
			keys[i] = c.Key
			n := list[c.Key]
			if n.IsLeaf() != (c.Leaf != nil) || (c.Leaf != nil && !Equal(c.Leaf, n)) {
				t.Fatalf("child %q: leaf %v, stored %s", c.Key, c.Leaf, n.ExportJSON())
			}
		}
		if !slices.Equal(keys, Object(list).Keys()) || page.Next != "" || page.Value != nil {
			t.Fatalf("whole page: %q next %q", keys, page.Next)
		}
	}
}

func TestShallowNodes(t *testing.T) {
	store := NewPGStore(storetest.NewPool(t))
	ctx := context.Background()
	db, _ := store.OpenDatabase(ctx, 482915736204, "chat-app-default-rtdb")
	tree, err := ParseData([]byte(`{"n": {"10": 1, "9": 2, "2": 3, "b": {"c": "\u0000x"}}, "s": "hi"}`), nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeTree(t, store, db, Path{}, tree)

	// Integer keys come first by value on a whole page, as clients order them.
	page, err := store.shallow(ctx, db, Path{"n"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range page.Children {
		keys = append(keys, c.Key)
	}
	if !slices.Equal(keys, []string{"2", "9", "10", "b"}) || page.Children[3].Leaf != nil || page.Children[0].Leaf.NumberValue() != 3 {
		t.Fatalf("children %q", keys)
	}
	// Later pages follow the keys' byte order.
	page, _ = store.shallow(ctx, db, Path{"n"}, "", 2)
	if page.Next != "2" || page.Children[0].Key != "10" || page.Children[1].Key != "2" {
		t.Fatalf("first page of two: %+v", page)
	}
	// A leaf is its value, with no children; a string Postgres cannot hold in jsonb comes back.
	page, _ = store.shallow(ctx, db, Path{"s"}, "", 10)
	if page.Value.StringValue() != "hi" || len(page.Children) != 0 {
		t.Fatalf("leaf: %+v", page)
	}
	page, _ = store.shallow(ctx, db, Path{"n", "b"}, "", 10)
	if page.Children[0].Leaf.StringValue() != "\u0000x" {
		t.Fatalf("NUL string: %+v", page)
	}
	// A missing node, or one below a leaf, is empty.
	for _, p := range []Path{{"missing"}, {"s", "x"}} {
		if page, _ := store.shallow(ctx, db, p, "", 10); page.Value != nil || len(page.Children) != 0 {
			t.Errorf("%v: %+v", p, page)
		}
	}

	size, err := store.size(ctx, db, Path{"n"}, 100)
	if err != nil || size.Rows != 4 || size.Capped || size.Bytes == 0 {
		t.Errorf("size of n: %+v %v", size, err)
	}
	size, _ = store.size(ctx, db, Path{}, 3)
	if size.Rows != 3 || !size.Capped {
		t.Errorf("capped size: %+v", size)
	}
	size, _ = store.size(ctx, db, Path{"missing"}, 3)
	if size.Rows != 0 || size.Capped {
		t.Errorf("size of nothing: %+v", size)
	}
}

func TestBrowseNeedsAStoreThatBrowses(t *testing.T) {
	svc := New(newMemStore(), nil, Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.Shallow(context.Background(), Target{Name: "chat-app-default-rtdb", Project: 1}, "/", "", 10); err != ErrUnavailable {
		t.Errorf("Shallow on a store without browsing: %v", err)
	}
	if _, err := svc.Shallow(context.Background(), Target{}, "/a.b", "", 10); AsError(err).HTTP != 400 {
		t.Errorf("Shallow of an invalid path: %v", err)
	}
}
