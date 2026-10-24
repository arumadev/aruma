package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every Storage write commits synchronously, so it runs in a transaction begun by begin or inTx
// (durability.go, ADR-022 §5). These tests hold every task's code in this package to that:
//
//  1. A write query (INSERT, UPDATE, DELETE, or SELECT ... FOR UPDATE) is called on a local
//     *queries.Queries, the q of an inTx closure or queries.New(tx) after begin, never on a
//     field such as s.reads.
//  2. queries.New never takes the pool, except in New, for s.reads.
//  3. s.reads appears only as the receiver of a read query.
//  4. Nothing executes on the pool directly (Exec, CopyFrom, SendBatch).

// writeQueries returns the queries of ../queries that must run in a transaction.
func writeQueries(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	name := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	writes := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		idx := name.FindAllStringSubmatchIndex(src, -1)
		for i, m := range idx {
			end := len(src)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			body := src[m[1]:end]
			if nl := strings.IndexByte(body, '\n'); nl >= 0 {
				body = body[nl+1:] // past the rest of the name line (:one, :exec)
			}
			if isWrite(body) {
				writes[src[m[2]:m[3]]] = true
			}
		}
	}
	return writes
}

var writeKeyword = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE)\b`)

// isWrite reports whether a query's text, after its name line, writes or locks.
func isWrite(q string) bool {
	var body []string
	for _, line := range strings.Split(q, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		body = append(body, line)
	}
	text := strings.TrimSpace(strings.Join(body, " "))
	return !strings.HasPrefix(strings.ToUpper(text), "SELECT") || writeKeyword.MatchString(text)
}

// checkWrites returns the rule violations in files.
func checkWrites(fset *token.FileSet, files []*ast.File, writes map[string]bool) []string {
	var bad []string
	report := func(n ast.Node, msg string) { bad = append(bad, fset.Position(n.Pos()).String()+": "+msg) }
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// Rule 3: s.reads as anything but a read query's receiver.
			readsOK := map[*ast.SelectorExpr]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// Rule 1.
				if writes[sel.Sel.Name] {
					if _, local := sel.X.(*ast.Ident); !local {
						report(call, sel.Sel.Name+" is called on a field, outside a transaction")
					}
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "reads" && !writes[sel.Sel.Name] {
					readsOK[inner] = true
				}
				// Rule 2.
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "queries" && sel.Sel.Name == "New" && fd.Name.Name != "New" {
					for _, arg := range call.Args {
						if isPool(arg) {
							report(call, "queries.New on the pool: writes would commit outside a transaction")
						}
					}
				}
				// Rule 4.
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "pool" &&
					slices.Contains([]string{"Exec", "CopyFrom", "SendBatch"}, sel.Sel.Name) {
					report(call, "pool."+sel.Sel.Name+" outside a transaction")
				}
				return true
			})
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "reads" && !readsOK[sel] {
					report(sel, "reads used other than for a read query")
				}
				return true
			})
		}
	}
	return bad
}

func isPool(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "pool"
	case *ast.SelectorExpr:
		return x.Sel.Name == "pool"
	}
	return false
}

func TestWritesOnlyInTransactions(t *testing.T) {
	writes := writeQueries(t)
	for _, w := range []string{"InsertObject", "LockObject", "ReplaceObject", "QueueBlobGC", "ReleaseUsage"} {
		if !writes[w] {
			t.Fatalf("%s is not recognised as a write", w)
		}
	}
	for _, r := range []string{"GetObject", "GetUsage", "GetInstance"} {
		if writes[r] {
			t.Fatalf("%s is taken for a write", r)
		}
	}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	for _, v := range checkWrites(fset, files, writes) {
		t.Error(v)
	}
}

// The checker finds what it is for.
func TestWritesCheckerCatches(t *testing.T) {
	src := `package core
func New() { _ = queries.New(pool) }
func (s *Service) ok(ctx context.Context) error {
	_, _ = s.reads.GetObject(ctx, p)
	tx, _ := s.begin(ctx)
	q := queries.New(tx)
	_, _ = q.InsertObject(ctx, p)
	return s.inTx(ctx, func(q *queries.Queries) error { return releaseObject(ctx, q, 1, "k", 1) })
}
func (s *Service) bad(ctx context.Context) {
	_, _ = s.reads.InsertObject(ctx, p)
	_ = queries.New(s.pool).QueueBlobGC(ctx, p)
	_ = releaseObject(ctx, s.reads, 1, "k", 1)
	_, _ = s.pool.Exec(ctx, "DELETE FROM storage.objects")
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := checkWrites(fset, []*ast.File{f}, map[string]bool{"InsertObject": true, "QueueBlobGC": true})
	for _, want := range []string{
		"x.go:11:9: InsertObject is called on a field",
		"x.go:11:9: reads used other than for a read query",
		"x.go:12:6: queries.New on the pool",
		"x.go:12:6: QueueBlobGC is called on a field",
		"x.go:13:25: reads used other than for a read query",
		"x.go:14:9: pool.Exec outside a transaction",
	} {
		if !slices.ContainsFunc(got, func(v string) bool { return strings.HasPrefix(v, want) }) {
			t.Errorf("missed %q in %q", want, got)
		}
	}
	if len(got) != 6 {
		t.Errorf("%d findings, want 6: %q", len(got), got)
	}
}
