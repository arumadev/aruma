// Package scopecheck is the isolation guard from the Phase 1 plan, shared by every query
// package: a query that touches a project's rows must filter on project_number, or declare why
// it may not:
//
//	-- scope: global — <reason>   (resolves a project from a globally unique key)
//	-- scope: owner — <reason>    (lists by owner_account_id)
package scopecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	nameLine   = regexp.MustCompile(`(?m)^-- name: (\w+)`)
	scopeLine  = regexp.MustCompile(`(?m)^-- scope: (global|owner) — \S.*$`)
	scopeAny   = regexp.MustCompile(`(?m)^-- scope:`)
	projectNum = regexp.MustCompile(`\bproject_number\b`)
)

// Check fails t for every query in dir/*.sql that touches one of tables without filtering on
// project_number or a well-formed scope annotation.
func Check(t *testing.T, dir string, tables []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no query files in %s", dir)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range split(string(b)) {
			name := nameLine.FindStringSubmatch(q)[1]
			if !touches(q, tables) {
				continue
			}
			if scopeAny.MatchString(q) {
				if !scopeLine.MatchString(q) {
					t.Errorf("%s: %s: malformed scope annotation; use `-- scope: global — <reason>`", f, name)
				}
				continue
			}
			if !projectNum.MatchString(stripComments(q)) {
				t.Errorf("%s: %s touches a project-scoped table without filtering on project_number", f, name)
			}
		}
	}
}

func split(src string) []string {
	idx := nameLine.FindAllStringIndex(src, -1)
	out := make([]string, 0, len(idx))
	for i, loc := range idx {
		end := len(src)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, src[loc[0]:end])
	}
	return out
}

func touches(q string, tables []string) bool {
	body := stripComments(q)
	for _, table := range tables {
		if strings.Contains(body, table) {
			return true
		}
	}
	return false
}

func stripComments(q string) string {
	var b strings.Builder
	for _, line := range strings.Split(q, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
