// Package archtest enforces the dependency rules between internal packages. CI runs it with
// every other test, so a forbidden import fails the build.
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/osama300/aurma-project/"

// allowed maps a package prefix to the internal prefixes it may import. The longest matching
// key wins. Imports inside the same prefix are always allowed.
var allowed = map[string][]string{
	"internal/platform":  {"internal/platform"},
	"internal/config":    {"internal/platform"},
	"internal/store":     {"internal/platform"},
	"internal/events":    {"internal/store", "internal/platform"},
	"internal/configgen": {"internal/platform"},
	"internal/tenancy":   {"internal/store", "internal/events", "internal/platform", "internal/config"},
	// Data services resolve keys and service accounts through the control plane, never the reverse.
	"internal/services/auth": {"internal/tenancy", "internal/store", "internal/events", "internal/platform", "internal/config"},
	"internal/admin":         {"internal/services/auth", "internal/tenancy", "internal/configgen", "internal/events", "internal/store", "internal/platform", "internal/config"},
	"internal/server":        {"internal/admin", "internal/services/auth", "internal/tenancy", "internal/platform", "internal/config", "internal/store"},
	"internal/app":           {"internal/"},
	"cmd":                    {"internal/", "pkg/"},
	"test":                   {"internal/", "pkg/"},
	"tools":                  {"internal/", "pkg/"},
	"pkg":                    {},
	// The published Go library stands alone: it may import itself (its helper commands), never
	// the server.
	"sdk/go/adminauth": {},
}

func TestDependencyRules(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "dashboard" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(p, module) {
				continue
			}
			target := strings.TrimPrefix(p, module)
			if !permitted(rel, target) {
				t.Errorf("%s imports %s, which its layer may not depend on", rel, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func permitted(from, target string) bool {
	if from == "internal/archtest" {
		return true
	}
	layer := ""
	for prefix := range allowed {
		if (from == prefix || strings.HasPrefix(from, prefix+"/")) && len(prefix) > len(layer) {
			layer = prefix
		}
	}
	if layer == "" {
		return false
	}
	if target == layer || strings.HasPrefix(target, layer+"/") {
		return true
	}
	for _, ok := range allowed[layer] {
		if strings.HasPrefix(target, ok) {
			return true
		}
	}
	return false
}

func TestRulesThemselves(t *testing.T) {
	cases := []struct {
		from, target string
		want         bool
	}{
		{"internal/admin/mgmt", "internal/tenancy/core", true},
		{"internal/tenancy/core", "internal/admin/console", false},
		{"internal/platform/authn", "internal/tenancy/core", false},
		{"internal/configgen", "internal/tenancy", false},
		{"internal/services/auth/core", "internal/tenancy/core", true},
		{"internal/tenancy/core", "internal/services/auth/core", false},
		{"internal/store", "internal/store/migrations", true},
		{"cmd/aruma", "internal/app", true},
		{"internal/unknown", "internal/platform/log", false},
	}
	for _, c := range cases {
		if got := permitted(c.from, c.target); got != c.want {
			t.Errorf("permitted(%q, %q) = %v, want %v", c.from, c.target, got, c.want)
		}
	}
}

func repoRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
