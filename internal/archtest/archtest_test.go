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
// key wins. Imports inside the same prefix are always allowed. A * in a key stands for one path
// segment and makes each match its own layer: every service under internal/services/* may
// import itself, never another service.
var allowed = map[string][]string{
	"internal/platform":  {"internal/platform"},
	"internal/config":    {"internal/platform"},
	"internal/store":     {"internal/platform"},
	"internal/events":    {"internal/store", "internal/platform"},
	"internal/configgen": {"internal/platform"},
	"internal/tenancy":   {"internal/store", "internal/events", "internal/platform", "internal/config"},
	// Data services resolve keys and service accounts through the control plane, never the reverse.
	"internal/services/*": {"internal/tenancy", "internal/store", "internal/events", "internal/platform", "internal/config"},
	// The console mounts the services' routes through platform/consolemount; only app wires them.
	"internal/admin":  {"internal/tenancy", "internal/configgen", "internal/events", "internal/store", "internal/platform", "internal/config"},
	"internal/server": {"internal/admin", "internal/services/", "internal/tenancy", "internal/platform", "internal/config", "internal/store"},
	"internal/app":    {"internal/"},
	"cmd":             {"internal/", "pkg/"},
	"test":            {"internal/", "pkg/"},
	"tools":           {"internal/", "pkg/"},
	"pkg":             {},
	// The published Go library stands alone: it may import itself (its helper commands), never
	// the server.
	"sdk/go/adminauth": {},
}

// denied narrows a layer: a package under a key may not import anything under the prefixes
// listed, even within its own layer. A * stands for one path segment, in keys and prefixes.
var denied = map[string][]string{
	// A service's domain logic knows nothing of HTTP routing or of the transports above it, so
	// one core serves its API, its console screens and any later protocol. net/http itself
	// stays allowed: core maps errors to status codes and calls Google and Apple.
	"internal/services/*/core": {"internal/services/*/api", "internal/services/*/console", "internal/platform/httpx"},
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
	for key, prefixes := range denied {
		if _, ok := under(from, key); !ok {
			continue
		}
		for _, p := range prefixes {
			if _, ok := under(target, p); ok {
				return false
			}
		}
	}
	layer, key := "", ""
	for k := range allowed {
		m, ok := under(from, k)
		// On a tie the literal key beats a wildcard one.
		if ok && (len(m) > len(layer) || len(m) == len(layer) && !strings.Contains(k, "*")) {
			layer, key = m, k
		}
	}
	if layer == "" {
		return false
	}
	if target == layer || strings.HasPrefix(target, layer+"/") {
		return true
	}
	for _, ok := range allowed[key] {
		if strings.HasPrefix(target, ok) {
			return true
		}
	}
	return false
}

// under reports whether path is pattern or lies below it, matching whole segments, with * in
// pattern standing for any one segment. It returns the part of path that pattern matched.
func under(path, pattern string) (string, bool) {
	segs, pats := strings.Split(path, "/"), strings.Split(pattern, "/")
	if len(segs) < len(pats) {
		return "", false
	}
	for i, p := range pats {
		if p != "*" && p != segs[i] {
			return "", false
		}
	}
	return strings.Join(segs[:len(pats)], "/"), true
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
		{"internal/servicesx", "internal/platform/log", false},
		{"internal/services", "internal/platform/log", false},
		// A service imports itself and the platform, never another service.
		{"internal/services/auth/api", "internal/services/auth/core", true},
		{"internal/services/auth/console", "internal/services/auth/api", true},
		{"internal/services/auth/console", "internal/platform/consolemount", true},
		{"internal/services/rtdb/core", "internal/services/auth/core", false},
		{"internal/services/auth/console", "internal/services/rtdb/api", false},
		{"internal/services/rtdb/api", "internal/services/rtdbx/core", false},
		// A service's core stays below its transports.
		{"internal/services/auth/core", "internal/services/auth/tokens", true},
		{"internal/services/auth/core", "internal/platform/apierr", true},
		{"internal/services/auth/core", "internal/services/auth/api", false},
		{"internal/services/auth/core", "internal/services/auth/console", false},
		{"internal/services/rtdb/core/rules", "internal/services/rtdb/api", false},
		{"internal/services/rtdb/core", "internal/platform/httpx", false},
		{"internal/services/auth/api", "internal/platform/httpx", true},
		// Only app wires services into the console and the admin APIs.
		{"internal/admin/console", "internal/services/auth/core", false},
		{"internal/admin/console", "internal/services/auth/console", false},
		{"internal/admin/console", "internal/platform/consolemount", true},
		{"internal/app", "internal/services/auth/console", true},
		{"internal/platform/consolemount", "internal/platform/authn", true},
		{"internal/platform/consolemount", "internal/tenancy/core", false},
		{"internal/platform/consolemount", "internal/services/auth/core", false},
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
