package api

import (
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
)

func TestParseAdminName(t *testing.T) {
	const d = "projects/p/databases/(default)"
	for _, c := range []struct {
		name                string
		group, kind, id, db string
		hasID               bool
		code                wire.Code
	}{
		{name: d, db: "(default)"},
		{name: d + "/collectionGroups/-", group: "-", db: "(default)"},
		{name: d + "/collectionGroups/c/indexes", group: "c", kind: "indexes", db: "(default)"},
		{name: d + "/collectionGroups/c/indexes/CICAgOjXh4EK", group: "c", kind: "indexes", id: "CICAgOjXh4EK", hasID: true, db: "(default)"},
		{name: d + "/collectionGroups/c/fields/`a/b`.c", group: "c", kind: "fields", id: "`a/b`.c", hasID: true, db: "(default)"},
		{name: d + "/operations/x", kind: "operations", id: "x", hasID: true, db: "(default)"},
		{name: "projects/p/databases/named-db", db: "named-db"},
		{name: "projects/p/databases/Upper-case", code: wire.CodeNotFound},
		{name: "projects/p", code: wire.CodeInvalidArgument},
		{name: "organizations/x/databases/(default)", code: wire.CodeInvalidArgument},
		{name: d + "/collectionGroups/c/operations/x", code: wire.CodeInvalidArgument},
		{name: d + "/indexes/x", code: wire.CodeInvalidArgument},
		{name: d + "/collectionGroups//indexes", code: wire.CodeInvalidArgument},
		{name: d + "/collectionGroups/c/indexes/", code: wire.CodeInvalidArgument},
		{name: d + "/backups/x", code: wire.CodeInvalidArgument},
	} {
		n, err := parseAdminName(c.name)
		if c.code != wire.CodeOK {
			if wire.From(err).Code != c.code {
				t.Errorf("%s: %v, want %v", c.name, err, c.code)
			}
			continue
		}
		if err != nil || n.group != c.group || n.kind != c.kind || n.id != c.id || n.hasID != c.hasID || n.db.ID != c.db || n.db.Project != "p" {
			t.Errorf("%s: %+v %v", c.name, n, err)
		}
	}
}

func TestFieldFilter(t *testing.T) {
	own := &adminpb.Field{IndexConfig: &adminpb.Field_IndexConfig{}}
	inherited := &adminpb.Field{IndexConfig: &adminpb.Field_IndexConfig{UsesAncestorConfig: true}}
	ttl := &adminpb.Field{IndexConfig: &adminpb.Field_IndexConfig{UsesAncestorConfig: true}, TtlConfig: &adminpb.Field_TtlConfig{}}
	for _, c := range []struct {
		filter               string
		own, inherited, ttls bool
	}{
		{"indexConfig.usesAncestorConfig=false OR ttlConfig:*", true, false, true},
		{"indexConfig.usesAncestorConfig:false", true, false, false},
		{"index_config.uses_ancestor_config = false", true, false, false},
		{"ttlConfig:*", false, false, true},
		{"ttl_config:* OR indexConfig.usesAncestorConfig:false", true, false, true},
	} {
		keep, err := fieldFilter(c.filter)
		if err != nil {
			t.Errorf("%q: %v", c.filter, err)
			continue
		}
		if keep(own) != c.own || keep(inherited) != c.inherited || keep(ttl) != c.ttls {
			t.Errorf("%q: %v %v %v", c.filter, keep(own), keep(inherited), keep(ttl))
		}
	}
	for _, f := range []string{"", "nonsense", "indexConfig.usesAncestorConfig=true", "ttlConfig:* AND indexConfig.usesAncestorConfig:false", "ttlConfig:* OR"} {
		if _, err := fieldFilter(f); err != errFieldFilter {
			t.Errorf("%q: %v", f, err)
		}
	}
}

func TestOperationNotFound(t *testing.T) {
	for id, want := range map[string]string{
		"nonexistent":            "Operation does not exist",
		"AAAAAAAAAAAAAAAAAAAAAA": "Requested entity was not found.",
		"ASA0NjE1MDcyMDYxChp0bHVhZmVkBxJsYXJ0bmVjc3Utc2Jvai1uaW1kYRQKLRI": "Requested entity was not found.",
		"not base64!": "Operation does not exist",
	} {
		if got := wire.From(operationNotFound(id)); got.Code != wire.CodeNotFound || got.Message != want {
			t.Errorf("%s: %v", id, got)
		}
	}
}

func TestAdminPage(t *testing.T) {
	all := []int{1, 2, 3, 4, 5}
	page, next, err := adminPage(all, 2, "")
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("%v %q %v", page, next, err)
	}
	page, next, err = adminPage(all, 2, next)
	if err != nil || page[0] != 3 || next == "" {
		t.Fatalf("%v %q %v", page, next, err)
	}
	page, next, err = adminPage(all, 0, next)
	if err != nil || len(page) != 1 || page[0] != 5 || next != "" {
		t.Fatalf("%v %q %v", page, next, err)
	}
	for _, tok := range []string{"abc", "bzk", "bzA"} { // garbage, o9 (past the end), o0
		if _, _, err := adminPage(all, 0, tok); err != errAdminPageTok {
			t.Errorf("%q: %v", tok, err)
		}
	}
	if _, _, err := adminPage(all, -1, ""); err == nil {
		t.Error("a negative page size")
	}
	if err := wholeList(1, ""); err != errPageSize {
		t.Errorf("wholeList: %v", err)
	}
}
