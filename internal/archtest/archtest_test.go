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

const module = "github.com/arumadev/aruma/"

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
	// The rules language stands alone: core feeds it data through an interface it defines.
	"internal/services/rtdb/rules": {},
	// So does the wire protocol, tested on frames recorded from the emulator (rtdb-layout.md).
	"internal/services/rtdb/wire": {},
	// Storage's bytes on disk and its wire formats import Go's standard library alone, so no
	// object name or header reaches the disk except through core (ADR-022 §1).
	"internal/services/storage/blob": {},
	"internal/services/storage/wire": {},
	// The Firestore and Storage rules language imports nothing internal: the services feed it
	// values and documents through its own types, and app wires it (ADR-021, rules-layout.md).
	"internal/platform/rules": {},
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
	"sdk/go/adminauth":    {},
	"sdk/go/adminstorage": {},
}

// denied narrows a layer: a package under a key may not import anything under the prefixes
// listed, even within its own layer. A * stands for one path segment, in keys and prefixes.
var denied = map[string][]string{
	// A service's domain logic knows nothing of HTTP routing or of the transports above it, so
	// one core serves its API, its console screens and any later protocol. net/http itself
	// stays allowed: core maps errors to status codes and calls Google and Apple.
	"internal/services/*/core": {"internal/services/*/api", "internal/services/*/console", "internal/services/*/gcs",
		"internal/services/*/media", "internal/platform/httpx"},
	// Storage's transports stand side by side on core and media, none on another (ADR-022 §1):
	// the Firebase REST API, the GCS JSON API and the console, and media below the three. Auth's
	// console builds on its own API, so the console's rule is Storage's alone.
	"internal/services/*/api":           {"internal/services/*/gcs", "internal/services/*/console"},
	"internal/services/*/gcs":           {"internal/services/*/api", "internal/services/*/console"},
	"internal/services/storage/console": {"internal/services/storage/api", "internal/services/storage/gcs"},
	// Storage's core knows no wire format either: each surface converts core's values (ADR-022 §1).
	"internal/services/storage/core": {"internal/services/storage/wire"},
	"internal/services/*/media":      {"internal/services/*/api", "internal/services/*/gcs", "internal/services/*/console", "internal/services/*/core"},
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
		// The rules language imports nothing; core imports it.
		{"internal/services/rtdb/core", "internal/services/rtdb/rules", true},
		{"internal/services/rtdb/rules", "internal/services/rtdb/core", false},
		{"internal/services/rtdb/rules", "internal/platform/log", false},
		{"internal/services/auth/api", "internal/platform/httpx", true},
		{"internal/services/rtdb/api", "internal/services/rtdb/wire", true},
		{"internal/services/rtdb/core", "internal/services/rtdb/wire", true},
		{"internal/services/rtdb/wire", "internal/services/rtdb/core", false},
		{"internal/services/rtdb/wire", "internal/platform/log", false},
		// The ID token contract lives in platform: Auth implements it, RTDB consumes it, only
		// app joins the two, and neither service reaches into the other for it.
		{"internal/platform/idtoken", "internal/platform/log", true},
		{"internal/platform/idtoken", "internal/services/auth/core", false},
		{"internal/platform/idtoken", "internal/tenancy/core", false},
		{"internal/services/auth/core", "internal/platform/idtoken", true},
		{"internal/services/rtdb/core", "internal/platform/idtoken", true},
		{"internal/services/rtdb/api", "internal/platform/idtoken", true},
		{"internal/services/rtdb/core", "internal/services/auth/tokens", false},
		{"internal/services/rtdb/api", "internal/services/auth/core", false},
		{"internal/services/auth/core", "internal/services/rtdb/core", false},
		{"internal/app", "internal/platform/idtoken", true},
		// The Firestore and Storage rules language stands alone in platform; the services, the
		// control plane that stores rulesets, the admin API and app import it.
		{"internal/platform/rules", "internal/platform/idtoken", false},
		{"internal/platform/rules", "internal/platform/log", false},
		{"internal/platform/rules", "internal/services/storage/core", false},
		{"internal/platform/rules", "internal/services/firestore/core", false},
		{"internal/platform/rules", "internal/tenancy/core", false},
		{"internal/platform/rules", "internal/store", false},
		{"internal/platform/rules", "internal/config", false},
		{"internal/platform/rules/rulestest", "internal/platform/rules", true},
		{"internal/platform/rules/rulestest", "internal/store", false},
		{"internal/services/storage/core", "internal/platform/rules", true},
		{"internal/services/firestore/core", "internal/platform/rules", true},
		{"internal/tenancy/core", "internal/platform/rules", true},
		{"internal/admin/mgmt", "internal/platform/rules", true},
		{"internal/config", "internal/platform/rules", true},
		{"internal/app", "internal/platform/rules", true},
		// Storage (ADR-022 §1): blob and wire stand alone; core knows no transport; the three
		// transports and media do not import one another; media stays below core's callers.
		{"internal/services/storage/blob", "internal/platform/log", false},
		{"internal/services/storage/blob", "internal/services/storage/core", false},
		{"internal/services/storage/wire", "internal/services/storage/core", false},
		{"internal/services/storage/wire", "internal/platform/httpx", false},
		{"internal/services/storage/core", "internal/services/storage/blob", true},
		{"internal/services/storage/core", "internal/services/storage/queries", true},
		{"internal/services/storage/core", "internal/platform/idtoken", true},
		{"internal/services/storage/core", "internal/platform/crypto", true},
		{"internal/services/storage/core", "internal/tenancy/core", true},
		{"internal/services/storage/core", "internal/store", true},
		{"internal/services/storage/core", "internal/services/storage/wire", false},
		{"internal/services/storage/core", "internal/services/storage/api", false},
		{"internal/services/storage/core", "internal/services/storage/gcs", false},
		{"internal/services/storage/core", "internal/services/storage/console", false},
		{"internal/services/storage/core", "internal/services/storage/media", false},
		{"internal/services/storage/core", "internal/platform/httpx", false},
		{"internal/services/storage/media", "internal/services/storage/wire", true},
		{"internal/services/storage/media", "internal/services/storage/core", false},
		{"internal/services/storage/media", "internal/services/storage/api", false},
		{"internal/services/storage/media", "internal/services/storage/gcs", false},
		{"internal/services/storage/media", "internal/services/storage/console", false},
		{"internal/services/storage/api", "internal/services/storage/core", true},
		{"internal/services/storage/api", "internal/services/storage/media", true},
		{"internal/services/storage/api", "internal/services/storage/wire", true},
		{"internal/services/storage/api", "internal/platform/httpx", true},
		{"internal/services/storage/api", "internal/services/storage/gcs", false},
		{"internal/services/storage/api", "internal/services/storage/console", false},
		{"internal/services/storage/gcs", "internal/services/storage/core", true},
		{"internal/services/storage/gcs", "internal/services/storage/media", true},
		{"internal/services/storage/gcs", "internal/services/storage/api", false},
		{"internal/services/storage/gcs", "internal/services/storage/console", false},
		{"internal/services/storage/console", "internal/services/storage/core", true},
		{"internal/services/storage/console", "internal/services/storage/media", true},
		{"internal/services/storage/console", "internal/platform/consolemount", true},
		{"internal/services/storage/console", "internal/services/storage/api", false},
		{"internal/services/storage/console", "internal/services/storage/gcs", false},
		{"internal/services/storage/core", "internal/services/rtdb/core", false},
		{"internal/services/storage/api", "internal/services/auth/core", false},
		{"internal/admin/mgmt", "internal/services/storage/core", false},
		{"internal/config", "internal/services/storage/core", false},
		{"internal/app", "internal/services/storage/core", true},
		// Auth's console keeps building on its API.
		{"internal/services/auth/console", "internal/services/auth/api", true},
		// The published Go libraries stand alone.
		{"sdk/go/adminstorage", "internal/services/storage/core", false},
		{"sdk/go/adminstorage", "internal/platform/log", false},
		{"sdk/go/adminauth", "internal/platform/log", false},
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

// stdlibOnly lists the packages that import Go's standard library alone (ADR-021 §1, §2, ADR-022 §1): no
// module outside the standard library, whose first path element has no dot. TestDependencyRules
// sees only this module's imports, so this test covers the rest. Test files are held to it too.
// What a package may import of this module besides is listed with it, exactly:
//   - rulestest, the oracle's reader, imports the contract and nothing else;
//   - the external tests of the rules package (package rules_test: the oracle, grammar and fuzz
//     tests, which use the public contract alone) import the package and rulestest.
var stdlibOnly = map[string]struct{ files, externalTests []string }{
	"internal/platform/rules":           {externalTests: []string{"internal/platform/rules", "internal/platform/rules/rulestest"}},
	"internal/platform/rules/rulestest": {files: []string{"internal/platform/rules"}},
	"internal/services/storage/blob":    {},
	"internal/services/storage/wire":    {},
}

func TestStandardLibraryOnly(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for pkg, allowed := range stdlibOnly {
		files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pkg), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Errorf("%s has no Go files", pkg)
		}
		for _, path := range files {
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			ok := allowed.files
			if strings.HasSuffix(path, "_test.go") && strings.HasSuffix(f.Name.Name, "_test") {
				ok = append(append([]string{}, ok...), allowed.externalTests...)
			}
		imports:
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				for _, a := range ok {
					if p == module+a {
						continue imports
					}
				}
				if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
					t.Errorf("%s/%s imports %s; the package imports the standard library alone", pkg, filepath.Base(path), p)
				}
			}
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
