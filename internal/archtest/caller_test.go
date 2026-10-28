package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// documentsAPI is the Document Database's service layer, whose api.Call.Caller admits a caller
// without a credential (ADR-030).
const documentsAPI = "internal/services/documents/api"

// callerSetters are the packages that may set api.Call.Caller: the dashboard's console routes
// alone, which admit the signed-in account as an admin of the projects it owns. Anywhere else, a
// Caller would let a request skip the credential check.
var callerSetters = []string{"internal/services/documents/console"}

// TestCallerOnlyFromConsole: an api.Call literal with a Caller field, or an assignment to a
// .Caller in a file of the api package or one that imports it, appears only in callerSetters.
// Without type information, the check reads the syntax: Call as the api package names it.
func TestCallerOnlyFromConsole(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal", "test"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			pkg := filepath.ToSlash(filepath.Dir(rel))
			for _, allowed := range callerSetters {
				if pkg == allowed {
					return nil
				}
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, pos := range callerSets(f, pkg == documentsAPI) {
				t.Errorf("%s: sets api.Call.Caller; only %v may (ADR-030)", fset.Position(pos), callerSetters)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// callerSets returns where f sets api.Call.Caller. inAPI is whether f is in the api package
// itself, where the type is named Call unqualified.
func callerSets(f *ast.File, inAPI bool) []token.Pos {
	api := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == module+documentsAPI {
			api = "api"
			if imp.Name != nil {
				api = imp.Name.Name
			}
		}
	}
	if api == "" && !inAPI {
		return nil
	}
	isCall := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Ident:
			return inAPI && e.Name == "Call"
		case *ast.SelectorExpr:
			x, ok := e.X.(*ast.Ident)
			return ok && x.Name == api && e.Sel.Name == "Call"
		}
		return false
	}
	var out []token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if !isCall(n.Type) {
				return true
			}
			for _, el := range n.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Caller" {
						out = append(out, kv.Pos())
					}
				}
			}
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if s, ok := l.(*ast.SelectorExpr); ok && s.Sel.Name == "Caller" {
					out = append(out, s.Pos())
				}
			}
		}
		return true
	})
	return out
}

// TestCallerSetsThemselves: the check finds a Caller in an api.Call literal under any import
// name, and an assignment to .Caller, and nothing in a file that does not import the api package.
func TestCallerSetsThemselves(t *testing.T) {
	imp := strconv.Quote(module + documentsAPI)
	for _, c := range []struct {
		src  string
		want int
	}{
		{`package p; import ` + imp + `; var c = api.Call{Caller: nil}`, 1},
		{`package p; import d ` + imp + `; var c = d.Call{Caller: nil, Authorization: "x"}`, 1},
		{`package p; import d ` + imp + `; var c = d.Call{Authorization: "x"}`, 0},
		{`package p; import d ` + imp + `; func f(c *d.Call) { c.Caller = nil }`, 1},
		{`package p; type Call struct{ Caller any }; var c = Call{Caller: nil}`, 0},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "p.go", c.src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(callerSets(f, false)); got != c.want {
			t.Errorf("%s: %d sets, want %d", c.src, got, c.want)
		}
	}
}
