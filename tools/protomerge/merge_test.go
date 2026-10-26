package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVendoredProtos holds the repository's own third_party trees to the merge rule.
func TestVendoredProtos(t *testing.T) {
	problems, err := Check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestVendoredWriteVerify pins the one field taken from firebase-js-sdk today.
func TestVendoredWriteVerify(t *testing.T) {
	f, err := parseFile(filepath.Join("..", "..", baseDir, "google/firestore/v1/write.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := f.scopes["Write"].byName["verify"]; !ok || m.num != 5 || m.typ != "string" {
		t.Fatalf("googleapis Write.verify = %+v, %v; want string verify = 5", m, ok)
	}
}

const jsWrite = `
syntax = "proto3";
package google.firestore.v1;
option go_package = "x;y";
// A comment with message Fake { and } in it.
message Write {
  oneof operation {
    Document update = 1;
    string delete = 2;
    string verify = 5; /* added by js-sdk */
    DocumentTransform transform = 6;
  }
  DocumentMask update_mask = 3 [(google.api.field_behavior) = OPTIONAL];
  message Inner { map<string, Value> fields = 1; }
  enum Kind { KIND_UNSPECIFIED = 0; ONE = 1; }
}
service S { rpc A(B) returns (C) { option (google.api.http) = { post: "/v1/{a=*}" body: "*" }; } }
`

func TestCheck(t *testing.T) {
	cases := []struct {
		name, base string
		want       []string // substrings, one per problem; none when the merge is right
	}{
		{"merged", strings.Replace(jsWrite, "/* added by js-sdk */", "", 1), nil},
		{"missing", strings.Replace(jsWrite, "string verify = 5;", "", 1),
			[]string{"Write.verify = 5 is only in firebase-js-sdk"}},
		{"collision", strings.Replace(jsWrite, "string verify = 5;", "string other = 5;", 1),
			[]string{"Write.verify = 5 collides with googleapis Write.other = 5"}},
		{"renumbered", strings.Replace(jsWrite, "string verify = 5;", "string verify = 7;", 1),
			[]string{"Write.verify = 5 has number 7"}},
		{"retyped", strings.Replace(jsWrite, "string verify = 5;", "bytes verify = 5;", 1),
			[]string{`Write.verify = 5 is "string" in firebase-js-sdk and "bytes"`}},
		{"reserved", strings.Replace(jsWrite, "string verify = 5;", "", 1) + "\n",
			nil}, // replaced below: reserved inside Write
		{"nested map", strings.Replace(jsWrite, "map<string, Value> fields = 1", "map<string, string> fields = 1", 1),
			[]string{`Write.Inner.fields = 1 is "map<string,Value>"`}},
		{"enum value", strings.Replace(jsWrite, "ONE = 1;", "TWO = 1;", 1),
			[]string{"Write.Kind.ONE = 1 collides with googleapis Write.Kind.TWO = 1"}},
		{"duplicate in googleapis", strings.Replace(jsWrite, "DocumentMask update_mask = 3", "DocumentMask update_mask = 2", 1),
			[]string{"delete and update_mask share number 2", "Write.update_mask = 3 has number 2"}},
	}
	cases[5].base = strings.Replace(cases[5].base, "message Write {", "message Write {\n  reserved 4 to 5, 9;\n  reserved \"verify\";", 1)
	cases[5].want = []string{"Write.verify = 5 collides with a number googleapis reserves"}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, jsDir, "google/firestore/v1/write.proto"), jsWrite)
			write(t, filepath.Join(root, baseDir, "google/firestore/v1/write.proto"), c.base)
			problems, err := Check(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(problems) != len(c.want) {
				t.Fatalf("problems %q, want %d matching %q", problems, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(problems[i], w) {
					t.Errorf("problem %q, want it to contain %q", problems[i], w)
				}
			}
		})
	}
}

func TestCheckFileOnlyInJS(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, jsDir, "google/firestore/v1/new.proto"), jsWrite)
	problems, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "only in firebase-js-sdk") {
		t.Fatalf("problems %q", problems)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
